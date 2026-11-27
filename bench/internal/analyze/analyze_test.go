package analyze_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// analyzeGolden runs the production path on a scenario's bundle, generated
// into a temporary directory: AnalyzeBundle reads every file back and writes
// analysis.json. It returns the result and the bundle directory.
func analyzeGolden(t *testing.T, name string) (spec.AnalysisResult, string) {
	t.Helper()
	dir := writeScenario(t, analyzetest.ScenarioNamed(name))
	result, err := analyze.AnalyzeBundle(context.Background(), dir)
	if err != nil {
		t.Fatalf("analyze %s: %v", name, err)
	}
	return result, dir
}

type sloExpectation struct {
	validStage     int
	failedStage    int // -1 for none
	isInterpolated bool
	limiting       spec.SloName
}

type kneeExpectation struct {
	stage      int
	reason     spec.KneeReason
	bottleneck spec.BottleneckResource
	isWeak     bool
}

type expectation struct {
	grade                  spec.QualityGrade
	runIssues              []spec.QualityIssueCode         // exactly these, order aside
	excluded               []int                           // exactly these stages
	stageIssues            map[int][]spec.QualityIssueCode // each must be present on the stage
	notStageIssues         map[int][]spec.QualityIssueCode // each must be absent from the stage
	clientBound            []int                           // exactly these stages
	excludedFromComparison bool
	knobs                  map[string]spec.KnobStatus
	sloMax                 *sloExpectation  // nil: slo_max must be absent
	knee                   *kneeExpectation // nil: no knee
	// advice is exactly the advisor rules in recommendations.json, in order:
	// every rule but sizing.*, with "!" after an urgent one. nil skips the check.
	advice []string
	// leak expects the memory limit's possible-leak caveat.
	leak bool
}

