package quality

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// RunInput is everything the run-level gate reads.
type RunInput struct {
	Stages []StageInput
	// Warnings are all stage-length warnings of phases.json; those without a
	// stage index concern the warmup.
	Warnings []spec.StageLengthWarning
	Declared spec.RunSpecKnobs
	Captured map[string]spec.KnobValue
	Pre      *spec.TargetSnapshot
	Post     *spec.TargetSnapshot
	Series   input.SignalSource
	// Matrix is the raw catalog entries, read for coverage only.
	Matrix  input.MatrixSource
	Queries *spec.PrometheusQueries
	// BurstStages are the indexes of the RunSpec's is_burst stages.
	BurstStages map[int]bool
}

// RunResult is the run quality plus the facts the capacity model reuses.
type RunResult struct {
	Quality spec.RunQuality
	// LowResolution marks stages whose server-side statistics are too coarse,
	// by stage index.
	LowResolution map[int]bool
}

// Check runs every quality check and grades the run. A stage that fails any
// stage check, or that ends after a config change, is excluded.
func Check(in RunInput) (RunResult, error) {
	result := RunResult{LowResolution: map[int]bool{}}
	quality := spec.RunQuality{Stages: []spec.StageQuality{}}

	var hasBenchSeries bool
	for _, stage := range in.Stages {
		outcome, err := CheckStage(stage)
		if err != nil {
			return RunResult{}, fmt.Errorf("stage %d: %w", stage.Stage.Index, err)
		}
		hasBenchSeries = hasBenchSeries || outcome.HasBenchSeries
		result.LowResolution[stage.Stage.Index] = outcome.IsLowResolution
		quality.Stages = append(quality.Stages, outcome.Quality)
	}
	if len(in.Stages) > 0 && !hasBenchSeries {
		quality.Issues = append(quality.Issues, spec.QualityIssue{
			Code:    spec.QualityIssueCodeCatalogMissing,
			Message: "the bench pod's CPU series (kubernetes catalog) were not collected, or its CPU usage could not be related to a CPU limit because it has none, so client_bound rests on send-lag records alone",
		})
	}
	for _, stage := range quality.Stages {
		if IsTargetStalled(stage) {
			quality.IsExcludedFromComparison = true
			quality.Issues = append(quality.Issues, spec.QualityIssue{
				Code:    spec.QualityIssueCodeTargetStalled,
				Message: fmt.Sprintf("the target's engine stalled in stage %d, so the run is excluded from experiment comparisons; restart the target and run again", stage.StageIndex),
			})
			break
		}
	}
	quality.Issues = append(quality.Issues, warmupIssues(in.Warnings)...)
	markBurstStages(&quality, in.BurstStages)

	driftIssues, err := applyDrift(&quality, in)
	if err != nil {
		return RunResult{}, err
	}
	quality.Issues = append(quality.Issues, driftIssues...)

	quality.Knobs = CheckKnobs(in.Declared, in.Captured)
	for _, knob := range quality.Knobs {
		switch knob.Status {
		case spec.KnobStatusMismatch:
			quality.IsExcludedFromComparison = true
			quality.Issues = append(quality.Issues, spec.QualityIssue{
				Code:    spec.QualityIssueCodeKnobMismatch,
				Message: fmt.Sprintf("%s declared %s, captured %s: the change was not applied, so the run is excluded from experiment comparisons", knob.Name, knob.Declared, *knob.Actual),
			})
		case spec.KnobStatusUnverified:
			quality.Issues = append(quality.Issues, spec.QualityIssue{
				Code:    spec.QualityIssueCodeKnobUnverified,
				Message: fmt.Sprintf("%s declared %s could not be captured from the target", knob.Name, knob.Declared),
			})
		}
	}

	if issue, batched := streamBatchedIssue(in.Declared, in.Captured); batched {
		quality.Issues = append(quality.Issues, issue)
	}

	coverage, coverageIssues, err := CheckCoverage(in.Queries, in.Matrix)
	if err != nil {
		return RunResult{}, err
	}
	quality.Coverage = coverage
	quality.Issues = append(quality.Issues, coverageIssues...)

	quality.Grade = Grade(quality)
	result.Quality = quality
	return result, nil
}

// streamBatchedIssue flags a vLLM target that streams several tokens per chunk
// (--stream-interval above 1): the client's inter-token latency then measures
// the gaps between chunks, not between tokens. TTFT and end-to-end latency are
// unaffected. The captured value wins; the declared one stands in when nothing
// was captured.
func streamBatchedIssue(declared spec.RunSpecKnobs, captured map[string]spec.KnobValue) (spec.QualityIssue, bool) {
	value, found := captured[target.KnobVLLMStreamInterval]
	if !found {
		value, found = declared[target.KnobVLLMStreamInterval]
	}
	interval, isNumber := value.(float64)
	if !found || !isNumber || interval <= 1 {
		return spec.QualityIssue{}, false
	}
	return spec.QualityIssue{
		Code: spec.QualityIssueCodeStreamBatched,
		Message: fmt.Sprintf("vLLM streams %s tokens per chunk (--stream-interval), so inter-token latency measures the gaps between chunks, not between tokens; TTFT and end-to-end latency are unaffected. Benchmark with --stream-interval 1 to measure ITL",
			strconv.FormatFloat(interval, 'f', -1, 64)),
	}, true
}

