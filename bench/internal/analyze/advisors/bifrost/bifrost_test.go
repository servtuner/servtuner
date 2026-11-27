package bifrost

import (
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

func router() spec.RecommendationTarget {
	return spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeBifrost, Namespace: "llm"}
}

func upstream() *spec.RecommendationTarget {
	index := 0
	return &spec.RecommendationTarget{Role: spec.WorkloadRoleUpstream, UpstreamIndex: &index, TargetType: spec.TargetTypeVllm, Namespace: "gpu"}
}

// calm is a two-stage run where nothing is wrong: flat upstream, flat
// overhead, no retries, idle concurrency, CPU and memory well inside limits.
func calm() Input {
	stage := func(index int, rps float64) StageView {
		return StageView{Index: index, RPS: rps, ClientE2EMean: 2, UpstreamMean: 1.9, UpstreamP95: 2.2, OverheadMean: 0.001, OverheadP95: 0.002,
			HasUpstream: true, HasOverhead: true, HasRetries: true, HasFallbacks: true, ActiveFraction: 0.1, HasActive: true}
	}
	stages := []StageView{stage(0, 20), stage(1, 50)}
	return Input{
		Router: router(), Upstream: upstream(), Stages: stages, Operating: stages[1],
		Container:  Container{CPUP95: 0.2, CPULimitCores: 1, MemoryPeak: 100, MemoryLimitBytes: 1000},
		Confidence: spec.RecommendationConfidenceMedium,
	}
}

func rules(recommendations []spec.Recommendation) []string {
	var names []string
	for _, recommendation := range recommendations {
		names = append(names, recommendation.Rule)
	}
	return names
}

func TestACalmRunGetsOnlyItsLatencyBudget(t *testing.T) {
	got := Advise(calm(), nil)
	if names := rules(got); len(names) != 1 || names[0] != RuleLatencyBudget {
		t.Fatalf("advice on a calm run: %v", names)
	}
	// 2s = 1.9s upstream (95%) + 0.001s overhead + 0.099s rest.
	if text := got[0].Evidence[1].Text; !strings.Contains(text, "upstream 1.9s (95.0%)") || !strings.Contains(text, "pre-upstream 0.099s (5.0%)") {
		t.Errorf("budget = %s", text)
	}
}

func queueRecommendations() []spec.Recommendation {
	recommendation := func(rule, knob string) spec.Recommendation {
		return spec.Recommendation{Id: rule + "/target", Rule: rule, Target: router(), Subject: spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindKnob, Name: knob}, Recommended: 43.0}
	}
	return []spec.Recommendation{recommendation(queueingConcurrencyRule, target.KnobBifrostConcurrency), recommendation(queueingQueueRule, target.KnobBifrostBufferSize)}
}

func TestB2AlwaysNamesTheQueueAndB1NeedsTheConcurrencyPattern(t *testing.T) {
	existing := queueRecommendations()
	Advise(calm(), existing)
	if existing[0].Rule != queueingConcurrencyRule || existing[1].Rule != RuleQueueSizing || existing[1].Id != "B2/target" {
		t.Errorf("calm run: rules %v", rules(existing))
	}

	bound := calm()
	bound.Stages[0].OverheadP95, bound.Stages[1].OverheadP95 = 0.002, 0.004 // overhead doubles
	bound.Stages[1].ActiveFraction = 0.95                                   // at the knob
	bound.Operating = bound.Stages[1]
	existing = queueRecommendations()
	Advise(bound, existing)
	if existing[0].Rule != RuleConcurrencyBound || !strings.Contains(existing[0].Evidence[len(existing[0].Evidence)-1].Text, "reached 95%") {
		t.Errorf("concurrency-bound run: %+v", existing[0])
	}
}

func TestB1IsNotFiredWhenTheUpstreamSlowsToo(t *testing.T) {
	in := calm()
	in.Stages[1].OverheadP95, in.Stages[1].ActiveFraction = 0.004, 0.95
	in.Stages[1].UpstreamMean = 3 // the upstream slowed 1.6×: the queue is upstream's
	in.Operating = in.Stages[1]
	existing := queueRecommendations()
	Advise(in, existing)
	if existing[0].Rule == RuleConcurrencyBound {
		t.Error("B1 fired while the upstream slowed")
	}
}

func TestB3PointsAtTheUpstream(t *testing.T) {
	in := calm()
	in.Stages[1].UpstreamMean, in.Stages[1].ClientE2EMean = 4, 4.1
	in.Operating = in.Stages[1]
	got := Advise(in, nil)[:1] // X1 comes last
	if got[0].Rule != RuleUpstreamBound || got[0].Target.Role != spec.WorkloadRoleUpstream {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].Evidence[0].Text, "rose 2.10526×") || got[0].Recommended == nil {
		t.Errorf("B3 = %+v", got[0])
	}
	in.Stages[1].OverheadP95 = 0.5 // overhead now 12% of E2E: Bifrost is part of the problem
	in.Operating = in.Stages[1]
	if got := Advise(in, nil); len(withoutBudget(got)) != 0 {
		t.Errorf("B3 with a large overhead: %v", rules(got))
	}
}

