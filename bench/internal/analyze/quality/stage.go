// Package quality is the data quality gate: it decides
// which stages can be trusted, and grades the run. A stage that fails a check
// is excluded from the capacity model; the reason is always recorded.
package quality

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// Thresholds of the gate. The first two are the RunSpec's analysis.stage_validity
// ("all three thresholds are configurable"); the rest are fixed here,
// each with the rule it comes from.
const (
	// TargetSaturatedInFlightRatio: send lag that grew while the requests in
	// flight reached 95% of the engine's cap is the target falling behind, not
	// the client: every slot was held by a request the target had not answered.
	TargetSaturatedInFlightRatio = 0.95
	// BenchCPUBoundRatio: the bench pod is client_bound at or above 90% of its
	// CPU limit.
	BenchCPUBoundRatio = 0.90
	// BenchThrottledRatio: 5% of CFS periods throttled, the threshold the sizing
	// uses for "the limit is too low by evidence".
	BenchThrottledRatio = 0.05
	// SendLagGrowthSeconds: "send lag grows across a stage" has no
	// number in the docs. The median send lag of the last third of the stage
	// exceeding that of the first third by 10ms, a fifth of the 50ms p99
	// ceiling, means the client is falling behind its schedule.
	SendLagGrowthSeconds = 0.010
	// SendLagGrowthMinRecords is how many records the thirds need to have a
	// meaningful median each.
	SendLagGrowthMinRecords = 30
	// ResolutionMinStage and ResolutionIntervals are the 60s and 10 x I terms of
	// the stage length rule; ResolutionE2EMultiple
	// is its 10 x mean E2E term (Rule B).
	ResolutionMinStage    = 60 * time.Second
	ResolutionIntervals   = 10
	ResolutionE2EMultiple = 10
	// ArrivalSigmas: with poisson arrivals the number of requests in a stage is
	// random with a standard deviation of sqrt(E) around its expectation E, so
	// a fixed 97% floor fails short stages by chance (at E = 300 the deviation is
	// 5.8%). A poisson stage passes at min(97% of E, E - 3 sqrt(E)): three
	// deviations of slack, and never stricter than the deterministic floor.
	ArrivalSigmas = 3.0
)

// StageInput is everything the per-stage checks read.
type StageInput struct {
	Stage input.Stage
	// SendLagGrowth is the stage's send-lag trend, computed while its rows were
	// in memory (SendLagGrowth), so a run's rows never all have to be held at
	// once. When it is nil the trend is computed from Rows.
	SendLagGrowth *SendLagTrend
	Rows          []engine.RequestRow
	Aggregate     aggregate.StageResult
	Validity      Validity
	Series        input.SignalSource
	Interval      aggregate.ScrapeInterval
	// Warnings are the stage-length warnings phases.json records for this stage.
	Warnings []spec.StageLengthWarning
	// PeakInFlight is the most requests in flight at once in the stage
	// (PeakInFlight), computed while its rows were in memory; nil computes it
	// from Rows. MaxInFlight is the engine's cap from environment.json, 0 when
	// the engine has none or did not record it.
	PeakInFlight *int
	MaxInFlight  int
}

// Validity are the stage validity thresholds of the RunSpec.
type Validity struct {
	MinAchievedRPSRatio float64
	MaxSendLagP99       time.Duration
	// IsRandomArrivals is true for poisson pacing, where the request count of a
	// stage is random (see ArrivalSigmas).
	IsRandomArrivals bool
}

// StageOutcome is the verdict on one stage before drift is applied.
type StageOutcome struct {
	Quality spec.StageQuality
	// IsLowResolution marks a stage whose server-side statistics are too coarse.
	IsLowResolution bool
	// HasBenchSeries says whether the bench pod's CPU series existed, so
	// client_bound could use them.
	HasBenchSeries bool
}

