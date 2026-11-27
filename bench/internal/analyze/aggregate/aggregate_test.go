package aggregate

import (
	"math"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var testStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func sequence(count int) []float64 {
	values := make([]float64, count)
	for i := range values {
		values[i] = float64(i + 1)
	}
	return values
}

func TestSummarizeWithholdsPercentilesBelowRuleA(t *testing.T) {
	// 150 samples: enough for p50 and p90 (100), not for p95 (200) or p99 (1000).
	summary := Summarize("run", "label", sequence(150))
	if summary.P50Seconds.Value == nil || summary.P90Seconds.Value == nil {
		t.Fatal("p50 and p90 should have values at 150 samples")
	}
	for name, interval := range map[string]spec.BootstrapInterval{"p95": summary.P95Seconds, "p99": summary.P99Seconds} {
		if interval.Value != nil || interval.CiLow != nil || interval.CiHigh != nil {
			t.Errorf("%s must carry no number below its Rule A count: %+v", name, interval)
		}
		if interval.InsufficientSamplesReason == nil || interval.Samples != 150 {
			t.Errorf("%s needs a reason and the sample count: %+v", name, interval)
		}
	}
}

func TestSummarizeRuleAThresholds(t *testing.T) {
	for _, test := range []struct {
		samples     int
		wantPresent map[spec.PercentileName]bool
	}{
		{99, map[spec.PercentileName]bool{"p50": false, "p90": false, "p95": false, "p99": false}},
		{100, map[spec.PercentileName]bool{"p50": true, "p90": true, "p95": false, "p99": false}},
		{199, map[spec.PercentileName]bool{"p50": true, "p90": true, "p95": false, "p99": false}},
		{200, map[spec.PercentileName]bool{"p50": true, "p90": true, "p95": true, "p99": false}},
		{999, map[spec.PercentileName]bool{"p50": true, "p90": true, "p95": true, "p99": false}},
		{1000, map[spec.PercentileName]bool{"p50": true, "p90": true, "p95": true, "p99": true}},
	} {
		summary := Summarize("run", "label", sequence(test.samples))
		got := map[spec.PercentileName]bool{
			"p50": summary.P50Seconds.Value != nil, "p90": summary.P90Seconds.Value != nil,
			"p95": summary.P95Seconds.Value != nil, "p99": summary.P99Seconds.Value != nil,
		}
		for name, want := range test.wantPresent {
			if got[name] != want {
				t.Errorf("%d samples: %s present = %v, want %v", test.samples, name, got[name], want)
			}
		}
	}
}

func TestSummarizeIntervalsContainTheValueAndAreDeterministic(t *testing.T) {
	values := make([]float64, 1500)
	generator := splitMix64{state: 7}
	for i := range values {
		values[i] = float64(generator.next()>>11) / (1 << 53)
	}
	first := Summarize("run-a", "stage-0/ttft", values)
	again := Summarize("run-a", "stage-0/ttft", values)
	if *first.P95Seconds.CiLow != *again.P95Seconds.CiLow || *first.P95Seconds.CiHigh != *again.P95Seconds.CiHigh {
		t.Error("the same run id and label must give the same interval")
	}
	other := Summarize("run-b", "stage-0/ttft", values)
	if *first.P50Seconds.CiLow == *other.P50Seconds.CiLow && *first.P50Seconds.CiHigh == *other.P50Seconds.CiHigh &&
		*first.P95Seconds.CiLow == *other.P95Seconds.CiLow && *first.P99Seconds.CiHigh == *other.P99Seconds.CiHigh {
		t.Error("a different run id should seed a different resampling")
	}
	for name, interval := range map[string]spec.BootstrapInterval{
		"p50": first.P50Seconds, "p90": first.P90Seconds, "p95": first.P95Seconds, "p99": first.P99Seconds,
	} {
		if *interval.CiLow > *interval.Value || *interval.Value > *interval.CiHigh {
			t.Errorf("%s: ci_low <= value <= ci_high violated: %+v", name, interval)
		}
		if interval.Samples != len(values) {
			t.Errorf("%s samples = %d, want %d", name, interval.Samples, len(values))
		}
	}
}

func TestBootstrapIntervalNarrowsWithMoreSamples(t *testing.T) {
	width := func(count int) float64 {
		values := make([]float64, count)
		for i := range values {
			values[i] = float64((i*7919)%1000) / 1000
		}
		interval := Summarize("run", "label", values).P50Seconds
		return *interval.CiHigh - *interval.CiLow
	}
	if small, large := width(200), width(5000); large >= small {
		t.Errorf("interval at 5000 samples (%v) should be narrower than at 200 (%v)", large, small)
	}
}

func TestBootstrapCoversTheTrueMedianOfAKnownDistribution(t *testing.T) {
	// A uniform distribution on [0, 1) has median 0.5. Across many independent
	// samples a 95% interval should contain it most of the time.
	covered := 0
	const trials = 40
	for trial := range trials {
		values := make([]float64, 400)
		generator := splitMix64{state: uint64(trial) + 1}
		for i := range values {
			values[i] = float64(generator.next()>>11) / (1 << 53)
		}
		interval := Summarize("run", "trial", values).P50Seconds
		if *interval.CiLow <= 0.5 && 0.5 <= *interval.CiHigh {
			covered++
		}
	}
	if covered < trials*8/10 {
		t.Errorf("the interval covered the true median in %d of %d trials", covered, trials)
	}
}

func TestBootstrapOfLargeSampleIsThinnedButContainsTheValue(t *testing.T) {
	values := make([]float64, BootstrapMaxPoints*3)
	for i := range values {
		values[i] = float64(i)
	}
	interval := Summarize("run", "itl", values).P99Seconds
	if interval.Samples != len(values) || *interval.CiLow > *interval.Value || *interval.Value > *interval.CiHigh {
		t.Errorf("unexpected interval %+v", interval)
	}
}

func TestSummarizeEmptyIsNil(t *testing.T) {
	if Summarize("run", "label", nil) != nil {
		t.Error("no values should give no summary")
	}
}

func TestSplitMixIsFixed(t *testing.T) {
	// The bootstrap stream must never change, or old bundles would analyse
	// differently: pin the first outputs of a known state.
	generator := splitMix64{state: 0}
	if got := generator.next(); got != 0xe220a8397b1dcdaf {
		t.Errorf("first splitmix64 output from state 0 = %#x", got)
	}
	if bound := generator.below(10); bound < 0 || bound >= 10 {
		t.Errorf("below(10) = %d", bound)
	}
}

func row(ttft, e2e float64, status int64) engine.RequestRow {
	start := testStart
	return engine.RequestRow{ScheduledAt: &start, SentAt: testStart, TTFTSeconds: &ttft, E2ESeconds: e2e, Status: status}
}

func TestAggregateStageCountsErrorsByClassAndAchievedRate(t *testing.T) {
	class := "timeout"
	rows := []engine.RequestRow{row(0.1, 1, 200), row(0.1, 1, 200), row(0.1, 1, 503), {Status: 0, Error: &class}, {Status: 200, Error: &class}}
	stage := input.Stage{Index: 3, OfferedRPS: 1, Start: testStart, End: testStart.Add(10 * time.Second)}
	result := AggregateStage(StageInput{RunID: "run", Stage: stage, Rows: rows})
	got := result.Aggregate
	if got.StageIndex != 3 || got.Requests != 5 || got.Errors != 3 || got.AchievedRps != 0.5 {
		t.Errorf("aggregate = %+v", got)
	}
	want := map[string]int{"http_503": 1, "timeout": 2}
	if len(got.ErrorsByClass) != 2 {
		t.Fatalf("errors by class = %+v", got.ErrorsByClass)
	}
	for _, entry := range got.ErrorsByClass {
		if want[entry.ErrorClass] != entry.Count {
			t.Errorf("class %s count %d, want %d", entry.ErrorClass, entry.Count, want[entry.ErrorClass])
		}
	}
	if got.ErrorsByClass[0].ErrorClass > got.ErrorsByClass[1].ErrorClass {
		t.Error("error classes must be sorted")
	}
	if got.E2E.P50Seconds.Samples != 2 {
		t.Errorf("latency statistics must use successful requests only, got %d samples", got.E2E.P50Seconds.Samples)
	}
}

func TestAggregateStageDerivesTPOTAndTokenRates(t *testing.T) {
	ttft, input1, output := 0.5, int64(100), int64(11)
	rows := []engine.RequestRow{{TTFTSeconds: &ttft, E2ESeconds: 1.5, InputTokens: &input1, OutputTokens: &output, Status: 200, ITLSeconds: []float64{0.1, 0.1, 0.3}}}
	stage := input.Stage{OfferedRPS: 1, Start: testStart, End: testStart.Add(2 * time.Second)}
	got := AggregateStage(StageInput{RunID: "run", Stage: stage, Rows: rows}).Aggregate
	// (e2e - ttft) / (output tokens - 1) = 1.0 / 10.
	if got.Tpot == nil || math.Abs(*got.Tpot.MeanSeconds-0.1) > 1e-12 {
		t.Errorf("tpot = %+v", got.Tpot)
	}
	if got.Itl == nil || got.Itl.P50Seconds.Samples != 3 || *got.Itl.MaxSeconds != 0.3 {
		t.Errorf("itl = %+v", got.Itl)
	}
	if *got.InputTokensPerSecond != 50 || *got.OutputTokensPerSecond != 5.5 || *got.TotalTokensPerSecond != 55.5 {
		t.Errorf("token rates = %v %v %v", *got.InputTokensPerSecond, *got.OutputTokensPerSecond, *got.TotalTokensPerSecond)
	}
}

func TestAggregateStageGoodputAndSLOResults(t *testing.T) {
	ttftSLO, e2eSLO, errorSLO := 0.3, 2.0, 0.5
	slos := SLOThresholds{TTFTP95Seconds: &ttftSLO, E2EP95Seconds: &e2eSLO, MaxErrorRate: &errorSLO}
	rows := []engine.RequestRow{row(0.1, 1, 200), row(0.5, 1, 200), row(0.1, 3, 200), row(0, 0, 500)}
	stage := input.Stage{OfferedRPS: 2, Start: testStart, End: testStart.Add(2 * time.Second)}
	got := AggregateStage(StageInput{RunID: "run", Stage: stage, Rows: rows, SLOs: slos}).Aggregate
	// Only the first request is within every per-request threshold.
	if *got.GoodputRps != 0.5 {
		t.Errorf("goodput = %v, want 0.5", *got.GoodputRps)
	}
	if len(got.SloResults) != 3 {
		t.Fatalf("slo results = %+v", got.SloResults)
	}
	for _, result := range got.SloResults[:2] {
		if result.Observed != nil || result.IsMet {
			t.Errorf("3 samples cannot show a p95 SLO: %+v", result)
		}
	}
	if errorResult := got.SloResults[2]; errorResult.Slo != spec.SloNameMaxErrorRate || *errorResult.Observed != 0.25 || !errorResult.IsMet {
		t.Errorf("error rate result = %+v", errorResult)
	}
}

func TestSLOThresholdsFromSpec(t *testing.T) {
	ttft := spec.Duration("250ms")
	got, err := SLOThresholdsFromSpec(&spec.Slos{TtftP95: &ttft})
	if err != nil || *got.TTFTP95Seconds != 0.25 || got.E2EP95Seconds != nil {
		t.Errorf("thresholds = %+v, %v", got, err)
	}
	bad := spec.Duration("soon")
	if _, err := SLOThresholdsFromSpec(&spec.Slos{E2EP95: &bad}); err == nil {
		t.Error("an unparseable duration must be an error")
	}
}

func TestResolveInterval(t *testing.T) {
	got, err := ResolveInterval(nil, nil, true)
	if err != nil || got.Duration != 5*time.Second || got.Source != spec.ScrapeIntervalSourceAssumed || got.Job != "" {
		t.Errorf("without targets.json the interval is assumed: %+v, %v", got, err)
	}
	targets := &spec.PrometheusTargets{Targets: []spec.ScrapedJob{
		{Job: "targets", Role: spec.JobRoleTarget, ScrapeIntervalSeconds: 5},
		{Job: "cadvisor", Role: spec.JobRoleCadvisor, ScrapeIntervalSeconds: 15},
		{Job: "ksm", Role: spec.JobRoleKubeStateMetrics, ScrapeIntervalSeconds: 60},
		{Job: "dcgm", Role: spec.JobRoleDcgm, ScrapeIntervalSeconds: 30},
	}}
	got, err = ResolveInterval(targets, nil, true)
	if err != nil || got.Duration != 15*time.Second || got.Job != "cadvisor" || got.Source != spec.ScrapeIntervalSourceDiscovered {
		t.Errorf("kube-state-metrics and an unrequested DCGM must not count: %+v, %v", got, err)
	}
	queries := &spec.PrometheusQueries{Queries: []spec.Query{{Catalog: spec.CatalogNameDcgm, Name: "gpu_util_percent"}}}
	if got, _ := ResolveInterval(targets, queries, true); got.Duration != 30*time.Second || got.Job != "dcgm" {
		t.Errorf("a queried DCGM catalog counts: %+v", got)
	}
}

func TestTrimWindow(t *testing.T) {
	stage := input.Stage{Start: testStart, End: testStart.Add(60 * time.Second)}
	start, end, ok := TrimWindow(stage, Gauge, 5*time.Second, 8*time.Second)
	if !ok || !start.Equal(testStart.Add(5*time.Second)) || !end.Equal(stage.End) {
		t.Errorf("gauge trim drops the first interval only: %v %v %v", start, end, ok)
	}
	start, end, ok = TrimWindow(stage, Derived, 5*time.Second, 8*time.Second)
	if !ok || !start.Equal(testStart.Add(8*time.Second)) || !end.Equal(testStart.Add(52*time.Second)) {
		t.Errorf("derived trim drops one mean E2E from each edge: %v %v %v", start, end, ok)
	}
	if _, _, ok := TrimWindow(stage, Derived, 5*time.Second, 30*time.Second); ok {
		t.Error("a window trimmed to nothing is not usable")
	}
}

func samples(offsets []int, values []float64) []input.Sample {
	result := make([]input.Sample, len(offsets))
	for i := range offsets {
		result[i] = input.Sample{At: testStart.Add(time.Duration(offsets[i]) * time.Second), Value: values[i]}
	}
	return result
}

func TestStatsInWindowIsHalfOpen(t *testing.T) {
	series := samples([]int{0, 5, 10, 15}, []float64{9, 1, 0, 7})
	stats, ok := StatsInWindow(series, testStart.Add(5*time.Second), testStart.Add(15*time.Second))
	if !ok || stats.Count != 2 || stats.Max != 1 || stats.PositiveFraction != 0.5 {
		t.Errorf("the sample at the end belongs to the next stage: %+v", stats)
	}
	if _, ok := StatsInWindow(series, testStart.Add(20*time.Second), testStart.Add(30*time.Second)); ok {
		t.Error("an empty window has no statistics")
	}
}

func TestCounterIncreaseHandlesBaselineAndReset(t *testing.T) {
	series := samples([]int{0, 5, 10, 15, 20, 25}, []float64{100, 110, 130, 4, 9, 9})
	// Baseline at 5s (110): +20, then a reset to 4 counts from zero, +5, +0.
	got, ok := CounterIncrease(series, testStart.Add(6*time.Second), testStart.Add(30*time.Second))
	if !ok || got != 29 {
		t.Errorf("increase = %v, %v; want 29", got, ok)
	}
	if _, ok := CounterIncrease(series[:1], testStart, testStart.Add(time.Hour)); ok {
		t.Error("one point is not an increase")
	}
}

func TestSendLagIsUnknownForNullZeroTimeAndNaN(t *testing.T) {
	scheduled, lag, nan := testStart, 0.002, math.NaN()
	var zero time.Time
	cases := map[string]struct {
		row   engine.RequestRow
		known bool
	}{
		"measured":         {engine.RequestRow{ScheduledAt: &scheduled, SendLagSeconds: &lag}, true},
		"null both":        {engine.RequestRow{}, false},
		"null lag":         {engine.RequestRow{ScheduledAt: &scheduled}, false},
		"null schedule":    {engine.RequestRow{SendLagSeconds: &lag}, false},
		"legacy zero time": {engine.RequestRow{ScheduledAt: &zero, SendLagSeconds: &lag}, false},
		"legacy NaN lag":   {engine.RequestRow{ScheduledAt: &scheduled, SendLagSeconds: &nan}, false},
	}
	for name, c := range cases {
		if got := IsSendLagKnown(c.row); got != c.known {
			t.Errorf("%s: known = %v, want %v", name, got, c.known)
		}
	}
}

func TestWarmupRowsAreRecognised(t *testing.T) {
	if !IsWarmupRow(engine.RequestRow{Phase: engine.RowPhaseWarmup}) || IsWarmupRow(engine.RequestRow{Phase: engine.RowPhaseStage}) || IsWarmupRow(engine.RequestRow{}) {
		t.Error("only phase warmup is a warmup row; a row without a phase column is a stage row")
	}
}

// A zero time.Time written to Parquet as nanoseconds reads back as 1754-08-30,
// so IsZero() would miss it; the marker must still be unknown after a round trip.
func TestZeroScheduleTimeIsUnknownAfterAParquetRoundTrip(t *testing.T) {
	var zero time.Time
	lag := 0.0
	sink := bundle.NewRequestSink()
	if err := sink.Write(engine.RequestRow{ScheduledAt: &zero, SentAt: testStart, SendLagSeconds: &lag, E2ESeconds: 1, Status: 200}); err != nil {
		t.Fatal(err)
	}
	content, err := sink.Finish()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := bundle.ReadRequests(content)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, err = %v", rows, err)
	}
	if rows[0].ScheduledAt == nil || rows[0].ScheduledAt.IsZero() {
		t.Fatalf("expected the round trip to turn the zero time into a non-zero one, got %v", rows[0].ScheduledAt)
	}
	if IsSendLagKnown(rows[0]) {
		t.Errorf("a pre-epoch schedule (%v) must be unknown", rows[0].ScheduledAt)
	}
}