func TestB4RetriesOrFallbacks(t *testing.T) {
	for name, change := range map[string]func(*StageView){
		"retries":   func(s *StageView) { s.RetriesPerAttempt = 0.2 },
		"fallbacks": func(s *StageView) { s.FallbackShare = 0.05 },
	} {
		in := calm()
		change(&in.Stages[1])
		in.Operating = in.Stages[1]
		if got := withoutBudget(Advise(in, nil)); len(got) != 1 || got[0] != RuleRetryPressure {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestB6ThrottlingOrCPUNearTheLimit(t *testing.T) {
	for name, container := range map[string]Container{
		"throttled":       {CPUP95: 0.2, CPULimitCores: 1, Throttle: 0.08, ThrottleStage: 1},
		"near the limit":  {CPUP95: 0.85, CPULimitCores: 1},
		"no limit is not": {CPUP95: 3},
	} {
		in := calm()
		in.Container = container
		got := withoutBudget(Advise(in, nil))
		want := name != "no limit is not"
		if (len(got) == 1 && got[0] == RuleCPUBound) != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestB7MemoryNearTheLimit(t *testing.T) {
	in := calm()
	in.Container.MemoryPeak = 900
	if got := withoutBudget(Advise(in, nil)); len(got) != 1 || got[0] != RuleMemory {
		t.Errorf("got %v", got)
	}
}

func TestB8AndB9ReadTheCapturedConfig(t *testing.T) {
	yes, no := true, false
	in := calm()
	in.Config = Config{EnableLogging: &yes, DisableContentLogging: &no, Providers: []ProviderConfig{{Name: "vllm", SendBackRawResponse: &yes}}}
	got := Advise(in, nil)
	if names := withoutBudget(got); len(names) != 2 || names[0] != RuleLogging || names[1] != RulePerfHostile {
		t.Fatalf("rules = %v", names)
	}
	if got[0].Confidence != spec.RecommendationConfidenceLow || !strings.Contains(got[1].Evidence[0].Text, "vllm.send_back_raw_response") {
		t.Errorf("B8 %+v, B9 %+v", got[0], got[1])
	}
	in.Config.DisableContentLogging = &yes
	in.Config.Providers = nil
	if got := Advise(in, nil); len(withoutBudget(got)) != 0 {
		t.Errorf("content logging off: %v", rules(got))
	}
}

func TestParseConfigReadsTheRedactedDocuments(t *testing.T) {
	config, err := ParseConfig(map[string][]byte{
		target.BifrostConfigDocument:    []byte(`{"client_config": {"enable_logging": true, "disable_content_logging": false, "drop_excess_requests": false}}`),
		target.BifrostProvidersDocument: []byte(`{"providers": [{"name": "servtuner-mock", "concurrency_and_buffer_size": {"concurrency": 1000, "buffer_size": 5000}, "network_config": {"max_retries": 0}}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !*config.EnableLogging || len(config.Providers) != 1 || *config.Providers[0].Concurrency != 1000 || *config.Providers[0].MaxRetries != 0 {
		t.Errorf("config = %+v", config)
	}
	if _, err := ParseConfig(map[string][]byte{target.BifrostConfigDocument: []byte(`{`)}); err == nil {
		t.Error("broken JSON accepted")
	}
}

func TestThePatchTargetsTheProvidersTheTrafficUsed(t *testing.T) {
	config := Config{Providers: []ProviderConfig{{Name: "servtuner-mock"}, {Name: "other"}}}
	runSpec := spec.RunSpec{Target: spec.Target{Upstreams: []spec.Upstream{{Name: "servtuner-mock"}}}}
	patch, err := RenderPatch(map[string]float64{target.KnobBifrostConcurrency: 43, target.KnobBifrostBufferSize: 10}, config, runSpec, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	for _, want := range []string{"# servtuner Bifrost patch: run run-1", "servtuner-mock:", "concurrency: 43", "buffer_size: 10"} {
		if !strings.Contains(text, want) {
			t.Errorf("patch lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "other") {
		t.Errorf("a provider the run did not use: %s", text)
	}
	if patch, _ := RenderPatch(nil, config, runSpec, "run-1"); patch != nil {
		t.Error("a patch with nothing to change")
	}
}

func TestViewStageReadsTheRoutersHistograms(t *testing.T) {
	start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	stage := input.Stage{Index: 1, Start: start, End: start.Add(time.Minute)}
	series := func(name string, labels map[string]string, before, after float64) input.Series {
		all := map[string]string{"__name__": name, "pod": "bifrost-0"}
		for key, value := range labels {
			all[key] = value
		}
		return input.Series{Labels: all, Samples: []input.Sample{{At: start, Value: before}, {At: start.Add(30 * time.Second), Value: after}}}
	}
	matrix := input.MapMatrix{
		upstreamLatency: {series("bifrost_upstream_latency_seconds_sum", nil, 0, 150), series("bifrost_upstream_latency_seconds_count", nil, 0, 100),
			series("bifrost_upstream_latency_seconds_bucket", map[string]string{"le": "2"}, 0, 100), series("bifrost_upstream_latency_seconds_bucket", map[string]string{"le": "+Inf"}, 0, 100)},
		overheadLatency: {series("bifrost_overhead_latency_microseconds_sum", nil, 0, 50000), series("bifrost_overhead_latency_microseconds_count", nil, 0, 100),
			series("bifrost_overhead_latency_microseconds_bucket", map[string]string{"le": "1000"}, 0, 100), series("bifrost_overhead_latency_microseconds_bucket", map[string]string{"le": "+Inf"}, 0, 100)},
		requestRetries: {series("bifrost_request_retries_sum", nil, 0, 20), series("bifrost_request_retries_count", nil, 0, 100)},
		upstreamRequests: {series("bifrost_upstream_requests_total", map[string]string{"fallback_index": "0"}, 0, 98),
			series("bifrost_upstream_requests_total", map[string]string{"fallback_index": "1"}, 0, 2)},
	}
	active := 0.4
	mean := 1.5
	view, err := ViewStage(matrix, map[string]bool{"bifrost-0": true}, stage, spec.StageAggregate{AchievedRps: 50, E2E: &spec.LatencySummary{MeanSeconds: &mean}}, &active)
	if err != nil {
		t.Fatal(err)
	}
	if view.UpstreamMean != 1.5 || view.OverheadMean != 0.0005 || view.RetriesPerAttempt != 0.2 || view.FallbackShare != 0.02 || view.ActiveFraction != 0.4 {
		t.Errorf("view = %+v", view)
	}
	if !view.HasUpstream || !view.HasOverhead || view.UpstreamP95 != 1.9 {
		t.Errorf("view = %+v", view)
	}
}

// withoutBudget drops X1, which every run with Bifrost histograms gets, so a
// test sees the rules it is about.
func withoutBudget(recommendations []spec.Recommendation) []string {
	var names []string
	for _, name := range rules(recommendations) {
		if name != RuleLatencyBudget {
			names = append(names, name)
		}
	}
	return names
}

func TestTheBudgetWithoutAnOverheadHistogram(t *testing.T) {
	in := calm()
	for i := range in.Stages {
		in.Stages[i].HasOverhead = false
	}
	in.Operating = in.Stages[1]
	got := Advise(in, nil)
	budget := got[len(got)-1]
	if budget.Rule != RuleLatencyBudget || !strings.Contains(budget.Evidence[0].Text, "network, pre-upstream and Bifrost overhead 0.1s (5.0%)") {
		t.Fatalf("budget = %+v", budget)
	}
	if !strings.Contains(strings.Join(budget.Caveats, " "), "cannot be judged") {
		t.Errorf("caveats = %v", budget.Caveats)
	}
}

func TestB2IsUrgentWhenBifrostDropped(t *testing.T) {
	in := calm()
	in.Stages[1].Dropped, in.Stages[1].HasDropped = 12, true
	existing := queueRecommendations()
	Advise(in, existing)
	queue := existing[1]
	if queue.IsUrgent == nil || !*queue.IsUrgent || !strings.Contains(queue.Evidence[len(queue.Evidence)-1].Text, "dropped 12 requests") {
		t.Errorf("B2 = %+v", queue)
	}
	existing = queueRecommendations()
	Advise(calm(), existing)
	if existing[1].IsUrgent != nil {
		t.Error("a run without drops made B2 urgent")
	}
}

func TestDropsNeedTheErrorTypeLabel(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	counter := func(labels map[string]string, from, to float64) input.Series {
		return input.Series{Labels: labels, Samples: []input.Sample{{At: start, Value: from}, {At: start.Add(30 * time.Second), Value: to}}}
	}
	end := start.Add(time.Minute)
	if _, ok := dropped([]input.Series{counter(map[string]string{"pod": "a"}, 0, 9)}, start, end); ok {
		t.Error("unlabelled errors (v1.5) were read as drops or as none")
	}
	count, ok := dropped([]input.Series{
		counter(map[string]string{"error_type": "bifrost_dropped"}, 4, 10),
		counter(map[string]string{"error_type": "provider_error"}, 0, 50),
	}, start, end)
	if !ok || count != 6 {
		t.Errorf("dropped = %v, %v; want 6", count, ok)
	}
}