func TestGoldenBundles(t *testing.T) {
	cases := map[string]expectation{
		"clean-ladder": {
			grade:  spec.QualityGradeGood,
			knobs:  map[string]spec.KnobStatus{"vllm.max_num_seqs": spec.KnobStatusVerified, "vllm.enable_prefix_caching": spec.KnobStatusVerified},
			sloMax: &sloExpectation{validStage: 3, failedStage: 4, isInterpolated: true, limiting: spec.SloNameTtftP95},
			knee:   &kneeExpectation{stage: 4, reason: spec.KneeReasonLatencySlope, bottleneck: spec.BottleneckResourceKvCache},
		},
		"achieved-rps-low": {
			grade:          spec.QualityGradeDegraded,
			runIssues:      []spec.QualityIssueCode{spec.QualityIssueCodeCatalogMissing}, // no bench series
			excluded:       []int{1},
			stageIssues:    map[int][]spec.QualityIssueCode{1: {spec.QualityIssueCodeAchievedRpsLow}},
			notStageIssues: map[int][]spec.QualityIssueCode{0: {spec.QualityIssueCodeAchievedRpsLow}, 2: {spec.QualityIssueCodeAchievedRpsLow}},
			sloMax:         &sloExpectation{validStage: 2, failedStage: -1},
		},
		"send-lag-high": {
			grade: spec.QualityGradeDegraded, excluded: []int{1},
			stageIssues:    map[int][]spec.QualityIssueCode{1: {spec.QualityIssueCodeSendLagHigh}},
			notStageIssues: map[int][]spec.QualityIssueCode{1: {spec.QualityIssueCodeClientBound}},
			sloMax:         &sloExpectation{validStage: 0, failedStage: -1},
		},
		"client-bound-lag-growth": {
			grade: spec.QualityGradeDegraded, excluded: []int{1}, clientBound: []int{1},
			stageIssues:    map[int][]spec.QualityIssueCode{1: {spec.QualityIssueCodeClientBound}},
			notStageIssues: map[int][]spec.QualityIssueCode{1: {spec.QualityIssueCodeSendLagHigh}},
			sloMax:         &sloExpectation{validStage: 0, failedStage: -1},
		},
		"client-bound-cpu": {
			grade: spec.QualityGradeDegraded, excluded: []int{1, 2}, clientBound: []int{1, 2},
			stageIssues: map[int][]spec.QualityIssueCode{1: {spec.QualityIssueCodeClientBound}, 2: {spec.QualityIssueCodeClientBound}},
			sloMax:      &sloExpectation{validStage: 0, failedStage: -1},
		},
		"low-resolution": {
			grade:       spec.QualityGradeDegraded,
			stageIssues: map[int][]spec.QualityIssueCode{0: {spec.QualityIssueCodeLowResolution}, 1: {spec.QualityIssueCodeLowResolution}},
			sloMax:      &sloExpectation{validStage: 1, failedStage: -1}, // client statistics are still used
		},
		"insufficient-samples": {
			grade:       spec.QualityGradeDegraded,
			stageIssues: map[int][]spec.QualityIssueCode{0: {spec.QualityIssueCodeInsufficientSamples}, 1: {spec.QualityIssueCodeInsufficientSamples}},
			// p95 cannot be observed from 150 samples, so no SLO can be shown to hold.
		},
		"config-drift-restart": {
			grade:          spec.QualityGradeDegraded,
			runIssues:      []spec.QualityIssueCode{spec.QualityIssueCodeConfigDrift},
			excluded:       []int{2, 3},
			stageIssues:    map[int][]spec.QualityIssueCode{2: {spec.QualityIssueCodeConfigDrift}, 3: {spec.QualityIssueCodeConfigDrift}},
			notStageIssues: map[int][]spec.QualityIssueCode{0: {spec.QualityIssueCodeConfigDrift}, 1: {spec.QualityIssueCodeConfigDrift}},
			sloMax:         &sloExpectation{validStage: 1, failedStage: -1},
		},
		"config-drift-unknown-time": {
			grade:     spec.QualityGradeUnusable,
			runIssues: []spec.QualityIssueCode{spec.QualityIssueCodeConfigDrift},
			excluded:  []int{0, 1},
		},
		"oom-restart": {
			grade:     spec.QualityGradeDegraded,
			runIssues: []spec.QualityIssueCode{spec.QualityIssueCodeOomRestart, spec.QualityIssueCodeConfigDrift},
			excluded:  []int{2},
			sloMax:    &sloExpectation{validStage: 1, failedStage: -1},
		},
		"knobs": {
			grade:                  spec.QualityGradeDegraded,
			runIssues:              []spec.QualityIssueCode{spec.QualityIssueCodeKnobMismatch, spec.QualityIssueCodeKnobUnverified},
			excludedFromComparison: true,
			knobs: map[string]spec.KnobStatus{
				"vllm.max_num_seqs": spec.KnobStatusMismatch, "vllm.enable_prefix_caching": spec.KnobStatusVerified,
				"bifrost.concurrency": spec.KnobStatusUnverified,
			},
			sloMax: &sloExpectation{validStage: 1, failedStage: -1},
		},
		"coverage": {
			grade:     spec.QualityGradeDegraded,
			runIssues: []spec.QualityIssueCode{spec.QualityIssueCodeCatalogMissing, spec.QualityIssueCodeSeriesGaps},
			sloMax:    &sloExpectation{validStage: 1, failedStage: -1},
		},
		"knee-queueing": {
			grade:  spec.QualityGradeGood,
			sloMax: &sloExpectation{validStage: 3, failedStage: -1},
			knee:   &kneeExpectation{stage: 3, reason: spec.KneeReasonQueueing, bottleneck: spec.BottleneckResourceKvCache},
		},
		"knee-preemptions-weak-gpu": {
			grade:  spec.QualityGradeGood,
			sloMax: &sloExpectation{validStage: 3, failedStage: -1},
			knee:   &kneeExpectation{stage: 3, reason: spec.KneeReasonPreemptions, bottleneck: spec.BottleneckResourceGpuUtil, isWeak: true},
		},
		"excluded-stage-ignored": {
			grade:       spec.QualityGradeDegraded,
			excluded:    []int{2},
			stageIssues: map[int][]spec.QualityIssueCode{2: {spec.QualityIssueCodeSendLagHigh}},
			sloMax:      &sloExpectation{validStage: 3, failedStage: -1}, // stage 2 failed every SLO but was excluded
		},
		"error-rate-slo": {
			grade:  spec.QualityGradeGood,
			sloMax: &sloExpectation{validStage: 0, failedStage: 1, isInterpolated: true, limiting: spec.SloNameMaxErrorRate},
		},
		// The Bifrost pathologies. Each run passes its SLOs
		// throughout, so its top stage is the operating point. B2 is urgent
		// wherever Bifrost's default queue of 5000 is past B_max.
		"bifrost-concurrency-bound": {
			grade: spec.QualityGradeGood, sloMax: &sloExpectation{validStage: 3, failedStage: -1},
			// Streaming, a slot is held until the first token, so a wait shows in the
			// TTFT: with a 1 s TTFT SLO and μ ≈ 92/s, B_max is 78, and the queue of 100
			// is past it (urgent B2).
			advice: []string{"B1", "B2!", "X1"},
		},
		"bifrost-queue-drops": {
			grade: spec.QualityGradeGood, sloMax: &sloExpectation{validStage: 3, failedStage: -1},
			advice: []string{"queueing.concurrency", "B2!", "X1"},
		},
		"bifrost-upstream-bound": {
			grade: spec.QualityGradeGood, sloMax: &sloExpectation{validStage: 3, failedStage: -1},
			advice: []string{"queueing.concurrency", "B2!", "B3", "X1"},
		},
		"bifrost-throttled": {
			grade: spec.QualityGradeGood, sloMax: &sloExpectation{validStage: 3, failedStage: -1},
			advice: []string{"queueing.concurrency", "B2!", "B6", "B8", "X1"},
		},
		"bifrost-memory-leak": {
			grade: spec.QualityGradeGood, sloMax: &sloExpectation{validStage: 1, failedStage: -1},
			advice: []string{"queueing.concurrency", "B2!", "X1"}, leak: true,
		},
	}
	for _, scenario := range analyzetest.Scenarios() {
		if _, covered := cases[scenario.Name]; !covered {
			t.Errorf("golden bundle %s has no expectation", scenario.Name)
		}
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			result, dir := analyzeGolden(t, name)
			checkExpectation(t, result, want)
			checkAdvice(t, dir, want)
		})
	}
}