// CheckStage applies the quality checks that need only the stage's own data:
// validity, client_bound, resolution and Rule A.
func CheckStage(in StageInput) (StageOutcome, error) {
	outcome := StageOutcome{}
	var issues []spec.QualityIssue
	add := func(code spec.QualityIssueCode, format string, args ...any) {
		issues = append(issues, spec.QualityIssue{Code: code, Message: fmt.Sprintf(format, args...)})
	}

	aggregated := in.Aggregate.Aggregate
	expected := in.Stage.OfferedRPS * in.Stage.Duration().Seconds()
	required := requiredRequests(expected, in.Validity)
	isRateOK := aggregated.Requests > 0 && expected > 0 && float64(aggregated.Requests) >= required
	if !isRateOK {
		add(spec.QualityIssueCodeAchievedRpsLow, "achieved %.2f RPS is %.1f%% of the offered %.2f, below the required %.1f%%",
			aggregated.AchievedRps, percentOf(aggregated.AchievedRps, in.Stage.OfferedRPS), in.Stage.OfferedRPS, percentOf(required, expected))
	}
	if aggregated.Requests > 0 && aggregated.SendLagP99Seconds == nil {
		add(spec.QualityIssueCodeInsufficientSamples, "send lag unknown for this engine: its records carry no schedule, so the send lag ceiling and send lag growth were not checked")
	}
	isLagOK := aggregated.SendLagP99Seconds == nil || *aggregated.SendLagP99Seconds < in.Validity.MaxSendLagP99.Seconds()
	if !isLagOK {
		add(spec.QualityIssueCodeSendLagHigh, "p99 send lag %.1fms is not below %.1fms",
			*aggregated.SendLagP99Seconds*1000, in.Validity.MaxSendLagP99.Seconds()*1000)
	}

	clientBound, err := checkClientBound(in)
	if err != nil {
		return StageOutcome{}, err
	}
	outcome.HasBenchSeries = clientBound.hasBenchSeries
	for _, reason := range clientBound.reasons {
		add(spec.QualityIssueCodeClientBound, "%s", reason)
	}
	stalled, err := targetStall(in)
	if err != nil {
		return StageOutcome{}, err
	}
	switch {
	case stalled != "":
		// A stalled engine also piles requests up to the cap; the stall is the cause.
		add(spec.QualityIssueCodeTargetStalled, "%s", stalled)
		clientBound.targetSaturated = ""
	case clientBound.targetSaturated != "":
		add(spec.QualityIssueCodeTargetSaturated, "%s", clientBound.targetSaturated)
	}

	lowResolution := lowResolutionReasons(in)
	if len(lowResolution) > 0 {
		outcome.IsLowResolution = true
		add(spec.QualityIssueCodeLowResolution, "%s", strings.Join(lowResolution, "; "))
	}
	if message := insufficientSamplesMessage(aggregated, in.Warnings); message != "" {
		add(spec.QualityIssueCodeInsufficientSamples, "%s", message)
	}

	isValid := isRateOK && isLagOK && len(clientBound.reasons) == 0 && clientBound.targetSaturated == "" && stalled == ""
	outcome.Quality = spec.StageQuality{
		StageIndex:    in.Stage.Index,
		IsValid:       isValid,
		IsClientBound: len(clientBound.reasons) > 0,
		IsExcluded:    !isValid,
		Issues:        issues,
	}
	return outcome, nil
}

// requiredRequests is the fewest requests a stage may have and still count as
// having achieved its offered rate: the configured share of the
// expected count, relaxed for poisson arrivals.
func requiredRequests(expected float64, validity Validity) float64 {
	required := validity.MinAchievedRPSRatio * expected
	if validity.IsRandomArrivals {
		required = min(required, expected-ArrivalSigmas*math.Sqrt(expected))
	}
	return required
}

func percentOf(part, whole float64) float64 {
	if whole == 0 {
		return 0
	}
	return part / whole * 100
}

type clientBoundResult struct {
	reasons        []string
	hasBenchSeries bool
	// targetSaturated explains a send-lag growth that the target caused: the
	// requests in flight filled the engine's cap. It replaces that reason.
	targetSaturated string
}