// warmupIssues turns the warmup warning of phases.json (the only one without a
// stage) into a run-level low_resolution issue.
func warmupIssues(warnings []spec.StageLengthWarning) []spec.QualityIssue {
	var issues []spec.QualityIssue
	for _, warning := range warnings {
		if warning.Rule == spec.StageLengthRuleWarmupDuration {
			issues = append(issues, spec.QualityIssue{Code: spec.QualityIssueCodeLowResolution, Message: "recorded at run time: " + warning.Message})
		}
	}
	return issues
}

// applyDrift excludes the stages after a config change
// and returns the run-level issues. The change time comes from the replica and
// restart series; without it every stage is excluded, because no stage can be
// shown to predate the change.
func applyDrift(quality *spec.RunQuality, in RunInput) ([]spec.QualityIssue, error) {
	drift := DetectDrift(in.Pre, in.Post)
	var issues []spec.QualityIssue
	for _, killed := range drift.OOMKills {
		issues = append(issues, spec.QualityIssue{Code: spec.QualityIssueCodeOomRestart, Message: killed + " was OOMKilled and restarted during the run: a finding, not just an exclusion"})
	}
	if len(drift.Changes) == 0 {
		return issues, nil
	}
	changedAt, isKnown, err := ChangeTime(in.Series)
	if err != nil {
		return nil, err
	}
	var excluded []string
	for i, stage := range in.Stages {
		if isKnown && !stage.Stage.End.After(changedAt) {
			continue
		}
		excluded = append(excluded, fmt.Sprint(stage.Stage.Index))
		quality.Stages[i].IsExcluded = true
		quality.Stages[i].Issues = append(quality.Stages[i].Issues, spec.QualityIssue{
			Code:    spec.QualityIssueCodeConfigDrift,
			Message: "the target changed during the run: " + strings.Join(drift.Changes, "; "),
		})
	}
	when := "the change time is unknown, so every stage is excluded"
	if isKnown {
		when = "the change happened at " + changedAt.UTC().Format("2006-01-02T15:04:05Z") + ", so later stages are excluded"
	}
	issues = append(issues, spec.QualityIssue{
		Code:    spec.QualityIssueCodeConfigDrift,
		Message: fmt.Sprintf("pre- and post-run snapshots differ (%s); %s (stages: %s)", strings.Join(drift.Changes, "; "), when, strings.Join(excluded, ", ")),
	})
	return issues, nil
}

// Grade grades a run: unusable when no stage survives,
// degraded when any stage is excluded or any issue other than
// insufficient_samples is recorded, good otherwise. insufficient_samples only
// withholds a percentile, so it does not lower the grade by itself.
func Grade(quality spec.RunQuality) spec.QualityGrade {
	isAnyStageUsable := false
	isDegraded := false
	for _, stage := range quality.Stages {
		if IsBurstOnly(stage) {
			continue // a deliberate probe: neither usable for capacity nor a flaw
		}
		if !stage.IsExcluded {
			isAnyStageUsable = true
		} else {
			isDegraded = true
		}
		for _, issue := range stage.Issues {
			isDegraded = isDegraded || issue.Code != spec.QualityIssueCodeInsufficientSamples
		}
	}
	for _, issue := range quality.Issues {
		isDegraded = isDegraded || issue.Code != spec.QualityIssueCodeInsufficientSamples
	}
	switch {
	case !isAnyStageUsable:
		return spec.QualityGradeUnusable
	case isDegraded:
		return spec.QualityGradeDegraded
	default:
		return spec.QualityGradeGood
	}
}

// markBurstStages excludes the RunSpec's burst probes from the capacity
// ladder: they check the queue sizing, they do not measure capacity.
func markBurstStages(quality *spec.RunQuality, burst map[int]bool) {
	for i := range quality.Stages {
		stage := &quality.Stages[i]
		if !burst[stage.StageIndex] {
			continue
		}
		stage.IsExcluded = true
		stage.Issues = append(stage.Issues, spec.QualityIssue{
			Code:    spec.QualityIssueCodeBurstStage,
			Message: "a burst probe: kept out of the capacity ladder, and used to check the recommended queue",
		})
	}
}

// IsBurstOnly is a burst probe with no other issue. Too few scrapes or
// samples do not count: a short probe always has them, and its check reads
// the client's records, not server-side statistics.
func IsBurstOnly(stage spec.StageQuality) bool {
	isBurst := false
	for _, issue := range stage.Issues {
		switch issue.Code {
		case spec.QualityIssueCodeBurstStage:
			isBurst = true
		case spec.QualityIssueCodeInsufficientSamples, spec.QualityIssueCodeLowResolution:
		default:
			return false
		}
	}
	return isBurst
}
