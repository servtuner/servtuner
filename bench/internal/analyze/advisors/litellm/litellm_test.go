package litellm

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func proxy() spec.RecommendationTarget {
	return spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeLitellm}
}

// calm is a healthy proxy: a flat queue, small overhead, one attempt per
// request, no errors and spare CPU.
func calm() Input {
	stages := []StageView{
		{Index: 0, RPS: 50, ClientE2EMean: 0.5, QueueP95: 0.01, HasQueue: true, OverheadMean: 0.01, HasOverhead: true, UpstreamMean: 0.45, HasUpstream: true, AttemptsPerRequest: 1, HasAttempts: true},
		{Index: 1, RPS: 100, ClientE2EMean: 0.52, QueueP95: 0.011, HasQueue: true, OverheadMean: 0.012, HasOverhead: true, UpstreamMean: 0.46, HasUpstream: true, AttemptsPerRequest: 1, HasAttempts: true},
	}
	return Input{
		Proxy: proxy(), Stages: stages, Operating: stages[1], Workers: 1, IsWorkersCaptured: true,
		Container:  Container{CPUP95: 0.3, CPULimitCores: 1},
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

func TestACalmProxyGetsOnlyItsLatencyBudget(t *testing.T) {
	got := Advise(calm())
	if !slices.Equal(rules(got), []string{RuleLatencyBudget}) {
		t.Fatalf("rules = %v", rules(got))
	}
	text := got[0].Evidence[1].Text
	if !strings.Contains(text, "upstream 0.46s (88.5%)") || !strings.Contains(text, "LiteLLM overhead 0.012s") {
		t.Errorf("budget = %q", text)
	}
}

func TestWorkerBoundNeedsARisingQueueAndBusyWorkers(t *testing.T) {
	in := calm()
	in.Stages[1].QueueP95 = 0.05 // 5× the first stage
	in.Container.CPUP95 = 0.95
	got := Advise(in)
	if !slices.Contains(rules(got), RuleWorkerBound) {
		t.Fatalf("rules = %v", rules(got))
	}
	l1 := got[slices.Index(rules(got), RuleWorkerBound)]
	if !strings.Contains(l1.Recommended.(string), "--num_workers (env NUM_WORKERS) above 1") {
		t.Errorf("directive = %v", l1.Recommended)
	}

	in.Container.CPUP95 = 0.95
	in.Workers = 2 // under half a core per worker: not saturated
	if slices.Contains(rules(Advise(in)), RuleWorkerBound) {
		t.Error("L1 with idle workers")
	}

	in = calm()
	in.Container.CPUP95 = 0.95 // busy but the queue is flat
	if slices.Contains(rules(Advise(in)), RuleWorkerBound) {
		t.Error("L1 without a rising queue")
	}
}

func TestAnUncapturedWorkerCountIsSaidToBeAssumed(t *testing.T) {
	in := calm()
	in.Stages[1].QueueP95 = 0.05
	in.Container.CPUP95 = 0.95
	in.IsWorkersCaptured = false
	got := Advise(in)
	l1 := got[slices.Index(rules(got), RuleWorkerBound)]
	if !slices.ContainsFunc(l1.Caveats, func(caveat string) bool { return strings.Contains(caveat, "default of 1 is assumed") }) {
		t.Errorf("caveats = %v", l1.Caveats)
	}
}

func TestOverheadRetriesErrorsAndCPU(t *testing.T) {
	in := calm()
	in.Operating.OverheadMean = 0.1 // 19% of 0.52s
	in.Operating.AttemptsPerRequest = 1.3
	in.Operating.ClientErrorRate = 0.05
	in.Container.Throttle, in.Container.ThrottleStage = 0.2, 1
	all := Advise(in)
	for _, recommendation := range all {
		if recommendation.Caveats == nil {
			t.Errorf("%s: caveats are null, which the schema rejects", recommendation.Rule)
		}
	}
	got := rules(all)
	for _, rule := range []string{RuleOverheadShare, RuleRetryPressure, RuleSurvivorship, RuleCPUBound} {
		if !slices.Contains(got, rule) {
			t.Errorf("missing %s in %v", rule, got)
		}
	}
}

func TestStreamingRunsCannotJudgeOverhead(t *testing.T) {
	in := calm()
	in.IsStreaming = true
	for i := range in.Stages {
		in.Stages[i].HasOverhead = false
	}
	in.Operating = in.Stages[1]
	got := Advise(in)
	if slices.Contains(rules(got), RuleOverheadShare) {
		t.Error("L2 without an overhead histogram")
	}
	if !slices.ContainsFunc(got[0].Caveats, func(caveat string) bool { return strings.Contains(caveat, "no overhead for streaming") }) {
		t.Errorf("caveats = %v", got[0].Caveats)
	}
	if !strings.Contains(got[0].Evidence[0].Text, "network, queueing and LiteLLM overhead") {
		t.Errorf("budget = %q", got[0].Evidence[0].Text)
	}
}

func TestViewStageReadsTheProxysSeries(t *testing.T) {
	start := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	stage := input.Stage{Index: 1, Start: start, End: start.Add(time.Minute)}
	series := func(name string, labels map[string]string, before, after float64) input.Series {
		all := map[string]string{"__name__": name, "pod": "litellm-0"}
		for key, value := range labels {
			all[key] = value
		}
		return input.Series{Labels: all, Samples: []input.Sample{{At: start, Value: before}, {At: start.Add(30 * time.Second), Value: after}}}
	}
	matrix := input.MapMatrix{
		queueTime: {series("litellm_request_queue_time_seconds_sum", nil, 0, 2), series("litellm_request_queue_time_seconds_count", nil, 0, 100),
			series("litellm_request_queue_time_seconds_bucket", map[string]string{"le": "0.05"}, 0, 100), series("litellm_request_queue_time_seconds_bucket", map[string]string{"le": "+Inf"}, 0, 100)},
		overheadLatency: {series("litellm_overhead_latency_metric_sum", nil, 0, 5), series("litellm_overhead_latency_metric_count", nil, 0, 50)},
		upstreamLatency: {series("litellm_llm_api_latency_metric_sum", nil, 0, 45), series("litellm_llm_api_latency_metric_count", nil, 0, 100)},
		proxyRequests:   {series("litellm_proxy_total_requests_metric_total", nil, 0, 100), series("litellm_proxy_total_requests_metric_total", nil, 0, 100)},
		deploymentRequests: {series("litellm_deployment_total_requests_total", nil, 0, 220),
			// Another pod's series is not this proxy's.
			{Labels: map[string]string{"__name__": "litellm_deployment_total_requests_total", "pod": "other"}, Samples: []input.Sample{{At: start, Value: 0}, {At: start.Add(30 * time.Second), Value: 999}}}},
	}
	mean := 0.5
	view, err := ViewStage(matrix, map[string]bool{"litellm-0": true}, stage,
		spec.StageAggregate{AchievedRps: 50, Requests: 200, Errors: 4, E2E: &spec.LatencySummary{MeanSeconds: &mean}})
	if err != nil {
		t.Fatal(err)
	}
	if view.OverheadMean != 0.1 || view.UpstreamMean != 0.45 || view.AttemptsPerRequest != 1.1 || view.ClientErrorRate != 0.02 {
		t.Errorf("view = %+v", view)
	}
	if !view.HasQueue || view.QueueP95 != 0.0475 || !view.HasAttempts {
		t.Errorf("view = %+v", view)
	}
}
