package sglang

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// SGLang advisor thresholds.
const (
	// S1: the KV pool at least 95% full while requests wait: memory, not the
	// scheduler, holds them back.
	kvFull = 0.95
	// S3: requests wait while running sits at the cap and the KV pool is below
	// 80%: the cap holds them back, and memory has room for more.
	kvRoom = 0.8
	// S4: under 50% of prompt tokens served from the prefix cache on a
	// workload built to share prefixes.
	prefixReuseLow = 0.5
	// SGLang's default schedule policy (v0.5.23, --schedule-policy).
	defaultSchedulePolicy = "fcfs"
	// The cache-aware policy: longest prefix match first.
	prefixAwarePolicy = "lpm"
)

// Rule ids, as recommendations name them.
const (
	RuleKVBound     = "S1"
	RuleRetraction  = "S2"
	RuleRunningCap  = "S3"
	RulePrefixReuse = "S4"
)

// Knobs are the captured SGLang flags the rules read; a zero or empty value
// means not captured (SGLang's own default then applies).
type Knobs struct {
	MemFractionStatic  float64
	MaxRunningRequests float64
	SchedulePolicy     string
	// IsRadixCacheDisabled is --disable-radix-cache; IsRadixCacheCaptured says
	// whether it was read at all.
	IsRadixCacheDisabled, IsRadixCacheCaptured bool
}

// Input is everything the advisor reads.
type Input struct {
	Target spec.RecommendationTarget
	// Stages are the usable stages in order; Operating is the operating point.
	Stages    []StageView
	Operating StageView
	Knobs     Knobs
	// IsSharedPrefixWorkload is true when the workload's prompts share
	// prefixes (dataset.shared_prefix): the prefix cache should hit.
	IsSharedPrefixWorkload bool
	Confidence             spec.RecommendationConfidence
}

// Advise applies the rules and returns their recommendations.
func Advise(in Input) []spec.Recommendation {
	a := advisor{in: in}
	a.kvBound()
	a.retraction()
	a.runningCap()
	a.prefixReuse()
	return a.out
}

type advisor struct {
	in  Input
	out []spec.Recommendation
}

// firstStage is the first usable stage that satisfies the condition, or nil.
func (a *advisor) firstStage(condition func(StageView) bool) *StageView {
	for i := range a.in.Stages {
		if condition(a.in.Stages[i]) {
			return &a.in.Stages[i]
		}
	}
	return nil
}

// kvBound (S1): the KV pool fills up while requests queue.
func (a *advisor) kvBound() {
	stage := a.firstStage(func(s StageView) bool { return s.HasScheduler && s.TokenUsagePeak >= kvFull && s.QueuePeak > 0 })
	if stage == nil {
		return
	}
	directive := "Give the KV pool more memory with --mem-fraction-static, or admit fewer requests at once with --max-running-requests."
	if a.in.Knobs.MemFractionStatic > 0 {
		directive = fmt.Sprintf("Raise --mem-fraction-static above %s, or lower --max-running-requests: the KV pool is full.",
			derive.FormatNumber(a.in.Knobs.MemFractionStatic))
	}
	a.advise(RuleKVBound, "sglang.mem_fraction_static", directive, stage,
		fmt.Sprintf("The KV pool reached %.0f%% with %s requests waiting in stage %d at %s RPS",
			100*stage.TokenUsagePeak, derive.FormatNumber(stage.QueuePeak), stage.Index, derive.FormatNumber(stage.RPS)),
		"sglang/token_usage",
		[]string{"A higher --mem-fraction-static leaves less memory for CUDA graphs and activations: if the server then fails to start, lower --cuda-graph-max-bs-decode."})
}