// checkClientBound sets client_bound: send lag growing across the
// stage, or the bench pod at 90% of its CPU limit or throttled. The CPU
// evidence needs the kubernetes catalog; without it the verdict rests on the
// records alone, and the run-level check says so.
//
// The return values are named because a deferred step settles the send-lag
// reason after the CPU evidence is in.
func checkClientBound(in StageInput) (result clientBoundResult, err error) {
	trend := in.SendLagGrowth
	if trend == nil {
		computed := SendLagGrowth(in.Rows)
		trend = &computed
	}
	growthReason := ""
	if growth, ok := trend.Seconds, trend.IsKnown; ok && growth >= SendLagGrowthSeconds {
		growthReason = fmt.Sprintf("send lag grew %.1fms across the stage (limit %.0fms)", growth*1000, SendLagGrowthSeconds*1000)
	}
	defer func() {
		if growthReason == "" {
			return
		}
		// Only send lag points at the client: the bench CPU did not. If the
		// requests in flight filled the engine's cap, the lag is the target's.
		if len(result.reasons) == 0 && in.MaxInFlight > 0 {
			peak := in.peakInFlight()
			if float64(peak) >= TargetSaturatedInFlightRatio*float64(in.MaxInFlight) {
				result.targetSaturated = fmt.Sprintf("%s while %d requests were in flight against the engine's cap of %d: "+
					"the target fell behind and the client waited for it", growthReason, peak, in.MaxInFlight)
				return
			}
		}
		result.reasons = append([]string{growthReason}, result.reasons...)
	}()
	// Whether the bench pod's series exist at all is decided before trimming: a stage too short for
	// the scrape interval is a resolution problem (low_resolution), not a missing catalog.
	for _, signal := range []input.Signal{input.BenchCPUUsage, input.BenchCPUThrottle} {
		_, found, err := in.Series.Signal(signal)
		if err != nil {
			return result, fmt.Errorf("read signal %s: %w", signal, err)
		}
		result.hasBenchSeries = result.hasBenchSeries || found
	}
	start, end, ok := aggregate.TrimWindow(in.Stage, aggregate.Gauge, in.Interval.Duration, 0)
	if !ok {
		return result, nil
	}
	cpu, hasCPU, err := windowStats(in.Series, input.BenchCPUUsage, start, end)
	if err != nil {
		return result, err
	}
	throttle, hasThrottle, err := windowStats(in.Series, input.BenchCPUThrottle, start, end)
	if err != nil {
		return result, err
	}
	if hasCPU && cpu.Mean >= BenchCPUBoundRatio {
		result.reasons = append(result.reasons, fmt.Sprintf("bench CPU averaged %.0f%% of its limit (limit %.0f%%)", cpu.Mean*100, BenchCPUBoundRatio*100))
	}
	if hasThrottle && throttle.Mean >= BenchThrottledRatio {
		result.reasons = append(result.reasons, fmt.Sprintf("bench CPU was throttled in %.0f%% of periods (limit %.0f%%)", throttle.Mean*100, BenchThrottledRatio*100))
	}
	return result, nil
}

func windowStats(series input.SignalSource, signal input.Signal, start, end time.Time) (aggregate.WindowStats, bool, error) {
	samples, found, err := series.Signal(signal)
	if err != nil {
		return aggregate.WindowStats{}, false, fmt.Errorf("read signal %s: %w", signal, err)
	}
	if !found {
		return aggregate.WindowStats{}, false, nil
	}
	stats, ok := aggregate.StatsInWindow(samples, start, end)
	return stats, ok, nil
}

// sendLagGrowth is the median send lag of the last third of the stage's
// requests (by scheduled time) minus that of the first third. ok is false with
// too few records to tell.
// SendLagTrend is how much a stage's send lag grew: the median lag of its last
// third of requests minus that of its first third, by send time. IsKnown is
// false when too few requests have a known lag to judge.
type SendLagTrend struct {
	Seconds float64
	IsKnown bool
}