func checkAdvice(t *testing.T, dir string, want expectation) {
	t.Helper()
	if want.advice == nil && !want.leak {
		return
	}
	encoded, err := os.ReadFile(filepath.Join(dir, bundle.RecommendationsJSON))
	if err != nil {
		t.Fatal(err)
	}
	var document spec.Recommendations
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	var advice []string
	hasLeak := false
	for _, recommendation := range document.Recommendations {
		if recommendation.Subject.Name == "memory.limit" && strings.Contains(strings.Join(recommendation.Caveats, " "), "a possible leak") {
			hasLeak = true
		}
		if strings.HasPrefix(recommendation.Rule, "sizing.") {
			continue
		}
		rule := recommendation.Rule
		if recommendation.IsUrgent != nil && *recommendation.IsUrgent {
			rule += "!"
		}
		advice = append(advice, rule)
	}
	if want.advice != nil && !slices.Equal(advice, want.advice) {
		t.Errorf("advice = %v, want %v", advice, want.advice)
	}
	if hasLeak != want.leak {
		t.Errorf("possible-leak caveat = %v, want %v", hasLeak, want.leak)
	}
}

func issueCodes(issues []spec.QualityIssue) []spec.QualityIssueCode {
	codes := make([]spec.QualityIssueCode, len(issues))
	for i, issue := range issues {
		codes[i] = issue.Code
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

func checkExpectation(t *testing.T, result spec.AnalysisResult, want expectation) {
	t.Helper()
	quality := result.Quality
	if quality.Grade != want.grade {
		t.Errorf("grade = %s, want %s", quality.Grade, want.grade)
	}
	if quality.IsExcludedFromComparison != want.excludedFromComparison {
		t.Errorf("is_excluded_from_comparison = %v", quality.IsExcludedFromComparison)
	}
	wantRun := slices.Clone(want.runIssues)
	slices.Sort(wantRun)
	if got := issueCodes(quality.Issues); !slices.Equal(got, slices.Compact(wantRun)) {
		t.Errorf("run issues = %v, want %v", got, wantRun)
	}
	var excluded, clientBound []int
	for _, stage := range quality.Stages {
		if stage.IsExcluded {
			excluded = append(excluded, stage.StageIndex)
		}
		if stage.IsClientBound {
			clientBound = append(clientBound, stage.StageIndex)
		}
		got := issueCodes(stage.Issues)
		for _, code := range want.stageIssues[stage.StageIndex] {
			if !slices.Contains(got, code) {
				t.Errorf("stage %d lacks issue %s (has %v)", stage.StageIndex, code, got)
			}
		}
		for _, code := range want.notStageIssues[stage.StageIndex] {
			if slices.Contains(got, code) {
				t.Errorf("stage %d must not have issue %s", stage.StageIndex, code)
			}
		}
	}
	if !slices.Equal(excluded, want.excluded) {
		t.Errorf("excluded stages = %v, want %v", excluded, want.excluded)
	}
	if !slices.Equal(clientBound, want.clientBound) {
		t.Errorf("client_bound stages = %v, want %v", clientBound, want.clientBound)
	}
	for _, knob := range quality.Knobs {
		if status, expected := want.knobs[knob.Name]; expected && knob.Status != status {
			t.Errorf("knob %s = %s, want %s", knob.Name, knob.Status, status)
		}
	}
	checkSLOMax(t, result.Capacity.SloMax, want.sloMax)
	checkKnee(t, result.Capacity.Knee, want.knee)
}

func checkSLOMax(t *testing.T, got *spec.SloMaxRps, want *sloExpectation) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("slo_max = %+v, want none", got)
		}
		return
	}
	if got == nil {
		t.Fatal("slo_max is absent")
	}
	if got.ValidStageIndex != want.validStage || got.IsInterpolated != want.isInterpolated {
		t.Errorf("slo_max = %+v, want valid stage %d, interpolated %v", got, want.validStage, want.isInterpolated)
	}
	if want.failedStage < 0 {
		if got.FailedStageIndex != nil {
			t.Errorf("slo_max has a failed stage %d, want none", *got.FailedStageIndex)
		}
		return
	}
	if got.FailedStageIndex == nil || *got.FailedStageIndex != want.failedStage {
		t.Errorf("slo_max failed stage = %v, want %d", got.FailedStageIndex, want.failedStage)
	}
	if got.LimitingSlo == nil || *got.LimitingSlo != want.limiting {
		t.Errorf("limiting slo = %v, want %s", got.LimitingSlo, want.limiting)
	}
}

