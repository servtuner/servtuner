package bifrost

import (
	"fmt"
	"math"
	"slices"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// Bifrost advisor thresholds.
const (
	// B1: active requests at 90% of the concurrency knob, overhead p95 up 50%
	// across the ladder while the upstream stayed within 20%.
	concurrencyBoundActive = 0.9
	overheadRisingRatio    = 1.5
	upstreamFlatRatio      = 1.2
	// B3: Bifrost's own overhead under 5% of E2E.
	overheadSmallShare = 0.05
	// B4: more than 0.1 retries per attempt, or over 1% of attempts on a fallback.
	retryPressure    = 0.1
	fallbackPressure = 0.01
	// B6: throttled over 5%, or CPU p95 over 80% of the limit.
	throttleBound = 0.05
	cpuLimitShare = 0.8
	// B7: working set above 85% of the memory limit.
	memoryLimitShare = 0.85
)

// Rule ids, as recommendations name them.
const (
	RuleConcurrencyBound = "B1"
	RuleQueueSizing      = "B2"
	RuleUpstreamBound    = "B3"
	RuleRetryPressure    = "B4"
	RuleCPUBound         = "B6"
	RuleMemory           = "B7"
	RuleLogging          = "B8"
	RulePerfHostile      = "B9"
	// RuleLatencyBudget is the cross-target rule X1.
	RuleLatencyBudget = "X1"
)

// Rules the queueing model writes, which the advisor names B1 and B2 for
// Bifrost (bench/internal/analyze/queueing).
const (
	queueingConcurrencyRule = "queueing.concurrency"
	queueingQueueRule       = "queueing.queue"
)

// Container is the router container's usage at the operating point and its
// captured limits (zero when it has none), and the router's pods.
type Container struct {
	Pods                         map[string]bool
	CPUP95, CPULimitCores        float64
	Throttle                     float64
	ThrottleStage                int
	MemoryPeak, MemoryLimitBytes float64
}

// Input is everything the advisor reads.
type Input struct {
	Router spec.RecommendationTarget
	// Upstream is the first upstream, which B3 points at; nil without one.
	Upstream *spec.RecommendationTarget
	// Stages are the usable stages in order; Operating is the operating point.
	Stages     []StageView
	Operating  StageView
	Config     Config
	Container  Container
	Confidence spec.RecommendationConfidence
	RunCaveats []string
}

// Advise applies the rules. It renames the queueing model's C and B
// recommendations for the router to B1 and B2 (in place) and returns the new
// recommendations.
func Advise(in Input, existing []spec.Recommendation) []spec.Recommendation {
	a := advisor{in: in}
	a.queueRules(existing)
	a.upstreamBound()
	a.retryPressure()
	a.cpuBound()
	a.memory()
	a.logging()
	a.perfHostile()
	a.latencyBudget()
	return a.out
}

// latencyBudget (X1) splits the client's mean E2E at each usable stage into
// the upstream's time, Bifrost's own overhead, and the rest: client network
// and what the router does before the upstream call. Bifrost releases without
// the overhead histogram (v1.5 on kind) get upstream and the rest only.
func (a *advisor) latencyBudget() {
	var evidence []spec.RecommendationEvidence
	hasOverhead := false
	for _, stage := range a.in.Stages {
		if !stage.HasUpstream || stage.ClientE2EMean <= 0 {
			continue
		}
		hasOverhead = hasOverhead || stage.HasOverhead
		evidence = append(evidence, budgetOf(stage))
	}
	if len(evidence) == 0 {
		return
	}
	caveats := []string{"Means, not tails. The upstream time is Bifrost's attempt wall time, which includes its queue wait and retries."}
	if !hasOverhead {
		caveats = append(caveats, "This Bifrost exports no bifrost_overhead_latency_microseconds, so its own overhead is inside the rest; rules B1 and B3, which need it, cannot be judged.")
	}
	a.advise(RuleLatencyBudget, a.in.Router, "latency_budget",
		"Where the time goes, per stage: spend tuning effort on the largest share.", evidence, caveats)
}

func budgetOf(stage StageView) spec.RecommendationEvidence {
	share := func(seconds float64) string { return fmt.Sprintf("%.1f%%", 100*seconds/stage.ClientE2EMean) }
	parts := fmt.Sprintf("upstream %ss (%s)", derive.FormatNumber(stage.UpstreamMean), share(stage.UpstreamMean))
	rest := stage.ClientE2EMean - stage.UpstreamMean
	restName := "network, pre-upstream and Bifrost overhead"
	if stage.HasOverhead {
		parts += fmt.Sprintf(" + Bifrost overhead %ss (%s)", derive.FormatNumber(stage.OverheadMean), share(stage.OverheadMean))
		rest -= stage.OverheadMean
		restName = "network and pre-upstream"
	}
	rest = math.Max(0, rest)
	index := stage.Index
	return spec.RecommendationEvidence{
		Text: fmt.Sprintf("Stage %d at %s RPS: E2E %ss = %s + %s %ss (%s)", stage.Index, derive.FormatNumber(stage.RPS),
			derive.FormatNumber(stage.ClientE2EMean), parts, restName, derive.FormatNumber(rest), share(rest)),
		StageIndex: &index, Series: stringPointer("bifrost/upstream_latency_seconds"),
	}
}

type advisor struct {
	in  Input
	out []spec.Recommendation
}

// ratio is last over first of a per-stage value across the usable stages;
// ok is false with fewer than two stages that have it.
func (a advisor) ratio(value func(StageView) (float64, bool)) (ratio float64, first, last StageView, ok bool) {
	var seen []StageView
	for _, stage := range a.in.Stages {
		if v, has := value(stage); has && v > 0 {
			seen = append(seen, stage)
		}
	}
	if len(seen) < 2 {
		return 0, StageView{}, StageView{}, false
	}
	first, last = seen[0], seen[len(seen)-1]
	from, _ := value(first)
	to, _ := value(last)
	return to / from, first, last, true
}

func upstreamMean(stage StageView) (float64, bool) { return stage.UpstreamMean, stage.HasUpstream }
func overheadP95(stage StageView) (float64, bool)  { return stage.OverheadP95, stage.HasOverhead }

// queueRules: B1 when Bifrost's own concurrency is what limits it; B2 always,
// for the queue.
func (a *advisor) queueRules(existing []spec.Recommendation) {
	overheadRatio, first, last, hasOverhead := a.ratio(overheadP95)
	upstreamRatio, _, _, hasUpstream := a.ratio(upstreamMean)
	isBound := a.in.Operating.HasActive && a.in.Operating.ActiveFraction >= concurrencyBoundActive &&
		hasOverhead && overheadRatio >= overheadRisingRatio && hasUpstream && upstreamRatio < upstreamFlatRatio
	for i := range existing {
		recommendation := &existing[i]
		if recommendation.Target.Role != a.in.Router.Role || recommendation.Target.TargetType != spec.TargetTypeBifrost {
			continue
		}
		switch recommendation.Rule {
		case queueingQueueRule:
			recommendation.Rule = RuleQueueSizing
			recommendation.Id = RuleQueueSizing + "/" + string(recommendation.Target.Role)
			a.markDrops(recommendation)
		case queueingConcurrencyRule:
			if !isBound {
				continue
			}
			recommendation.Rule = RuleConcurrencyBound
			recommendation.Id = RuleConcurrencyBound + "/" + string(recommendation.Target.Role)
			recommendation.Evidence = append(recommendation.Evidence, spec.RecommendationEvidence{
				Text: fmt.Sprintf("Concurrency-bound: active requests reached %.0f%% of the concurrency knob; overhead p95 rose %s× from stage %d to %d while the upstream stayed within %s×",
					100*a.in.Operating.ActiveFraction, derive.FormatNumber(overheadRatio), first.Index, last.Index, derive.FormatNumber(upstreamRatio)),
				StageIndex: &last.Index, Series: stringPointer("bifrost/overhead_latency_microseconds"),
			})
		}
	}
}

// markDrops makes B2 urgent when Bifrost rejected requests with a full queue
// in any usable stage.
func (a *advisor) markDrops(recommendation *spec.Recommendation) {
	for _, stage := range a.in.Stages {
		if !stage.HasDropped || stage.Dropped <= 0 {
			continue
		}
		urgent := true
		recommendation.IsUrgent = &urgent
		index := stage.Index
		recommendation.Evidence = append(recommendation.Evidence, spec.RecommendationEvidence{
			Text: fmt.Sprintf("Urgent: Bifrost dropped %s requests with a full queue in stage %d at %s RPS",
				derive.FormatNumber(math.Round(stage.Dropped)), stage.Index, derive.FormatNumber(stage.RPS)),
			StageIndex: &index, Series: stringPointer("bifrost/error_requests_total"),
		})
		return
	}
}

// upstreamBound (B3): the upstream slows with load and Bifrost's overhead is
// a sliver of E2E, so Bifrost is not the bottleneck.
func (a *advisor) upstreamBound() {
	upstreamRatio, first, last, ok := a.ratio(upstreamMean)
	operating := a.in.Operating
	if !ok || upstreamRatio < upstreamFlatRatio || !operating.HasOverhead || operating.ClientE2EMean <= 0 {
		return
	}
	share := operating.OverheadP95 / operating.ClientE2EMean
	if share >= overheadSmallShare {
		return
	}
	targetOf := a.in.Router
	if a.in.Upstream != nil {
		targetOf = *a.in.Upstream
	}
	a.advise(RuleUpstreamBound, targetOf, "bifrost.upstream_bound",
		"Bifrost is not the bottleneck: scale or tune the upstream, and do not add Bifrost replicas.",
		[]spec.RecommendationEvidence{{
			Text: fmt.Sprintf("Upstream latency rose %s× from stage %d to %d; Bifrost's overhead p95 is %.1f%% of E2E at the operating point",
				derive.FormatNumber(upstreamRatio), first.Index, last.Index, 100*share),
			StageIndex: &operating.Index, Series: stringPointer("bifrost/upstream_latency_seconds"),
		}}, []string{"Hand off to the upstream's advisor when it is in the experiment."})
}

// retryPressure (B4): retries and fallbacks inflate overhead and upstream load.
func (a *advisor) retryPressure() {
	operating := a.in.Operating
	retries := operating.HasRetries && operating.RetriesPerAttempt > retryPressure
	fallbacks := operating.HasFallbacks && operating.FallbackShare > fallbackPressure
	if !retries && !fallbacks {
		return
	}
	text := fmt.Sprintf("%s retries per attempt and %.1f%% of attempts on a fallback at the operating point",
		derive.FormatNumber(operating.RetriesPerAttempt), 100*operating.FallbackShare)
	a.advise(RuleRetryPressure, a.in.Router, "bifrost.retries",
		"Review network_config.max_retries and the backoff, and the fallback config: retries inflate overhead and upstream load.",
		[]spec.RecommendationEvidence{{Text: text, StageIndex: &operating.Index, Series: stringPointer("bifrost/request_retries")}},
		[]string{"The upstream's λ in the queueing model includes these attempts."})
}

// cpuBound (B6): throttling or CPU near the limit.
func (a *advisor) cpuBound() {
	container := a.in.Container
	nearLimit := container.CPULimitCores > 0 && container.CPUP95 > cpuLimitShare*container.CPULimitCores
	if container.Throttle <= throttleBound && !nearLimit {
		return
	}
	text := fmt.Sprintf("CPU p95 %s of a %s-core limit; throttled %.1f%% in stage %d",
		derive.FormatNumber(container.CPUP95), derive.FormatNumber(container.CPULimitCores), 100*container.Throttle, container.ThrottleStage)
	a.advise(RuleCPUBound, a.in.Router, "bifrost.cpu_bound",
		"Raise the CPU limit or add replicas (see the cpu.limit recommendation).",
		[]spec.RecommendationEvidence{{Text: text, StageIndex: &container.ThrottleStage, Series: stringPointer("kubernetes/cpu_throttle_ratio")}},
		[]string{"GOMAXPROCS follows the CPU limit, so a higher limit also means more parallelism in Bifrost."})
}

// memory (B7): working set near the limit.
func (a *advisor) memory() {
	container := a.in.Container
	if container.MemoryLimitBytes <= 0 || container.MemoryPeak <= memoryLimitShare*container.MemoryLimitBytes {
		return
	}
	a.advise(RuleMemory, a.in.Router, "bifrost.memory",
		"Set GOMEMLIMIT to about 90% of the memory limit (see the GOMEMLIMIT recommendation), and consider GOGC=200. Confirm with an A/B run.",
		[]spec.RecommendationEvidence{{Text: fmt.Sprintf("Working set peaked at %.0f%% of the memory limit", 100*container.MemoryPeak/container.MemoryLimitBytes),
			StageIndex: &a.in.Operating.Index, Series: stringPointer("kubernetes/memory_working_set_bytes")}},
		[]string{"Vendor guidance (Bifrost's docs)."})
}

// logging (B8): request logging is on, a cost to measure rather than switch off.
func (a *advisor) logging() {
	config := a.in.Config
	if config.EnableLogging == nil || !*config.EnableLogging || (config.DisableContentLogging != nil && *config.DisableContentLogging) {
		return
	}
	a.adviseWith(RuleLogging, spec.RecommendationConfidenceLow, a.in.Router, "bifrost.logging",
		"Measure with client.enable_logging off, or with disable_content_logging, to quantify what logging costs.",
		[]spec.RecommendationEvidence{{Text: "client_config.enable_logging is true and content logging is on", StageIndex: &a.in.Operating.Index}},
		[]string{"A cost to quantify, not an automatic \"turn it off\": the run has no logging-off baseline to compare with."})
}

// perfHostile (B9): settings that copy payloads or add cardinality.
func (a *advisor) perfHostile() {
	var settings []string
	for _, provider := range a.in.Config.Providers {
		for name, value := range map[string]*bool{
			"send_back_raw_request": provider.SendBackRawRequest, "send_back_raw_response": provider.SendBackRawResponse,
			"store_raw_request_response": provider.StoreRawRequestResponse,
		} {
			if value != nil && *value {
				settings = append(settings, provider.Name+"."+name)
			}
		}
	}
	if len(settings) == 0 {
		return
	}
	slices.Sort(settings)
	a.advise(RulePerfHostile, a.in.Router, "bifrost.raw_payloads",
		"Turn these off for benchmarking: they copy every payload.",
		[]spec.RecommendationEvidence{{Text: fmt.Sprintf("On: %v", settings), StageIndex: &a.in.Operating.Index}}, nil)
}

func (a *advisor) advise(rule string, targetOf spec.RecommendationTarget, name, directive string, evidence []spec.RecommendationEvidence, caveats []string) {
	a.adviseWith(rule, a.in.Confidence, targetOf, name, directive, evidence, caveats)
}

func (a *advisor) adviseWith(rule string, confidence spec.RecommendationConfidence, targetOf spec.RecommendationTarget, name, directive string, evidence []spec.RecommendationEvidence, caveats []string) {
	a.out = append(a.out, spec.Recommendation{
		Id: rule + "/" + roleKey(targetOf), Rule: rule, Target: targetOf,
		Subject:     spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindAdvice, Name: name},
		Recommended: directive, Tier: spec.RecommendationTierDiagnostic, Confidence: confidence,
		Evidence: evidence, Caveats: append(append([]string{}, caveats...), a.in.RunCaveats...),
	})
}

func roleKey(targetOf spec.RecommendationTarget) string {
	if targetOf.UpstreamIndex != nil {
		return fmt.Sprintf("%s%d", targetOf.Role, *targetOf.UpstreamIndex)
	}
	return string(targetOf.Role)
}

func stringPointer(value string) *string { return &value }

// KnobsOf returns the C and B the recommendations settle on, for the patch.
func KnobsOf(recommendations []spec.Recommendation) map[string]float64 {
	knobs := map[string]float64{}
	for _, recommendation := range recommendations {
		if recommendation.Subject.Kind != spec.RecommendationSubjectKindKnob {
			continue
		}
		if value, ok := recommendation.Recommended.(float64); ok &&
			(recommendation.Subject.Name == target.KnobBifrostConcurrency || recommendation.Subject.Name == target.KnobBifrostBufferSize) {
			knobs[recommendation.Subject.Name] = value
		}
	}
	return knobs
}
