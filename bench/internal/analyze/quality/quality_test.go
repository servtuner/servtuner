package quality

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/signals"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

var testStart = analyzetest.StartTime

func defaultValidity() Validity {
	return Validity{MinAchievedRPSRatio: 0.97, MaxSendLagP99: 50 * time.Millisecond}
}

// stageInput builds a 60s stage at 10 RPS whose records have the given count
// and a constant send lag.
func stageInput(count int, lag float64, series input.SignalSource) StageInput {
	stage := input.Stage{Index: 0, OfferedRPS: 10, Start: testStart, End: testStart.Add(60 * time.Second)}
	rows := make([]engine.RequestRow, count)
	for i := range rows {
		ttft := 0.2
		scheduled, sendLag := testStart.Add(time.Duration(i)*60*time.Second/time.Duration(count)), lag
		rows[i] = engine.RequestRow{
			ScheduledAt: &scheduled, SendLagSeconds: &sendLag,
			TTFTSeconds: &ttft, E2ESeconds: 1, Status: 200,
		}
	}
	if series == nil {
		series = input.NoSignals{}
	}
	return StageInput{
		Stage: stage, Rows: rows, Validity: defaultValidity(), Series: series,
		Interval:  aggregate.ScrapeInterval{Duration: 5 * time.Second, Source: spec.ScrapeIntervalSourceDiscovered, Job: "targets"},
		Aggregate: aggregate.AggregateStage(aggregate.StageInput{RunID: "run", Stage: stage, Rows: rows}),
	}
}

func codes(issues []spec.QualityIssue) []spec.QualityIssueCode {
	var result []spec.QualityIssueCode
	for _, issue := range issues {
		result = append(result, issue.Code)
	}
	return result
}