// retraction (S2): running requests were sent back to the queue at the
// operating point, which wastes the work already done on them.
func (a *advisor) retraction() {
	operating := a.in.Operating
	if !operating.HasRetracted || operating.Retracted <= 0 {
		return
	}
	a.advise(RuleRetraction, "sglang.max_running_requests",
		"Lower --max-running-requests, or give the KV pool more memory (--mem-fraction-static): SGLang retracted running requests to make room.",
		&operating,
		fmt.Sprintf("%s requests were retracted at the operating point (stage %d, %s RPS)",
			derive.FormatNumber(operating.Retracted), operating.Index, derive.FormatNumber(operating.RPS)),
		"sglang/num_retracted_requests_total",
		[]string{"A raised --schedule-conservativeness also makes SGLang admit fewer new requests while memory is tight."})
}

// runningCap (S3): requests wait, running sits at the captured cap, and the KV
// pool has room: the cap is the limit.
func (a *advisor) runningCap() {
	limit := a.in.Knobs.MaxRunningRequests
	if limit <= 0 {
		return // the effective cap was not captured: nothing to compare against
	}
	stage := a.firstStage(func(s StageView) bool {
		return s.HasScheduler && s.QueuePeak > 0 && s.RunningPeak >= limit && s.TokenUsagePeak < kvRoom
	})
	if stage == nil {
		return
	}
	a.advise(RuleRunningCap, "sglang.max_running_requests",
		fmt.Sprintf("Raise --max-running-requests above %s: requests wait for a running slot while the KV pool has room.", derive.FormatNumber(limit)),
		stage,
		fmt.Sprintf("Stage %d at %s RPS: %s running (the cap), %s waiting, KV pool at %.0f%%",
			stage.Index, derive.FormatNumber(stage.RPS), derive.FormatNumber(stage.RunningPeak), derive.FormatNumber(stage.QueuePeak), 100*stage.TokenUsagePeak),
		"sglang/num_running_reqs", nil)
}

// prefixReuse (S4): a workload built to share prefixes hits the prefix cache
// rarely, so the cache or the schedule policy is wrong for it.
func (a *advisor) prefixReuse() {
	if !a.in.IsSharedPrefixWorkload {
		return
	}
	knobs := a.in.Knobs
	if knobs.IsRadixCacheCaptured && knobs.IsRadixCacheDisabled {
		operating := a.in.Operating
		a.advise(RulePrefixReuse, "sglang.disable_radix_cache",
			"Drop --disable-radix-cache: this workload shares prefixes, and without the radix cache every prompt is computed in full.",
			&operating, "The workload shares prompt prefixes and the radix (prefix) cache is disabled", "", nil)
		return
	}
	operating := a.in.Operating
	if !operating.HasPrefixReuse || operating.PrefixReuse >= prefixReuseLow {
		return
	}
	policy := knobs.SchedulePolicy
	caveats := []string(nil)
	if policy == "" {
		policy = defaultSchedulePolicy
		caveats = []string{"The schedule policy was not captured, so SGLang's default (fcfs) is assumed."}
	}
	if policy == prefixAwarePolicy {
		return // already cache-aware: the low reuse is the workload's, not the policy's
	}
	a.advise(RulePrefixReuse, "sglang.schedule_policy",
		fmt.Sprintf("Try --schedule-policy %s (now %s): it runs requests that share a cached prefix first.", prefixAwarePolicy, policy),
		&operating,
		fmt.Sprintf("Only %.0f%% of prompt tokens came from the prefix cache at the operating point, on a workload that shares prefixes",
			100*operating.PrefixReuse),
		"sglang/cached_tokens_total", caveats)
}

func (a *advisor) advise(rule, name, directive string, stage *StageView, evidence, series string, caveats []string) {
	index := stage.Index
	item := spec.RecommendationEvidence{Text: evidence, StageIndex: &index}
	if series != "" {
		item.Series = &series
	}
	a.out = append(a.out, spec.Recommendation{
		Id: rule + "/" + string(a.in.Target.Role), Rule: rule, Target: a.in.Target,
		Subject:     spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindAdvice, Name: name},
		Recommended: directive, Tier: spec.RecommendationTierDiagnostic, Confidence: a.in.Confidence,
		// The schema wants an array, never null, for a rule with no caveats.
		Evidence: []spec.RecommendationEvidence{item}, Caveats: append([]string{}, caveats...),
	})
}