// SendLagGrowth is the send-lag trend of one stage's rows.
func SendLagGrowth(rows []engine.RequestRow) SendLagTrend {
	seconds, isKnown := sendLagGrowth(rows)
	return SendLagTrend{Seconds: seconds, IsKnown: isKnown}
}

func sendLagGrowth(rows []engine.RequestRow) (float64, bool) {
	var ordered []engine.RequestRow
	for _, row := range rows {
		if aggregate.IsSendLagKnown(row) {
			ordered = append(ordered, row)
		}
	}
	if len(ordered) < SendLagGrowthMinRecords {
		return 0, false
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].SentAt.Before(ordered[j].SentAt) })
	third := len(ordered) / 3
	median := func(part []engine.RequestRow) float64 {
		lags := make([]float64, len(part))
		for i, row := range part {
			lags[i], _ = aggregate.SendLag(row)
		}
		return stats.Percentile(lags, 0.5)
	}
	return median(ordered[len(ordered)-third:]) - median(ordered[:third]), true
}

// lowResolutionReasons applies the combined stage length rule:
// stage >= max(10 x I, 60s, N / RPS, 10 x mean
// E2E), with N the p95 sample count of Rule A, because the SLOs and the
// capacity curves are p95. The mean E2E is the stage's own, since the warmup's
// is not in the records. Warnings that servtuner validate recorded for the stage
// add their reasons.
func lowResolutionReasons(in StageInput) []string {
	interval := in.Interval.Duration
	requiredRequests := aggregate.RuleFor(spec.PercentileNameP95).MinSamples
	terms := []struct {
		name     string
		required time.Duration
	}{
		{fmt.Sprintf("10 x the %s scrape interval", interval), ResolutionIntervals * interval},
		{"the 60s floor", ResolutionMinStage},
		{fmt.Sprintf("%d requests at %.2f RPS", requiredRequests, in.Stage.OfferedRPS), requestsDuration(requiredRequests, in.Stage.OfferedRPS)},
		{"10 x the mean E2E", time.Duration(ResolutionE2EMultiple * in.Aggregate.MeanE2ESeconds * float64(time.Second))},
	}
	var reasons []string
	binding := terms[0]
	for _, term := range terms[1:] {
		if term.required > binding.required {
			binding = term
		}
	}
	if in.Stage.Duration() < binding.required {
		source := string(in.Interval.Source) + " interval"
		if in.Interval.Job != "" {
			source += " of job " + in.Interval.Job
		}
		reasons = append(reasons, fmt.Sprintf("stage lasts %s, shorter than %s required by %s (%s)",
			in.Stage.Duration().Round(time.Second), binding.required.Round(time.Second), binding.name, source))
	}
	for _, warning := range in.Warnings {
		switch warning.Rule {
		case spec.StageLengthRuleMinStageDuration, spec.StageLengthRuleRequestDurationRatio:
			reasons = append(reasons, "recorded at run time: "+warning.Message)
		}
	}
	return reasons
}

func requestsDuration(requests int, rps float64) time.Duration {
	if rps <= 0 {
		return 0
	}
	return time.Duration(float64(requests) / rps * float64(time.Second))
}