func checkKnee(t *testing.T, got *spec.Knee, want *kneeExpectation) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("knee = %+v, want none", got)
		}
		return
	}
	if got == nil {
		t.Fatal("knee is absent")
	}
	if got.StageIndex != want.stage || got.Reason != want.reason {
		t.Errorf("knee = stage %d, %s; want stage %d, %s", got.StageIndex, got.Reason, want.stage, want.reason)
	}
	if got.Bottleneck == nil || got.Bottleneck.Resource != want.bottleneck || got.Bottleneck.IsWeakSignal != want.isWeak {
		t.Errorf("bottleneck = %+v, want %s (weak %v)", got.Bottleneck, want.bottleneck, want.isWeak)
	}
}

func TestCleanLadderNumbers(t *testing.T) {
	result, _ := analyzeGolden(t, "clean-ladder")
	if len(result.Stages) != 6 {
		t.Fatalf("stages = %d", len(result.Stages))
	}
	// Stage 3 holds at p95 TTFT 0.308s and stage 4 fails at 0.540s, so the 0.5s
	// threshold is crossed (0.5-0.308)/(0.540-0.308) of the way from 7 to 8 RPS.
	slo := result.Capacity.SloMax
	lowTTFT := *result.Stages[3].Ttft.P95Seconds.Value
	highTTFT := *result.Stages[4].Ttft.P95Seconds.Value
	want := 7 + (0.5-lowTTFT)/(highTTFT-lowTTFT)*(8-7)
	if math.Abs(slo.Rps-want) > 1e-9 {
		t.Errorf("interpolated slo_max = %v, want %v", slo.Rps, want)
	}
	replica := result.Capacity.PerReplica
	if replica.ReadyReplicas != 2 || *replica.TotalGpus != 2 || math.Abs(replica.RpsPerReplica-slo.Rps/2) > 1e-9 || math.Abs(*replica.RpsPerGpu-slo.Rps/2) > 1e-9 {
		t.Errorf("per_replica = %+v", replica)
	}
	if result.HardwareProfileId == nil || *result.HardwareProfileId != "hardware-synthetic" {
		t.Errorf("hardware_profile_id = %v", result.HardwareProfileId)
	}
	knee := result.Capacity.Knee
	if *knee.SlopeRatio <= 2 || knee.Bottleneck.Utilization != 0.93 {
		t.Errorf("knee = %+v, bottleneck = %+v", knee, knee.Bottleneck)
	}
	for _, stage := range result.Stages {
		if stage.Requests == 0 || stage.GoodputRps == nil || stage.SendLagP99Seconds == nil || stage.OutputTokensPerSecond == nil {
			t.Errorf("stage %d is missing client figures: %+v", stage.StageIndex, stage)
		}
	}
}

