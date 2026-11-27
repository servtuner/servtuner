package litellm

import (
	"fmt"
	"math"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// LiteLLM advisor thresholds.
const (
	// L1: queue-time p95 up 50% across the ladder, with the proxy's CPU p95
	// above 80% of a core per worker: each worker is one Python event loop,
	// so a worker near a full core is saturated whatever the CPU limit.
	queueRisingRatio = 1.5
	cpuPerWorkerBusy = 0.8
	// L2: LiteLLM's own overhead above 10% of the client's mean E2E.
	overheadLargeShare = 0.10
	// L3: more than 1.1 upstream attempts per request the proxy received.
	attemptPressure = 1.1
	// L4: a client error rate above 1%.
	errorRateWarning = 0.01
	// L5 (as Bifrost's B6): throttled over 5%, or CPU p95 over 80% of the limit.
	throttleBound = 0.05
	cpuLimitShare = 0.8
)

// Rule ids, as recommendations name them.
const (
	RuleWorkerBound   = "L1"
	RuleOverheadShare = "L2"
	RuleRetryPressure = "L3"
	RuleSurvivorship  = "L4"
	RuleCPUBound      = "L5"
	// RuleLatencyBudget is cross-target rule X1.
	RuleLatencyBudget = "X1"
)

// Container is the proxy container's usage at the operating point and its
// captured CPU limit (zero when it has none), and the proxy's pods.
type Container struct {
	Pods          map[string]bool
	CPUP95        float64
	CPULimitCores float64
	Throttle      float64
	ThrottleStage int
}

// Input is everything the advisor reads.
type Input struct {
	Proxy spec.RecommendationTarget
	// Stages are the usable stages in order; Operating is the operating point.
	Stages    []StageView
	Operating StageView
	Container Container
	// Workers is the captured litellm.num_workers; IsWorkersCaptured is false
	// when no pod set it, and LiteLLM's default of 1 is assumed.
	Workers           float64
	IsWorkersCaptured bool
	// IsStreaming is the RunSpec's workload.streaming: LiteLLM does not
	// observe its overhead histogram for streaming requests.
	IsStreaming bool
	Confidence  spec.RecommendationConfidence
}

// Advise applies the rules and returns their recommendations.
func Advise(in Input) []spec.Recommendation {
	a := advisor{in: in}
	a.workerBound()
	a.overheadShare()
	a.retryPressure()
	a.survivorship()
	a.cpuBound()
	a.latencyBudget()
	return a.out
}

type advisor struct {
	in  Input
	out []spec.Recommendation
}

// workerBound (L1): requests wait longer to be picked up as load rises while
// every worker is near a full core: the proxy needs more workers.
func (a *advisor) workerBound() {
	var first, last *StageView
	for i := range a.in.Stages {
		stage := &a.in.Stages[i]
		if !stage.HasQueue || stage.QueueP95 <= 0 {
			continue
		}
		if first == nil {
			first = stage
		}
		last = stage
	}
	if first == nil || last == first || last.QueueP95/first.QueueP95 < queueRisingRatio {
		return
	}
	perWorker := a.in.Container.CPUP95 / math.Max(1, a.in.Workers)
	if perWorker < cpuPerWorkerBusy {
		return
	}
	caveats := []string{
		"Each worker needs about a core: raise the CPU request and limit with the workers (see the cpu recommendations).",
		"Each worker is a separate process with its own memory: raise the memory limit with the workers. On kind, one v1.108.3 worker used about 910 MiB, and two workers under a 1 GiB limit were OOMKilled at startup.",
		"With more than one worker LiteLLM needs PROMETHEUS_MULTIPROC_DIR for its metrics; it creates one itself when the prometheus callback is on.",
	}
	if !a.in.IsWorkersCaptured {
		caveats = append(caveats, "The worker count was not captured (no --num_workers or NUM_WORKERS on the pods), so LiteLLM's default of 1 is assumed.")
	}
	index := last.Index
	a.advise(RuleWorkerBound, "litellm.num_workers",
		fmt.Sprintf("Raise --num_workers (env NUM_WORKERS) above %s: the workers are saturated.", derive.FormatNumber(a.in.Workers)),
		[]spec.RecommendationEvidence{{
			Text: fmt.Sprintf("Queue-time p95 rose %s× from stage %d to %d (%ss to %ss) while CPU p95 was %s cores over %s worker(s)",
				derive.FormatNumber(last.QueueP95/first.QueueP95), first.Index, last.Index,
				derive.FormatNumber(first.QueueP95), derive.FormatNumber(last.QueueP95),
				derive.FormatNumber(a.in.Container.CPUP95), derive.FormatNumber(a.in.Workers)),
			StageIndex: &index, Series: stringPointer("litellm/request_queue_time_seconds"),
		}}, caveats)
}

// overheadShare (L2): LiteLLM's own processing is a large part of what the
// client waits for.
func (a *advisor) overheadShare() {
	operating := a.in.Operating
	if !operating.HasOverhead || operating.ClientE2EMean <= 0 {
		return
	}
	share := operating.OverheadMean / operating.ClientE2EMean
	if share <= overheadLargeShare {
		return
	}
	a.advise(RuleOverheadShare, "litellm.overhead",
		"Investigate the proxy-side cost: callbacks, logging and auth. Compare against a run of the proxy over the mock.",
		[]spec.RecommendationEvidence{{
			Text: fmt.Sprintf("LiteLLM's overhead averaged %ss, %.1f%% of the client's mean E2E of %ss at the operating point",
				derive.FormatNumber(operating.OverheadMean), 100*share, derive.FormatNumber(operating.ClientE2EMean)),
			StageIndex: &operating.Index, Series: stringPointer("litellm/overhead_latency_seconds"),
		}}, []string{"LiteLLM observes its overhead histogram for non-streaming requests only (verified on v1.108.3)."})
}

// retryPressure (L3): the proxy sends more attempts upstream than it receives.
func (a *advisor) retryPressure() {
	operating := a.in.Operating
	if !operating.HasAttempts || operating.AttemptsPerRequest <= attemptPressure {
		return
	}
	a.advise(RuleRetryPressure, "litellm.retries",
		"Review router_settings num_retries, allowed_fails and cooldown_time, and the fallbacks: each extra attempt is upstream load and latency.",
		[]spec.RecommendationEvidence{{
			Text: fmt.Sprintf("%s upstream attempts per request received at the operating point",
				derive.FormatNumber(operating.AttemptsPerRequest)),
			StageIndex: &operating.Index, Series: stringPointer("litellm/deployment_total_requests_total"),
		}}, []string{"bench does not capture LiteLLM's config.yaml, so the current router settings are not shown."})
}

// survivorship (L4): a warning, not a tuning rule.
func (a *advisor) survivorship() {
	operating := a.in.Operating
	if operating.ClientErrorRate <= errorRateWarning {
		return
	}
	a.adviseWith(RuleSurvivorship, spec.RecommendationConfidenceHigh, "litellm.survivorship",
		"Read latency from the client's numbers, not LiteLLM's: its latency histograms record successes only, so their tail is too short.",
		[]spec.RecommendationEvidence{{
			Text:       fmt.Sprintf("%.1f%% of requests failed at the operating point", 100*operating.ClientErrorRate),
			StageIndex: &operating.Index,
		}}, nil)
}

// cpuBound (L5): throttling or CPU near the limit.
func (a *advisor) cpuBound() {
	container := a.in.Container
	nearLimit := container.CPULimitCores > 0 && container.CPUP95 > cpuLimitShare*container.CPULimitCores
	if container.Throttle <= throttleBound && !nearLimit {
		return
	}
	a.advise(RuleCPUBound, "litellm.cpu_bound",
		"Raise the CPU limit, or the workers with it, or add replicas (see the cpu.limit recommendation).",
		[]spec.RecommendationEvidence{{
			Text: fmt.Sprintf("CPU p95 %s of a %s-core limit; throttled %.1f%% in stage %d",
				derive.FormatNumber(container.CPUP95), derive.FormatNumber(container.CPULimitCores), 100*container.Throttle, container.ThrottleStage),
			StageIndex: &container.ThrottleStage, Series: stringPointer("kubernetes/cpu_throttle_ratio"),
		}}, nil)
}

// latencyBudget (X1) splits the client's mean E2E at each usable stage into
// the upstream call, LiteLLM's overhead when it was observed, and the rest:
// client network, queueing and pre-call processing.
func (a *advisor) latencyBudget() {
	var evidence []spec.RecommendationEvidence
	for _, stage := range a.in.Stages {
		if !stage.HasUpstream || stage.ClientE2EMean <= 0 {
			continue
		}
		evidence = append(evidence, budgetOf(stage))
	}
	if len(evidence) == 0 {
		return
	}
	caveats := []string{"Means, not tails, and of successful requests only on LiteLLM's side."}
	if a.in.IsStreaming {
		caveats = append(caveats, "Streaming: LiteLLM records no overhead for streaming requests, so its overhead is inside the rest.")
	}
	a.advise(RuleLatencyBudget, "latency_budget",
		"Where the time goes, per stage: spend tuning effort on the largest share.", evidence, caveats)
}

func budgetOf(stage StageView) spec.RecommendationEvidence {
	share := func(seconds float64) string { return fmt.Sprintf("%.1f%%", 100*seconds/stage.ClientE2EMean) }
	parts := fmt.Sprintf("upstream %ss (%s)", derive.FormatNumber(stage.UpstreamMean), share(stage.UpstreamMean))
	rest := stage.ClientE2EMean - stage.UpstreamMean
	restName := "network, queueing and LiteLLM overhead"
	if stage.HasOverhead {
		parts += fmt.Sprintf(" + LiteLLM overhead %ss (%s)", derive.FormatNumber(stage.OverheadMean), share(stage.OverheadMean))
		rest -= stage.OverheadMean
		restName = "network and queueing"
	}
	rest = math.Max(0, rest)
	index := stage.Index
	return spec.RecommendationEvidence{
		Text: fmt.Sprintf("Stage %d at %s RPS: E2E %ss = %s + %s %ss (%s)", stage.Index, derive.FormatNumber(stage.RPS),
			derive.FormatNumber(stage.ClientE2EMean), parts, restName, derive.FormatNumber(rest), share(rest)),
		StageIndex: &index, Series: stringPointer("litellm/llm_api_latency_seconds"),
	}
}

func (a *advisor) advise(rule, name, directive string, evidence []spec.RecommendationEvidence, caveats []string) {
	a.adviseWith(rule, a.in.Confidence, name, directive, evidence, caveats)
}

func (a *advisor) adviseWith(rule string, confidence spec.RecommendationConfidence, name, directive string, evidence []spec.RecommendationEvidence, caveats []string) {
	a.out = append(a.out, spec.Recommendation{
		Id: rule + "/" + string(a.in.Proxy.Role), Rule: rule, Target: a.in.Proxy,
		Subject:     spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindAdvice, Name: name},
		Recommended: directive, Tier: spec.RecommendationTierDiagnostic, Confidence: confidence,
		// The schema wants an array, never null, for a rule with no caveats.
		Evidence: evidence, Caveats: append([]string{}, caveats...),
	})
}

func stringPointer(value string) *string { return &value }