// insufficientSamplesMessage lists the percentiles that Rule A withheld, and
// the Rule A warnings recorded at run time. It returns "" when none.
func insufficientSamplesMessage(aggregated spec.StageAggregate, warnings []spec.StageLengthWarning) string {
	var parts []string
	for _, metric := range []struct {
		name    string
		summary *spec.LatencySummary
	}{{"ttft", aggregated.Ttft}, {"itl", aggregated.Itl}, {"tpot", aggregated.Tpot}, {"e2e", aggregated.E2E}} {
		if metric.summary == nil {
			continue
		}
		var names []string
		for _, entry := range []struct {
			name     spec.PercentileName
			interval spec.BootstrapInterval
		}{
			{spec.PercentileNameP50, metric.summary.P50Seconds}, {spec.PercentileNameP90, metric.summary.P90Seconds},
			{spec.PercentileNameP95, metric.summary.P95Seconds}, {spec.PercentileNameP99, metric.summary.P99Seconds},
		} {
			if entry.interval.InsufficientSamplesReason != nil {
				names = append(names, string(entry.name))
			}
		}
		if len(names) > 0 {
			parts = append(parts, fmt.Sprintf("%s %s (%d samples)", metric.name, strings.Join(names, ", "), samplesOf(metric.summary)))
		}
	}
	for _, warning := range warnings {
		if warning.Rule == spec.StageLengthRulePercentileSampleCount {
			parts = append(parts, "recorded at run time: "+warning.Message)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "too few samples for " + strings.Join(parts, "; ")
}

func samplesOf(summary *spec.LatencySummary) int { return summary.P50Seconds.Samples }

// IsTargetSaturated reports whether a stage was excluded because the target
// fell behind (target_saturated). Such a stage is not a measurement of the
// target at its offered rate, but it is evidence that the target could not
// sustain it, which the capacity model uses as a failing stage.
func IsTargetSaturated(stage spec.StageQuality) bool {
	for _, issue := range stage.Issues {
		if issue.Code == spec.QualityIssueCodeTargetSaturated {
			return true
		}
	}
	return false
}

func (in StageInput) peakInFlight() int {
	if in.PeakInFlight != nil {
		return *in.PeakInFlight
	}
	return PeakInFlight(in.Rows)
}

// PeakInFlight is the most requests in flight at once among rows: each holds
// a slot from sent_at for its end-to-end time, failed ones included, because a
// request that errored held its slot until it did.
func PeakInFlight(rows []engine.RequestRow) int {
	type event struct {
		at    time.Time
		delta int
	}
	events := make([]event, 0, 2*len(rows))
	for _, row := range rows {
		if row.SentAt.IsZero() || math.IsNaN(row.E2ESeconds) || math.IsInf(row.E2ESeconds, 0) || row.E2ESeconds < 0 {
			continue
		}
		end := row.SentAt.Add(time.Duration(row.E2ESeconds * float64(time.Second)))
		events = append(events, event{row.SentAt, 1}, event{end, -1})
	}
	// Ends sort before starts at the same instant, so back-to-back requests
	// on one slot do not count twice.
	sort.Slice(events, func(i, j int) bool {
		if events[i].at.Equal(events[j].at) {
			return events[i].delta < events[j].delta
		}
		return events[i].at.Before(events[j].at)
	})
	current, peak := 0, 0
	for _, e := range events {
		current += e.delta
		peak = max(peak, current)
	}
	return peak
}

// targetStall reports a target that stopped producing output while it still
// held requests: through the second half of the stage, its output-token counter
// did not move although requests were running. That is an engine fault, not
// load: it says nothing about where capacity ends. Without both vLLM series
// there is no evidence either way.
func targetStall(in StageInput) (string, error) {
	tokens, hasTokens, err := in.Series.Signal(input.GenerationTokens)
	if err != nil || !hasTokens {
		return "", err
	}
	running, hasRunning, err := in.Series.Signal(input.RequestsRunning)
	if err != nil || !hasRunning {
		return "", err
	}
	start := in.Stage.Start.Add(in.Stage.Duration() / 2)
	produced, ok := aggregate.CounterIncrease(tokens, start, in.Stage.End)
	if !ok || produced > 0 {
		return "", nil
	}
	held, ok := aggregate.StatsInWindow(running, start, in.Stage.End)
	if !ok || held.Max < 1 {
		return "", nil
	}
	return fmt.Sprintf("the target produced no output tokens through the second half of the stage while %.0f requests were running: "+
		"its engine stalled, so the stage measures a fault, not capacity", held.Max), nil
}

// IsTargetStalled reports whether a stage saw the target stall (target_stalled).
func IsTargetStalled(stage spec.StageQuality) bool {
	for _, issue := range stage.Issues {
		if issue.Code == spec.QualityIssueCodeTargetStalled {
			return true
		}
	}
	return false
}