func hasCode(issues []spec.QualityIssue, code spec.QualityIssueCode) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func mustCheckStage(t *testing.T, in StageInput) StageOutcome {
	t.Helper()
	outcome, err := CheckStage(in)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func TestStageValidityAchievedRPSBoundary(t *testing.T) {
	// 600 requests in 60s at 10 RPS offered is exactly 100%; 582 is exactly 97%.
	for _, test := range []struct {
		count   int
		isValid bool
	}{{600, true}, {582, true}, {581, false}} {
		outcome := mustCheckStage(t, stageInput(test.count, 0, nil))
		if outcome.Quality.IsValid != test.isValid {
			t.Errorf("%d requests: is_valid = %v, want %v (%v)", test.count, outcome.Quality.IsValid, test.isValid, codes(outcome.Quality.Issues))
		}
		if hasCode(outcome.Quality.Issues, spec.QualityIssueCodeAchievedRpsLow) == test.isValid {
			t.Errorf("%d requests: achieved_rps_low presence is wrong", test.count)
		}
	}
}

func TestStageValiditySendLagCeilingIsStrict(t *testing.T) {
	if outcome := mustCheckStage(t, stageInput(600, 0.0499, nil)); !outcome.Quality.IsValid {
		t.Errorf("49.9ms is below the ceiling: %v", codes(outcome.Quality.Issues))
	}
	outcome := mustCheckStage(t, stageInput(600, 0.050, nil))
	if outcome.Quality.IsValid || !hasCode(outcome.Quality.Issues, spec.QualityIssueCodeSendLagHigh) {
		t.Errorf("p99 send lag of exactly 50ms is not < 50ms: %+v", outcome.Quality)
	}
	if outcome.Quality.IsClientBound {
		t.Error("a constant high send lag is not client_bound by itself")
	}
}

func TestStageValidityUsesConfiguredThresholds(t *testing.T) {
	in := stageInput(570, 0.020, nil) // 95% achieved, 20ms lag
	if mustCheckStage(t, in).Quality.IsValid {
		t.Fatal("95% is below the default 97%")
	}
	in.Validity = Validity{MinAchievedRPSRatio: 0.9, MaxSendLagP99: 10 * time.Millisecond}
	outcome := mustCheckStage(t, in)
	if hasCode(outcome.Quality.Issues, spec.QualityIssueCodeAchievedRpsLow) || !hasCode(outcome.Quality.Issues, spec.QualityIssueCodeSendLagHigh) {
		t.Errorf("the stricter lag ceiling and looser rate ratio must apply: %v", codes(outcome.Quality.Issues))
	}
}

func TestStageWithNoRequestsIsInvalid(t *testing.T) {
	outcome := mustCheckStage(t, stageInput(0, 0, nil))
	if outcome.Quality.IsValid || !outcome.Quality.IsExcluded {
		t.Errorf("a stage with no requests cannot be valid: %+v", outcome.Quality)
	}
}

func TestClientBoundFromSendLagGrowth(t *testing.T) {
	in := stageInput(600, 0, nil)
	for i := range in.Rows {
		setSendLag(&in.Rows[i], 0.0001*float64(i)/10) // 0 to 6ms: below the growth limit
	}
	if mustCheckStage(t, in).Quality.IsClientBound {
		t.Error("6ms of growth is below the 10ms limit")
	}
	for i := range in.Rows {
		setSendLag(&in.Rows[i], 0.00004*float64(i)) // 0 to 24ms; p99 stays under 50ms
	}
	in.Aggregate = aggregate.AggregateStage(aggregate.StageInput{RunID: "run", Stage: in.Stage, Rows: in.Rows})
	outcome := mustCheckStage(t, in)
	if !outcome.Quality.IsClientBound || !outcome.Quality.IsExcluded || outcome.Quality.IsValid {
		t.Errorf("growing send lag must make the stage client_bound: %+v", outcome.Quality)
	}
	if hasCode(outcome.Quality.Issues, spec.QualityIssueCodeSendLagHigh) {
		t.Error("send_lag_high must not fire below the 50ms ceiling")
	}
}

func TestClientBoundNeedsEnoughRecordsToJudgeGrowth(t *testing.T) {
	in := stageInput(SendLagGrowthMinRecords-1, 0, nil)
	for i := range in.Rows {
		setSendLag(&in.Rows[i], 0.001*float64(i))
	}
	if _, ok := sendLagGrowth(in.Rows); ok {
		t.Error("too few records must not give a growth figure")
	}
}

func benchSeries(cpu, throttle float64) input.MapSignals {
	return input.MapSignals{
		input.BenchCPUUsage:    {{At: testStart.Add(10 * time.Second), Value: cpu}, {At: testStart.Add(30 * time.Second), Value: cpu}},
		input.BenchCPUThrottle: {{At: testStart.Add(10 * time.Second), Value: throttle}, {At: testStart.Add(30 * time.Second), Value: throttle}},
	}
}

func TestClientBoundFromBenchCPU(t *testing.T) {
	for _, test := range []struct {
		name            string
		cpu, throttle   float64
		wantClientBound bool
	}{
		{"idle", 0.5, 0, false},
		{"just below the CPU limit", 0.899, 0, false},
		{"at 90% of the limit", 0.90, 0, true},
		{"throttled", 0.5, 0.05, true},
		{"barely throttled", 0.5, 0.049, false},
	} {
		outcome := mustCheckStage(t, stageInput(600, 0, benchSeries(test.cpu, test.throttle)))
		if outcome.Quality.IsClientBound != test.wantClientBound {
			t.Errorf("%s: is_client_bound = %v", test.name, outcome.Quality.IsClientBound)
		}
		if !outcome.HasBenchSeries {
			t.Errorf("%s: the bench series were present", test.name)
		}
	}
}

func TestClientBoundIgnoresTheTrimmedFirstInterval(t *testing.T) {
	series := input.MapSignals{input.BenchCPUUsage: {
		{At: testStart.Add(2 * time.Second), Value: 1}, // inside the first 5s interval: trimmed
		{At: testStart.Add(20 * time.Second), Value: 0.4},
	}}
	if mustCheckStage(t, stageInput(600, 0, series)).Quality.IsClientBound {
		t.Error("samples from the first scrape interval still hold the previous stage's load")
	}
}

func TestBenchSeriesCountEvenWhenTheStageIsTooShortToJudge(t *testing.T) {
	// A 1m scrape interval trims the whole 60s stage, so CPU can't be judged; but the series were
	// collected, which is low resolution, not a missing catalog.
	series := input.MapSignals{input.BenchCPUUsage: {{At: testStart.Add(20 * time.Second), Value: 0.4}}}
	in := stageInput(600, 0, series)
	in.Interval.Duration = time.Minute
	outcome := mustCheckStage(t, in)
	if !outcome.HasBenchSeries {
		t.Error("collected bench series reported as missing when the stage is shorter than the interval")
	}
	if outcome.Quality.IsClientBound {
		t.Error("client_bound judged from a window trimmed to nothing")
	}
}

func TestClientBoundWithoutSeriesRestsOnRecords(t *testing.T) {
	outcome := mustCheckStage(t, stageInput(600, 0, nil))
	if outcome.HasBenchSeries || outcome.Quality.IsClientBound {
		t.Errorf("no series and no lag growth: %+v", outcome)
	}
}

func TestLowResolutionTerms(t *testing.T) {
	// A 60s stage at 10 RPS, 5s interval, 1s E2E: needs max(50s, 60s, 20s, 10s) = 60s.
	if mustCheckStage(t, stageInput(600, 0, nil)).IsLowResolution {
		t.Error("a 60s stage satisfies the rule at a 5s interval")
	}
	in := stageInput(600, 0, nil)
	in.Interval.Duration = 10 * time.Second // needs 100s
	outcome := mustCheckStage(t, in)
	if !outcome.IsLowResolution || !hasCode(outcome.Quality.Issues, spec.QualityIssueCodeLowResolution) {
		t.Fatalf("a 60s stage is too short at a 10s interval: %+v", outcome.Quality)
	}
	if outcome.Quality.IsExcluded {
		t.Error("low resolution marks server-side statistics only; the stage stays usable")
	}
	var message string
	for _, issue := range outcome.Quality.Issues {
		if issue.Code == spec.QualityIssueCodeLowResolution {
			message = issue.Message
		}
	}
	if !strings.Contains(message, "job targets") || !strings.Contains(message, "discovered") {
		t.Errorf("the issue must name the limiting job and where the interval came from: %q", message)
	}
}

func TestLowResolutionFromRequestDurationAndRequestCount(t *testing.T) {
	long := stageInput(600, 0, nil)
	for i := range long.Rows {
		long.Rows[i].E2ESeconds = 8 // 10 x 8s = 80s > 60s
	}
	long.Aggregate = aggregate.AggregateStage(aggregate.StageInput{RunID: "run", Stage: long.Stage, Rows: long.Rows})
	if !mustCheckStage(t, long).IsLowResolution {
		t.Error("a stage shorter than 10 x its mean E2E is low resolution (Rule B)")
	}
	slow := stageInput(60, 0, nil)
	slow.Stage.OfferedRPS = 1 // 200 requests at 1 RPS take 200s
	if !mustCheckStage(t, slow).IsLowResolution {
		t.Error("a stage too short to hold the p95 sample count at its rate is low resolution (Rule A)")
	}
}

func TestRuntimeWarningsAreCarriedIntoTheIssues(t *testing.T) {
	in := stageInput(600, 0, nil)
	in.Warnings = []spec.StageLengthWarning{
		{Rule: spec.StageLengthRuleRequestDurationRatio, Message: "stage shorter than 10 x E2E"},
		{Rule: spec.StageLengthRulePercentileSampleCount, Message: "p99 needs 1000 requests"},
	}
	outcome := mustCheckStage(t, in)
	var joined []string
	for _, issue := range outcome.Quality.Issues {
		joined = append(joined, issue.Message)
	}
	text := strings.Join(joined, "\n")
	if !strings.Contains(text, "stage shorter than 10 x E2E") || !strings.Contains(text, "p99 needs 1000 requests") {
		t.Errorf("recorded warnings missing from %q", text)
	}
	if !outcome.IsLowResolution {
		t.Error("a recorded duration warning marks the stage low resolution")
	}
}

func TestInsufficientSamplesNamesThePercentiles(t *testing.T) {
	outcome := mustCheckStage(t, stageInput(600, 0, nil))
	if !hasCode(outcome.Quality.Issues, spec.QualityIssueCodeInsufficientSamples) {
		t.Fatalf("600 requests cannot support p99: %v", codes(outcome.Quality.Issues))
	}
	for _, issue := range outcome.Quality.Issues {
		if issue.Code == spec.QualityIssueCodeInsufficientSamples && !strings.Contains(issue.Message, "p99") {
			t.Errorf("message should name p99: %q", issue.Message)
		}
	}
	if !outcome.Quality.IsValid {
		t.Error("Rule A withholds a percentile; it does not invalidate the stage")
	}
	enough := mustCheckStage(t, stageInput(1200, 0, nil))
	if hasCode(enough.Quality.Issues, spec.QualityIssueCodeInsufficientSamples) {
		t.Error("1200 requests support every percentile")
	}
}

func TestCheckKnobs(t *testing.T) {
	declared := spec.RunSpecKnobs{
		"vllm.max_num_seqs": 256.0, "vllm.dtype": "bf16", "vllm.enable_prefix_caching": true,
		"bifrost.buffer_size": 5000.0, "vllm.gpu_memory_utilization": 0.9,
	}
	captured := map[string]spec.KnobValue{
		"vllm.max_num_seqs": 256.0, "vllm.dtype": "fp16", "vllm.enable_prefix_caching": true, "vllm.gpu_memory_utilization": 0.9,
	}
	status := map[string]spec.KnobStatus{}
	for _, check := range CheckKnobs(declared, captured) {
		status[check.Name] = check.Status
	}
	want := map[string]spec.KnobStatus{
		"vllm.max_num_seqs": spec.KnobStatusVerified, "vllm.dtype": spec.KnobStatusMismatch,
		"vllm.enable_prefix_caching": spec.KnobStatusVerified, "bifrost.buffer_size": spec.KnobStatusUnverified,
		"vllm.gpu_memory_utilization": spec.KnobStatusVerified,
	}
	for name, wantStatus := range want {
		if status[name] != wantStatus {
			t.Errorf("%s = %s, want %s", name, status[name], wantStatus)
		}
	}
	checks := CheckKnobs(declared, captured)
	for i := 1; i < len(checks); i++ {
		if checks[i-1].Name > checks[i].Name {
			t.Error("knob checks must be sorted by name")
		}
	}
	for _, check := range checks {
		if check.Status == spec.KnobStatusUnverified && check.Actual != nil {
			t.Error("an unverified knob has no actual value")
		}
	}
}

func TestDetectDriftFindsEachKindOfChange(t *testing.T) {
	pre := analyzetest.Snapshot(spec.SnapshotPhasePre)
	if drift := DetectDrift(pre, analyzetest.Clone(pre)); len(drift.Changes) != 0 || len(drift.OOMKills) != 0 {
		t.Fatalf("identical snapshots must not drift: %+v", drift)
	}
	if drift := DetectDrift(nil, pre); len(drift.Changes) != 0 {
		t.Error("a missing snapshot means nothing to compare")
	}
	mutations := map[string]func(*spec.TargetSnapshot){
		"image": func(s *spec.TargetSnapshot) { s.Workloads[0].Pods[0].Containers[0].Image = "registry.invalid/other:1" },
		"digest": func(s *spec.TargetSnapshot) {
			d := "sha256:" + strings.Repeat("a", 64)
			s.Workloads[0].Pods[0].Containers[0].ImageDigest = &d
		},
		"args": func(s *spec.TargetSnapshot) {
			s.Workloads[0].Pods[0].Containers[0].Args = []string{"--max-num-seqs", "64"}
		},
		"env": func(s *spec.TargetSnapshot) { s.Workloads[0].Pods[0].Containers[0].Env = nil },
		"resources": func(s *spec.TargetSnapshot) {
			s.Workloads[0].Pods[0].Containers[0].Limits = spec.ResourceQuantities{"memory": "64Gi"}
		},
		"replicas": func(s *spec.TargetSnapshot) { s.Workloads[0].Replicas = &spec.Replicas{Desired: 3, Ready: 3} },
		"pod":      func(s *spec.TargetSnapshot) { s.Workloads[0].Pods[0].Name = "vllm-1" },
		"workload": func(s *spec.TargetSnapshot) { s.Workloads = nil },
	}
	for name, mutate := range mutations {
		post := analyzetest.Clone(pre)
		mutate(post)
		if drift := DetectDrift(pre, post); len(drift.Changes) == 0 {
			t.Errorf("%s change was not detected", name)
		}
	}
}

func TestDetectDriftDoesNotSeeARedactedValue(t *testing.T) {
	pre := analyzetest.Snapshot(spec.SnapshotPhasePre)
	post := analyzetest.Clone(pre)
	// The redacted HF_TOKEN carries no value, so two snapshots agree on it.
	if drift := DetectDrift(pre, post); len(drift.Changes) != 0 {
		t.Errorf("unexpected drift %+v", drift)
	}
}

func TestDetectDriftOOMKill(t *testing.T) {
	pre := analyzetest.Snapshot(spec.SnapshotPhasePre)
	post := analyzetest.Clone(pre)
	restarts, reason := 1, "OOMKilled"
	post.Workloads[0].Pods[0].Containers[0].RestartCount = &restarts
	post.Workloads[0].Pods[0].Containers[0].LastTerminationReason = &reason
	drift := DetectDrift(pre, post)
	if len(drift.OOMKills) != 1 || len(drift.Changes) != 1 {
		t.Errorf("an OOM restart is a change and a finding: %+v", drift)
	}
	reason = "Error"
	if drift := DetectDrift(pre, post); len(drift.OOMKills) != 0 || len(drift.Changes) != 1 {
		t.Errorf("a non-OOM restart is only a change: %+v", drift)
	}
}

func TestChangeTimeIsTheFirstDeviation(t *testing.T) {
	series := input.MapSignals{
		input.ReadyPods: {{At: testStart, Value: 2}, {At: testStart.Add(5 * time.Second), Value: 2}, {At: testStart.Add(20 * time.Second), Value: 1}},
		input.Restarts:  {{At: testStart, Value: 0}, {At: testStart.Add(10 * time.Second), Value: 1}},
	}
	at, ok, err := ChangeTime(series)
	if err != nil || !ok || !at.Equal(testStart.Add(10*time.Second)) {
		t.Errorf("change time = %v, %v, %v; want the earlier of the two series", at, ok, err)
	}
	if _, ok, _ := ChangeTime(input.NoSignals{}); ok {
		t.Error("no series gives no change time")
	}
	flat := input.MapSignals{input.ReadyPods: {{At: testStart, Value: 2}, {At: testStart.Add(time.Minute), Value: 2}}}
	if _, ok, _ := ChangeTime(flat); ok {
		t.Error("a flat series has no change")
	}
}

func TestCheckCoverage(t *testing.T) {
	window := spec.TimeWindow{Start: spec.NewUTCTimestamp(testStart), End: spec.NewUTCTimestamp(testStart.Add(time.Minute))}
	query := func(catalog spec.CatalogName, name string, result spec.QueryResult) spec.Query {
		return spec.Query{Catalog: catalog, Name: spec.MetricName(name), Query: "q", StepSeconds: 5, Window: window, Result: result}
	}
	gappy := []input.Sample{{At: testStart, Value: 1}, {At: testStart.Add(10 * time.Second), Value: 1}, {At: testStart.Add(21 * time.Second), Value: 1}}
	whole := []input.Sample{{At: testStart, Value: 1}, {At: testStart.Add(10 * time.Second), Value: 1}}
	series := input.MapMatrix{
		{Catalog: spec.CatalogNameVllm, Name: "gappy"}:       {{Samples: gappy}},
		{Catalog: spec.CatalogNameKubernetes, Name: "whole"}: {{Samples: whole}},
	}
	detail := "no exporter"
	queries := &spec.PrometheusQueries{
		Queries: []spec.Query{
			query(spec.CatalogNameVllm, "gappy", spec.QueryResultCollected), query(spec.CatalogNameKubernetes, "whole", spec.QueryResultCollected),
			query(spec.CatalogNameKubernetes, "empty", spec.QueryResultEmpty), query(spec.CatalogNameKubernetes, "missing_file", spec.QueryResultCollected),
		},
		OptionalCatalogs: []spec.OptionalCatalogResult{{Name: spec.CatalogNameDcgm, Result: spec.CatalogResultAbsent, Detail: &detail}},
	}
	coverage, issues, err := CheckCoverage(queries, series)
	if err != nil {
		t.Fatal(err)
	}
	// gappy (a 11s hop at a 5s step), the empty query and the missing file have gaps: 3 of 4.
	if coverage.SeriesWithGapsPercent != 75 {
		t.Errorf("series_with_gaps_percent = %v, want 75", coverage.SeriesWithGapsPercent)
	}
	collected := map[spec.CatalogName]bool{}
	for _, catalog := range coverage.Catalogs {
		collected[catalog.Catalog] = catalog.IsCollected
	}
	if !collected[spec.CatalogNameVllm] || !collected[spec.CatalogNameKubernetes] || collected[spec.CatalogNameDcgm] || len(collected) != 3 {
		t.Errorf("catalogs = %v", collected)
	}
	if !hasCode(issues, spec.QualityIssueCodeCatalogMissing) || !hasCode(issues, spec.QualityIssueCodeSeriesGaps) {
		t.Errorf("issues = %v", codes(issues))
	}
	if coverage, issues, _ := CheckCoverage(nil, series); coverage != nil || issues != nil {
		t.Error("without queries.json there is no coverage to judge")
	}
}

func TestCoverageBelowTheGapThresholdRaisesNoIssue(t *testing.T) {
	window := spec.TimeWindow{Start: spec.NewUTCTimestamp(testStart), End: spec.NewUTCTimestamp(testStart.Add(time.Minute))}
	series := input.MapMatrix{}
	queries := &spec.PrometheusQueries{OptionalCatalogs: []spec.OptionalCatalogResult{}}
	for i := range 20 {
		name := string(rune('a' + i))
		queries.Queries = append(queries.Queries, spec.Query{Catalog: spec.CatalogNameVllm, Name: spec.MetricName(name), Query: "q", StepSeconds: 5, Window: window, Result: spec.QueryResultCollected})
		samples := []input.Sample{{At: testStart, Value: 1}, {At: testStart.Add(5 * time.Second), Value: 1}}
		if i == 0 {
			samples[1].At = testStart.Add(30 * time.Second) // 1 of 20 series has a gap: 5%
		}
		series[input.SeriesRef{Catalog: spec.CatalogNameVllm, Name: name}] = []input.Series{{Samples: samples}}
	}
	coverage, issues, err := CheckCoverage(queries, series)
	if err != nil || coverage.SeriesWithGapsPercent != 5 || hasCode(issues, spec.QualityIssueCodeSeriesGaps) {
		t.Errorf("5%% gaps: %v %v %v", coverage, codes(issues), err)
	}
}

// A series the catalog declares may_be_empty (no CPU limit, no HPA) is a state
// of the cluster when it comes back empty, not missing data. Failing is still a gap.
func TestAnEmptyMayBeEmptySeriesIsNotAGap(t *testing.T) {
	window := spec.TimeWindow{Start: spec.NewUTCTimestamp(testStart), End: spec.NewUTCTimestamp(testStart.Add(time.Minute))}
	yes, no := true, false
	query := func(name string, result spec.QueryResult, mayBeEmpty *bool) spec.Query {
		detail := "boom"
		recorded := spec.Query{Catalog: spec.CatalogNameKubernetes, Name: spec.MetricName(name), Query: "q", StepSeconds: 5, Window: window, Result: result, MayBeEmpty: mayBeEmpty}
		if result == spec.QueryResultError {
			recorded.Detail = &detail
		}
		return recorded
	}
	queries := &spec.PrometheusQueries{OptionalCatalogs: []spec.OptionalCatalogResult{}, Queries: []spec.Query{
		query("cpu_limit_cores", spec.QueryResultEmpty, &yes),
		query("hpa_desired_max", spec.QueryResultError, &yes),
		query("cpu_cores", spec.QueryResultEmpty, &no),
		query("memory_working_set_bytes", spec.QueryResultEmpty, nil),
	}}
	coverage, _, err := CheckCoverage(queries, input.MapMatrix{})
	if err != nil || coverage.SeriesWithGapsPercent != 75 {
		t.Errorf("only the empty may_be_empty series is not a gap: %v, %v", coverage, err)
	}
}

func TestGrade(t *testing.T) {
	good := spec.StageQuality{StageIndex: 0, IsValid: true}
	excluded := spec.StageQuality{StageIndex: 1, IsExcluded: true}
	noted := spec.StageQuality{StageIndex: 2, IsValid: true, Issues: []spec.QualityIssue{{Code: spec.QualityIssueCodeInsufficientSamples, Message: "m"}}}
	low := spec.StageQuality{StageIndex: 3, IsValid: true, Issues: []spec.QualityIssue{{Code: spec.QualityIssueCodeLowResolution, Message: "m"}}}
	for _, test := range []struct {
		name    string
		quality spec.RunQuality
		want    spec.QualityGrade
	}{
		{"clean", spec.RunQuality{Stages: []spec.StageQuality{good}}, spec.QualityGradeGood},
		{"only withheld percentiles", spec.RunQuality{Stages: []spec.StageQuality{good, noted}}, spec.QualityGradeGood},
		{"an excluded stage", spec.RunQuality{Stages: []spec.StageQuality{good, excluded}}, spec.QualityGradeDegraded},
		{"a stage issue", spec.RunQuality{Stages: []spec.StageQuality{low}}, spec.QualityGradeDegraded},
		{"a run issue", spec.RunQuality{Stages: []spec.StageQuality{good}, Issues: []spec.QualityIssue{{Code: spec.QualityIssueCodeCatalogMissing, Message: "m"}}}, spec.QualityGradeDegraded},
		{"everything excluded", spec.RunQuality{Stages: []spec.StageQuality{excluded}}, spec.QualityGradeUnusable},
		{"no stages", spec.RunQuality{}, spec.QualityGradeUnusable},
	} {
		if got := Grade(test.quality); got != test.want {
			t.Errorf("%s: grade = %s, want %s", test.name, got, test.want)
		}
	}
}

func TestCheckExcludesStagesAfterTheChangeAndOnlyThose(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("config-drift-restart")
	run, err := scenario.Run()
	if err != nil {
		t.Fatal(err)
	}
	stages, err := input.StagesFromPhases(run.Phases)
	if err != nil {
		t.Fatal(err)
	}
	derived := signals.New(signals.Config{Matrix: run.Matrix, BenchPod: run.BenchPod, TargetPods: signals.PodsOf(run.PreSnapshot, run.PostSnapshot)})
	var inputs []StageInput
	for _, stage := range stages {
		rows := scenarioRows(scenario, stage.Index)
		inputs = append(inputs, StageInput{
			Stage: stage, Rows: rows, Validity: defaultValidity(), Series: derived,
			Interval:  aggregate.ScrapeInterval{Duration: 5 * time.Second, Source: spec.ScrapeIntervalSourceDiscovered},
			Aggregate: aggregate.AggregateStage(aggregate.StageInput{RunID: "run", Stage: stage, Rows: rows}),
		})
	}
	result, err := Check(RunInput{Stages: inputs, Pre: run.PreSnapshot, Post: run.PostSnapshot, Series: derived, Matrix: run.Matrix})
	if err != nil {
		t.Fatal(err)
	}
	var excluded []int
	for _, stage := range result.Quality.Stages {
		if stage.IsExcluded {
			excluded = append(excluded, stage.StageIndex)
			if !hasCode(stage.Issues, spec.QualityIssueCodeConfigDrift) {
				t.Errorf("stage %d is excluded without a config_drift issue", stage.StageIndex)
			}
		}
	}
	if len(excluded) != 2 || excluded[0] != 2 || excluded[1] != 3 {
		t.Errorf("excluded stages = %v, want [2 3]", excluded)
	}
}

func scenarioRows(scenario analyzetest.Scenario, stage int) []engine.RequestRow {
	var rows []engine.RequestRow
	for _, row := range scenario.Records() {
		if row.StageIndex != nil && int(*row.StageIndex) == stage {
			rows = append(rows, row)
		}
	}
	return rows
}

func TestRequiredRequestsRelaxesOnlyForRandomArrivals(t *testing.T) {
	fixed := Validity{MinAchievedRPSRatio: 0.97}
	random := Validity{MinAchievedRPSRatio: 0.97, IsRandomArrivals: true}
	if got := requiredRequests(300, fixed); math.Abs(got-291) > 1e-9 {
		t.Errorf("deterministic floor = %v, want 291", got)
	}
	if got, want := requiredRequests(300, random), 300-3*math.Sqrt(300); math.Abs(got-want) > 1e-9 {
		t.Errorf("poisson floor = %v, want %v", got, want)
	}
	// Large stages: three deviations are tighter than 3%, so 97% still rules.
	if got := requiredRequests(100000, random); math.Abs(got-97000) > 1e-9 {
		t.Errorf("poisson floor at 100000 = %v, want 97000", got)
	}
}

func TestNoKnownSendLagIsNotedAndNotJudged(t *testing.T) {
	in := stageInput(600, 0, nil)
	for i := range in.Rows {
		in.Rows[i].ScheduledAt, in.Rows[i].SendLagSeconds = nil, nil
	}
	in.Aggregate = aggregate.AggregateStage(aggregate.StageInput{RunID: "run", Stage: in.Stage, Rows: in.Rows})
	outcome := mustCheckStage(t, in)
	if !outcome.Quality.IsValid || hasCode(outcome.Quality.Issues, spec.QualityIssueCodeSendLagHigh) || !hasCode(outcome.Quality.Issues, spec.QualityIssueCodeInsufficientSamples) {
		t.Errorf("outcome = %+v", outcome.Quality)
	}
	if _, ok := sendLagGrowth(in.Rows); ok {
		t.Error("rows with no known lag give no growth figure")
	}
}

func setSendLag(row *engine.RequestRow, lag float64) { row.SendLagSeconds = &lag }

func growingLagStage() StageInput {
	in := stageInput(600, 0, nil)
	for i := range in.Rows {
		setSendLag(&in.Rows[i], 0.00004*float64(i)) // grows 24ms across the stage
	}
	in.Aggregate = aggregate.AggregateStage(aggregate.StageInput{RunID: "run", Stage: in.Stage, Rows: in.Rows})
	return in
}

func TestLagGrowthAtTheInFlightCapIsTheTargetFallingBehind(t *testing.T) {
	in := growingLagStage()
	peak := 1200
	in.PeakInFlight, in.MaxInFlight = &peak, 1200
	outcome := mustCheckStage(t, in)
	if outcome.Quality.IsClientBound || hasCode(outcome.Quality.Issues, spec.QualityIssueCodeClientBound) {
		t.Errorf("the client only waited for the target: %+v", outcome.Quality.Issues)
	}
	if !hasCode(outcome.Quality.Issues, spec.QualityIssueCodeTargetSaturated) || !outcome.Quality.IsExcluded || outcome.Quality.IsValid {
		t.Errorf("a saturated stage is excluded and says why: %+v", outcome.Quality)
	}
	if !IsTargetSaturated(outcome.Quality) {
		t.Error("IsTargetSaturated must see the issue")
	}
}

func TestLagGrowthBelowTheCapStaysClientBound(t *testing.T) {
	in := growingLagStage()
	peak := 1100 // under 95% of 1200
	in.PeakInFlight, in.MaxInFlight = &peak, 1200
	if outcome := mustCheckStage(t, in); !outcome.Quality.IsClientBound || IsTargetSaturated(outcome.Quality) {
		t.Errorf("below the cap the lag is the client's: %+v", outcome.Quality.Issues)
	}
	in.MaxInFlight = 0 // no cap recorded: nothing to compare with
	if outcome := mustCheckStage(t, in); !outcome.Quality.IsClientBound {
		t.Errorf("without a recorded cap the verdict is unchanged: %+v", outcome.Quality.Issues)
	}
}

func TestBenchCPUEvidenceKeepsClientBoundEvenAtTheCap(t *testing.T) {
	in := growingLagStage()
	in.Series = benchSeries(0.95, 0)
	peak := 1200
	in.PeakInFlight, in.MaxInFlight = &peak, 1200
	outcome := mustCheckStage(t, in)
	if !outcome.Quality.IsClientBound || IsTargetSaturated(outcome.Quality) {
		t.Errorf("a client at its CPU limit is client_bound whatever the in-flight count: %+v", outcome.Quality.Issues)
	}
}

func TestPeakInFlight(t *testing.T) {
	at := func(seconds float64) time.Time { return testStart.Add(time.Duration(seconds * float64(time.Second))) }
	rows := []engine.RequestRow{
		{SentAt: at(0), E2ESeconds: 2},       // 0-2
		{SentAt: at(1), E2ESeconds: 2},       // 1-3: overlaps the first
		{SentAt: at(2), E2ESeconds: 1},       // 2-3: starts as the first ends
		{SentAt: at(10), E2ESeconds: 1},      // alone
		{SentAt: time.Time{}, E2ESeconds: 5}, // never sent: no slot
	}
	if got := PeakInFlight(rows); got != 2 {
		t.Errorf("peak = %d, want 2 (a request ending as another starts frees its slot first)", got)
	}
	if PeakInFlight(nil) != 0 {
		t.Error("no rows, no requests in flight")
	}
}

// vllmSeries samples every 5s across a 60s stage: running requests constant,
// the output-token counter rising until stallAt seconds, then flat.
func vllmSeries(running float64, stallAt int) input.MapSignals {
	var tokens, held []input.Sample
	counter := 0.0
	for second := 0; second <= 60; second += 5 {
		if second <= stallAt {
			counter = float64(second) * 100
		}
		at := testStart.Add(time.Duration(second) * time.Second)
		tokens = append(tokens, input.Sample{At: at, Value: counter})
		held = append(held, input.Sample{At: at, Value: running})
	}
	return input.MapSignals{input.GenerationTokens: tokens, input.RequestsRunning: held}
}

func TestAStalledEngineIsTargetStalledNotSaturated(t *testing.T) {
	in := growingLagStage()
	in.Series = vllmSeries(17, 20) // no output after 20s of 60
	peak := 1200
	in.PeakInFlight, in.MaxInFlight = &peak, 1200
	outcome := mustCheckStage(t, in)
	if !hasCode(outcome.Quality.Issues, spec.QualityIssueCodeTargetStalled) || IsTargetSaturated(outcome.Quality) {
		t.Fatalf("a stall is the cause, not saturation: %+v", outcome.Quality.Issues)
	}
	if outcome.Quality.IsValid || !outcome.Quality.IsExcluded || !IsTargetStalled(outcome.Quality) {
		t.Errorf("a stalled stage is excluded: %+v", outcome.Quality)
	}
}

func TestNoStallWhileOutputFlowsOrWithoutEvidence(t *testing.T) {
	for name, series := range map[string]input.SignalSource{
		"output flows to the end":       vllmSeries(17, 60),
		"stalls only in the first half": vllmSeries(0, 20), // nothing running afterwards: idle, not stalled
		"no vLLM series":                input.NoSignals{},
	} {
		in := stageInput(600, 0, series)
		if outcome := mustCheckStage(t, in); IsTargetStalled(outcome.Quality) {
			t.Errorf("%s: not a stall: %+v", name, outcome.Quality.Issues)
		}
	}
}

func TestAStallExcludesTheRunFromComparison(t *testing.T) {
	healthy := stageInput(600, 0, vllmSeries(5, 60))
	stalled := stageInput(600, 0, vllmSeries(17, 20))
	stalled.Stage.Index = 1
	result, err := Check(RunInput{Stages: []StageInput{healthy, stalled}, Series: input.NoSignals{}, Matrix: input.NoMatrix{}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Quality.IsExcludedFromComparison || !hasCode(result.Quality.Issues, spec.QualityIssueCodeTargetStalled) {
		t.Errorf("a run whose target stalled must not compete in an experiment: %+v", result.Quality.Issues)
	}
}

func TestStreamBatchedFlagsAStreamIntervalAboveOne(t *testing.T) {
	batched := map[string]spec.KnobValue{target.KnobVLLMStreamInterval: 10.0}
	issue, flagged := streamBatchedIssue(nil, batched)
	if !flagged || issue.Code != spec.QualityIssueCodeStreamBatched || !strings.Contains(issue.Message, "10 tokens per chunk") {
		t.Fatalf("issue = %+v, flagged %v", issue, flagged)
	}
	if _, flagged := streamBatchedIssue(nil, map[string]spec.KnobValue{target.KnobVLLMStreamInterval: 1.0}); flagged {
		t.Error("one token per chunk measures ITL correctly")
	}
	if _, flagged := streamBatchedIssue(nil, nil); flagged {
		t.Error("an unknown interval is not evidence")
	}
	declared := spec.RunSpecKnobs{target.KnobVLLMStreamInterval: 4.0}
	if _, flagged := streamBatchedIssue(declared, nil); !flagged {
		t.Error("the declared value stands in when nothing was captured")
	}
	if _, flagged := streamBatchedIssue(declared, map[string]spec.KnobValue{target.KnobVLLMStreamInterval: 1.0}); flagged {
		t.Error("the captured value wins over the declared one")
	}
}