func TestErrorRateSLOGolden(t *testing.T) {
	result, _ := analyzeGolden(t, "error-rate-slo")
	stage := result.Stages[1]
	if stage.Errors != 18 || len(stage.ErrorsByClass) != 1 || stage.ErrorsByClass[0].ErrorClass != "upstream_error" || stage.ErrorsByClass[0].Count != 18 {
		t.Errorf("errors = %d, %+v", stage.Errors, stage.ErrorsByClass)
	}
	// 5% observed against a 1% limit, from 0%: crossed a fifth of the way up from 4 to 6 RPS.
	if got := result.Capacity.SloMax.Rps; math.Abs(got-4.4) > 1e-9 {
		t.Errorf("slo_max rps = %v, want 4.4", got)
	}
}

// TestAnalysisIsDeterministic is the byte-for-byte guarantee of `bench
// analyze`: the same raw files give the same output files every time. Map
// iteration order only shows in some runs, so each scenario is re-analysed
// several times.
func TestAnalysisIsDeterministic(t *testing.T) {
	outputs := []string{bundle.AnalysisJSON, bundle.RecommendationsJSON, bundle.CapacityJSON, bundle.CapacityCSV,
		bundle.CapacityModelJSON, bundle.ResourcesPatch, bundle.BifrostPatch}
	read := func(dir string) map[string][]byte {
		files := map[string][]byte{}
		for _, name := range outputs {
			if content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name))); err == nil {
				files[name] = content
			}
		}
		return files
	}
	for _, name := range []string{"clean-ladder", "knobs", "coverage", "bifrost-queue-drops", "bifrost-upstream-bound"} {
		t.Run(name, func(t *testing.T) {
			_, dir := analyzeGolden(t, name)
			first := read(dir)
			if len(first) == 0 {
				t.Fatal("analyze wrote no output files")
			}
			if err := os.Remove(filepath.Join(dir, bundle.AnalysisJSON)); err != nil {
				t.Fatal(err)
			}
			// Re-analysing over existing outputs must not read them back, so the
			// later runs also cover that.
			for run := 2; run <= 5; run++ {
				if _, err := analyze.AnalyzeBundle(context.Background(), dir); err != nil {
					t.Fatal(err)
				}
				again := read(dir)
				for file, content := range first {
					if !bytes.Equal(content, again[file]) {
						t.Errorf("run %d wrote a different %s", run, file)
					}
				}
			}
		})
	}
}

func TestAnalysisDependsOnTheRunID(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("clean-ladder")
	run, err := scenario.Run()
	if err != nil {
		t.Fatal(err)
	}
	first, err := analyze.Analyze(run)
	if err != nil {
		t.Fatal(err)
	}
	run.ID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	second, err := analyze.Analyze(run)
	if err != nil {
		t.Fatal(err)
	}
	if *first.Stages[0].Ttft.P50Seconds.Value != *second.Stages[0].Ttft.P50Seconds.Value {
		t.Error("point values must not depend on the seed")
	}
	var isAnyDifferent bool
	for i := range first.Stages {
		a, b := first.Stages[i].E2E, second.Stages[i].E2E
		isAnyDifferent = isAnyDifferent || *a.P50Seconds.CiLow != *b.P50Seconds.CiLow || *a.P90Seconds.CiHigh != *b.P90Seconds.CiHigh ||
			*a.P95Seconds.CiLow != *b.P95Seconds.CiLow
	}
	if !isAnyDifferent {
		t.Error("the run id must seed the bootstrap")
	}
}

func TestAnalysisJSONMatchesItsSchemaAndRoundTrips(t *testing.T) {
	_, dir := analyzeGolden(t, "clean-ladder")
	content, err := os.ReadFile(filepath.Join(dir, bundle.AnalysisJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaAnalysis, content); err != nil {
		t.Fatal(err)
	}
	parsed, err := spec.Parse[spec.AnalysisResult](spec.SchemaAnalysis, content)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AnalyzerVersion != analyze.AnalyzerVersion {
		t.Errorf("analyzer_version = %q", parsed.AnalyzerVersion)
	}
	if err := analyze.SelfCheck(parsed); err != nil {
		t.Errorf("a written analysis must pass its own self-check: %v", err)
	}
}
