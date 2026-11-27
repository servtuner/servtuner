// Package vllm is the vLLM target advisor: rules V1
// to V12, read from vLLM's own series and histograms, the GPU's DCGM series,
// the request records and the knobs.
//
// Every rule looks at the usable stages at or below the knee: past the knee
// the target is overloaded, and every limit binds at once.
package vllm

import (
	"fmt"
	"math"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// Rule ids, as recommendations name them.
const (
	RuleSeqCapBound    = "V1"
	RuleKVBound        = "V2"
	RuleModelLenSlack  = "V3"
	RulePrefillHeavy   = "V4"
	RuleDecodeHeavy    = "V5"
	RulePrefixCacheHit = "V6"
	RuleFrontendBound  = "V7"
	RuleGPUMemory      = "V8"
	RuleGPUIdle        = "V9"
	RuleHistogramRes   = "V10"
	RuleLoadShedding   = "V11"
	RuleLogging        = "V12"
)

// Thresholds of the rules.
const (
	// SeqCapPinnedFraction: running requests at 98% of max_num_seqs or more are pinned at it.
	SeqCapPinnedFraction = 0.98
	// KVHeadroomCeiling: V1 needs KV usage below 80%, so the scheduler cap binds before memory.
	KVHeadroomCeiling = 0.80
	// KVBoundFloor: KV usage at 95% or more is KV-bound (V2).
	KVBoundFloor = 0.95
	// KVTarget is the KV usage V1's ceiling aims for, short of KVBoundFloor.
	KVTarget = 0.90
	// SeqCapStep: V1 at most doubles max_num_seqs in one step, then confirms with a run.
	SeqCapStep = 2.0
	// ModelLenSlackFraction: V3 fires when the observed p99.9 length is below half of max_model_len.
	ModelLenSlackFraction = 0.5
	// ModelLenMargin and ModelLenStep: V3 recommends 1.2 × the p99.9, rounded up to 256 tokens.
	ModelLenMargin = 1.2
	ModelLenStep   = 256
	// PrefixHitFloor: a hit rate below 10% means the workload does not reuse prefixes (V6).
	PrefixHitFloor = 0.10
	// PrefixHitShortfall: with a shared-prefix workload, a hit rate below half
	// the cacheable share means the cache is off or evicting the prefixes (V6).
	PrefixHitShortfall = 0.5
	// FrontendCPUFloor: V7 needs the target's CPU at 90% of its limit (or request) or more.
	FrontendCPUFloor = 0.90
	// FrontendGPUCeiling: V7 needs GPU activity below 60%.
	FrontendGPUCeiling = 0.60
	// IdleWaiting: a mean below one waiting request is "waiting ≈ 0" (V7).
	IdleWaiting = 1.0
	// FramebufferGrowth: V8 fires when GPU memory rises more than 5% above the plateau.
	FramebufferGrowth = 0.05
	// FramebufferFreeFloor: V8 fires when the free framebuffer falls below 2%.
	FramebufferFreeFloor = 0.02
	// IdleGPUCeiling: V9 fires below 50% GPU activity at the SLO-max stage.
	IdleGPUCeiling = 0.50
	// HistogramFirstBucket is the upper bound, in seconds, of the first bucket of
	// vLLM's request-phase histograms: a p95
	// below it is interpolated inside that bucket (V10).
	HistogramFirstBucket = 0.3
)

// StageView is one usable stage as the rules read it. A Has* field is false
// when the series was not collected: no evidence, not zero.
type StageView struct {
	Index       int
	AchievedRPS float64
	// RunningMax is the most requests running at once; WaitingMean the mean waiting.
	RunningMax, WaitingMean float64
	HasRunning, HasWaiting  bool
	// KVMax is the fullest KV cache, 0 to 1.
	KVMax float64
	HasKV bool
	// Preemptions is the counter's increase in the stage.
	Preemptions    float64
	HasPreemptions bool
	// PrefixHits and PrefixQueries are the counters' increases in the stage (tokens).
	PrefixHits, PrefixQueries float64
	HasPrefix                 bool
	SLOs                      []spec.SloResult
	// GPUActivity is the mean activity of the busiest target GPU, 0 to 1: DCGM
	// engine activity, or GPU_UTIL when the GPU has no profiling metrics
	// (GPUActivityIsUtil), which reads high, so a low value still means idle.
	GPUActivity       float64
	HasGPUActivity    bool
	GPUActivityIsUtil bool
	// GPUMemoryMean and GPUMemoryMax are the fullest GPU's framebuffer in use, bytes.
	GPUMemoryMean, GPUMemoryMax float64
	HasGPUMemory                bool
	// GPUFreeMin is the smallest free share of a target GPU's framebuffer, 0 to 1.
	GPUFreeMin    float64
	HasGPUFree    bool
	TargetCPUMax  float64 // the busiest target container's CPU over its limit (or request)
	HasTargetCPU  bool
	QueueP95      float64 // vLLM's server-side queue and prefill p95, seconds
	PrefillP95    float64
	HasQueueP95   bool
	HasPrefillP95 bool
	// Requests is the stage's request count; Rejected how many of them got
	// HTTP 503, vLLM's answer at a queue limit (V11).
	Requests, Rejected int
}

// Input is everything the advisor reads.
type Input struct {
	Target spec.RecommendationTarget
	// Stages are the usable stages in order.
	Stages []StageView
	// KneeIndex is the knee's stage index, or nil without a knee.
	KneeIndex *int
	// Knobs are the captured knobs, with the declared ones where nothing was captured.
	Knobs map[string]spec.KnobValue
	// TokensP999 is the p99.9 of prompt plus generation tokens per request, when known.
	TokensP999 float64
	HasTokens  bool
	Confidence spec.RecommendationConfidence
	// SLOMaxIndex is the SLO-max stage's index, or nil without one (V9).
	SLOMaxIndex *int
	// SharedPrefixShare is the share of prompt tokens the workload's shared
	// prefix makes cacheable (shared_prefix.tokens over the input_tokens
	// mean), or 0 when the workload shares no prefix (V6).
	SharedPrefixShare float64
}

// Advise applies V1 to V12 and returns their recommendations.
func Advise(in Input) []spec.Recommendation {
	stages := atOrBelowKnee(in.Stages, in.KneeIndex)
	var out []spec.Recommendation
	fired := map[string]bool{}
	rules := []func(Input, []StageView) *spec.Recommendation{
		seqCapBound, kvBound, modelLenSlack, prefillHeavy, decodeHeavy, prefixCacheHits, frontendBound, gpuMemoryHeadroom,
	}
	for _, rule := range rules {
		recommendation := rule(in, stages)
		if recommendation == nil {
			continue
		}
		// Requests queueing behind a pinned sequence cap make TTFT fail while
		// ITL holds; that is V1's cause, so V4 would blame prefill wrongly.
		if recommendation.Rule == RulePrefillHeavy && fired[RuleSeqCapBound] {
			continue
		}
		fired[recommendation.Rule] = true
		out = append(out, *recommendation)
	}
	// V9 explains an idle GPU only when no vLLM limit (V1, V2, V7) does.
	if !fired[RuleSeqCapBound] && !fired[RuleKVBound] && !fired[RuleFrontendBound] {
		if recommendation := idleGPU(in, in.Stages); recommendation != nil {
			out = append(out, *recommendation)
		}
	}
	// V10 and V11 read every usable stage: resolution matters past the knee
	// too, and shedding starts at overload.
	if recommendation := histogramResolution(in, in.Stages); recommendation != nil {
		out = append(out, *recommendation)
	}
	if recommendation := loadShedding(in, in.Stages); recommendation != nil {
		out = append(out, *recommendation)
	}
	// V12 reads the knobs only.
	if recommendation := costlyLogging(in); recommendation != nil {
		out = append(out, *recommendation)
	}
	return out
}

// costlyLoggingKnobs are the logging settings V12 flags when on, with why.
var costlyLoggingKnobs = []struct {
	knob, why string
}{
	{target.KnobVLLMEnableLogRequests, "--enable-log-requests logs every request, which costs the API server CPU on each one"},
	{target.KnobVLLMIterationDetails, "--enable-logging-iteration-details logs every engine step, which costs time on each step"},
	{target.KnobVLLMDisableLogStats, "--disable-log-stats turns off vLLM's stat loggers, Prometheus metrics included, so the server side of the run cannot be explained"},
}

// costlyLogging is V12: logging that slows the target, or that removes the
// metrics, is on. A benchmark should measure the target as it serves.
func costlyLogging(in Input) *spec.Recommendation {
	var evidence []spec.RecommendationEvidence
	subject := ""
	for _, setting := range costlyLoggingKnobs {
		if on, _ := in.Knobs[setting.knob].(bool); on {
			evidence = append(evidence, spec.RecommendationEvidence{Text: setting.why})
			if subject == "" {
				subject = setting.knob
			}
		}
	}
	if subject == "" {
		return nil
	}
	recommendation := in.recommendation(RuleLogging, subject, "", nil, evidence, []string{
		"Benchmark hygiene: turn these off for benchmark runs (and in production unless needed), or the results describe a slower server than the one that serves. The run itself stays valid.",
	})
	recommendation.Subject = spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindAdvice, Name: "vllm_logging"}
	return recommendation
}

// queueLimitKnobs are vLLM's admission limits, in the order V11 names them:
// past either one, the API server rejects new requests with HTTP 503.
var queueLimitKnobs = []string{target.KnobVLLMMaxNumQueuedReqs, target.KnobVLLMMaxNumQueuedTokens}

// loadShedding is V11: a queue limit is set and requests got HTTP 503, so the
// errors are admission control by design, not faults. They still count against
// the error-rate SLO: the client was refused.
func loadShedding(in Input, stages []StageView) *spec.Recommendation {
	var limits []string
	subject := ""
	for _, name := range queueLimitKnobs {
		if limit, ok := knob(in, name); ok && limit > 0 {
			limits = append(limits, fmt.Sprintf("%s=%s", strings.TrimPrefix(name, "vllm."), derive.FormatNumber(limit)))
			if subject == "" {
				subject = name
			}
		}
	}
	if subject == "" {
		return nil
	}
	var evidence []spec.RecommendationEvidence
	for _, stage := range stages {
		if stage.Rejected == 0 || stage.Requests == 0 {
			continue
		}
		evidence = append(evidence, stageEvidence(stage, "%d of %d requests (%.1f%%) rejected with HTTP 503 at %.1f RPS",
			stage.Rejected, stage.Requests, 100*float64(stage.Rejected)/float64(stage.Requests), stage.AchievedRPS))
	}
	if len(evidence) == 0 {
		return nil
	}
	return in.recommendation(RuleLoadShedding, subject, "", nil, evidence, []string{
		fmt.Sprintf("Load shedding: vLLM's queue limit (%s) rejected these requests by design, so they are admission control, not faults. They still count against the error-rate SLO, because the client was refused. The first stage that sheds is where capacity runs out: add replicas or GPUs for more; raising the limit only moves the wait into the queue.", strings.Join(limits, ", ")),
	})
}

// gpuSource names where GPU activity came from, for evidence.
func gpuSource(stage StageView) string {
	if stage.GPUActivityIsUtil {
		return "GPU utilization (no profiling metrics on this GPU)"
	}
	return "GPU engine activity"
}

// frontendBound is V7: the API server's CPU, not the GPU, limits the target.
func frontendBound(in Input, stages []StageView) *spec.Recommendation {
	for _, stage := range stages {
		if !stage.HasTargetCPU || !stage.HasGPUActivity || !stage.HasWaiting || stage.TargetCPUMax < FrontendCPUFloor ||
			stage.GPUActivity >= FrontendGPUCeiling || stage.WaitingMean >= IdleWaiting {
			continue
		}
		return in.recommendation(RuleFrontendBound, target.KnobVLLMAPIServerCount, "processes", nil, []spec.RecommendationEvidence{
			stageEvidence(stage, "target CPU at %.0f%% of its limit while %s averaged %.0f%% and %.1f requests waited on average",
				100*stage.TargetCPUMax, gpuSource(stage), 100*stage.GPUActivity, stage.WaitingMean),
		}, []string{
			"Frontend-bound: tokenization and the HTTP server are the bottleneck, not the GPU. Raise --api-server-count, or the container's CPU, and confirm with a run.",
		})
	}
	return nil
}

// gpuMemoryHeadroom is V8: non-KV GPU allocations grow during the run, or the
// framebuffer is nearly full, so a CUDA out-of-memory error is a risk.
func gpuMemoryHeadroom(in Input, stages []StageView) *spec.Recommendation {
	var plateau float64
	for _, stage := range stages {
		if stage.HasGPUMemory && plateau == 0 {
			plateau = stage.GPUMemoryMean // vLLM's reservation, set before the first stage
		}
		if stage.HasGPUFree && stage.GPUFreeMin < FramebufferFreeFloor {
			return in.gpuMemoryRecommendation(stageEvidence(stage, "free GPU framebuffer fell to %.1f%%", 100*stage.GPUFreeMin))
		}
		if stage.HasGPUMemory && plateau > 0 && stage.GPUMemoryMax > (1+FramebufferGrowth)*plateau {
			return in.gpuMemoryRecommendation(stageEvidence(stage, "GPU memory in use reached %s, %.1f%% above the %s plateau of the first stage",
				formatBytes(stage.GPUMemoryMax), 100*(stage.GPUMemoryMax/plateau-1), formatBytes(plateau)))
		}
	}
	return nil
}

func (in Input) gpuMemoryRecommendation(evidence spec.RecommendationEvidence) *spec.Recommendation {
	return in.recommendation(RuleGPUMemory, target.KnobVLLMGPUMemoryUtil, "fraction", nil, []spec.RecommendationEvidence{evidence}, []string{
		"Lower gpu_memory_utilization: allocations outside the KV pool (activations, CUDA graphs, other processes on the GPU) need room, or a request can fail with CUDA out of memory.",
	})
}

// idleGPU is V9: the GPU is mostly idle at the SLO-max stage, so the limit is
// outside it (router concurrency upstream, client validity, configuration).
func idleGPU(in Input, stages []StageView) *spec.Recommendation {
	if in.SLOMaxIndex == nil {
		return nil
	}
	for _, stage := range stages {
		if stage.Index != *in.SLOMaxIndex || !stage.HasGPUActivity || stage.GPUActivity >= IdleGPUCeiling {
			continue
		}
		recommendation := in.recommendation(RuleGPUIdle, "gpu", "", nil, []spec.RecommendationEvidence{
			stageEvidence(stage, "%s averaged %.0f%% at the SLO-max stage (%.1f RPS)", gpuSource(stage), 100*stage.GPUActivity, stage.AchievedRPS),
		}, []string{
			"The bottleneck is outside the GPU: check router or proxy concurrency in front of vLLM, the client's validity, and the network path. This flags a configuration error, not a vLLM tuning issue.",
		})
		recommendation.Subject = spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindAdvice, Name: "gpu_underutilized"}
		return recommendation
	}
	return nil
}

// histogramResolution is V10: a server-side queue or prefill p95 inside the
// first histogram bucket is interpolated, so it is not a measurement.
func histogramResolution(in Input, stages []StageView) *spec.Recommendation {
	for _, stage := range stages {
		var parts []string
		if stage.HasQueueP95 && stage.QueueP95 < HistogramFirstBucket {
			parts = append(parts, fmt.Sprintf("queue time p95 %s s", derive.FormatNumber(stage.QueueP95)))
		}
		if stage.HasPrefillP95 && stage.PrefillP95 < HistogramFirstBucket {
			parts = append(parts, fmt.Sprintf("prefill time p95 %s s", derive.FormatNumber(stage.PrefillP95)))
		}
		if len(parts) == 0 {
			continue
		}
		recommendation := in.recommendation(RuleHistogramRes, "histogram_buckets", "", nil, []spec.RecommendationEvidence{
			stageEvidence(stage, "%s, inside the first %s s bucket of vLLM's histogram", strings.Join(parts, " and "), derive.FormatNumber(HistogramFirstBucket)),
		}, []string{
			"Server-side queue and prefill quantiles below 0.3 s are interpolated inside one bucket. Set --custom-histogram-buckets on benchmark targets for finer resolution (it adds series); the client's TTFT is unaffected.",
		})
		recommendation.Subject = spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindAdvice, Name: "histogram_buckets"}
		return recommendation
	}
	return nil
}

// formatBytes writes a byte count in GiB.
func formatBytes(bytes float64) string {
	const bytesPerGiB = 1073741824 // 1024 x 1024 x 1024
	return derive.FormatNumber(math.Round(100*bytes/bytesPerGiB)/100) + " GiB"
}

func atOrBelowKnee(stages []StageView, knee *int) []StageView {
	if knee == nil {
		return stages
	}
	var kept []StageView
	for _, stage := range stages {
		if stage.Index <= *knee {
			kept = append(kept, stage)
		}
	}
	return kept
}

func knob(in Input, name string) (float64, bool) {
	value, ok := in.Knobs[name].(float64)
	return value, ok
}

func (in Input) recommendation(rule, knobName, unit string, recommended spec.RecommendationValue, evidence []spec.RecommendationEvidence, caveats []string) *spec.Recommendation {
	recommendation := &spec.Recommendation{
		Id: rule + "/" + string(in.Target.Role), Rule: rule, Target: in.Target,
		Subject:     spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindKnob, Name: knobName},
		Recommended: recommended, Tier: spec.RecommendationTierDiagnostic, Confidence: in.Confidence,
		Evidence: evidence, Caveats: append([]string{}, caveats...),
	}
	if current, ok := in.Knobs[knobName]; ok {
		recommendation.Current = current
	}
	if unit != "" {
		recommendation.Unit = &unit
	}
	return recommendation
}

func stageEvidence(stage StageView, format string, args ...any) spec.RecommendationEvidence {
	index := stage.Index
	return spec.RecommendationEvidence{Text: fmt.Sprintf(format, args...), StageIndex: &index}
}

// seqCapBound is V1: the scheduler's sequence cap binds before KV memory.
// It recommends doubling max_num_seqs, at most to the requests the measured
// KV usage could hold at 90% (C_kv), and confirms with a run at that value.
func seqCapBound(in Input, stages []StageView) *spec.Recommendation {
	seqCap, ok := knob(in, target.KnobVLLMMaxNumSeqs)
	if !ok || seqCap <= 0 {
		return nil
	}
	for _, stage := range stages {
		if !stage.HasRunning || !stage.HasWaiting || !stage.HasKV || stage.RunningMax < SeqCapPinnedFraction*seqCap || stage.WaitingMean <= 0 ||
			stage.KVMax >= KVHeadroomCeiling || (stage.HasPreemptions && stage.Preemptions > 0) {
			continue
		}
		ceiling := math.Inf(1)
		if stage.KVMax > 0 {
			ceiling = math.Floor(KVTarget * stage.RunningMax / stage.KVMax)
		}
		recommended := math.Min(SeqCapStep*seqCap, ceiling)
		if recommended <= seqCap {
			return nil
		}
		recommendation := in.recommendation(RuleSeqCapBound, target.KnobVLLMMaxNumSeqs, "sequences", recommended, []spec.RecommendationEvidence{
			stageEvidence(stage, "%.0f requests running against max_num_seqs %s, %.0f waiting on average, KV cache at most %.1f%%, no preemptions",
				stage.RunningMax, derive.FormatNumber(seqCap), stage.WaitingMean, 100*stage.KVMax),
		}, []string{
			"The scheduler cap binds before KV memory: raise max_num_seqs in steps (at most doubling) and confirm each with a run; an experiment over the values is the empirical answer.",
		})
		recommendation.Min = &seqCap
		if !math.IsInf(ceiling, 1) {
			recommendation.Max = &ceiling
			recommendation.Caveats = append(recommendation.Caveats,
				fmt.Sprintf("C_kv %s: the requests the KV cache could hold at %.0f%%, from %.0f running at %.1f%% usage.", derive.FormatNumber(ceiling), 100*KVTarget,
					stage.RunningMax, 100*stage.KVMax))
		}
		return recommendation
	}
	return nil
}

// kvBound is V2: the KV pool is the bottleneck.
func kvBound(in Input, stages []StageView) *spec.Recommendation {
	for _, stage := range stages {
		full := stage.HasKV && stage.KVMax >= KVBoundFloor
		preempted := stage.HasPreemptions && stage.Preemptions > 0
		if !full && !preempted {
			continue
		}
		text := fmt.Sprintf("KV cache reached %.1f%%", 100*stage.KVMax)
		if preempted {
			text = fmt.Sprintf("%s requests were preempted", derive.FormatNumber(stage.Preemptions))
			if stage.HasKV { // without the KV series there is no evidence, never "0%"
				text += fmt.Sprintf(" (KV cache at most %.1f%%)", 100*stage.KVMax)
			}
		}
		return in.recommendation(RuleKVBound, target.KnobVLLMMaxNumSeqs, "sequences", nil, []spec.RecommendationEvidence{stageEvidence(stage, "%s", text)}, []string{
			"The KV pool is the bottleneck: lower max_num_seqs or reduce max_model_len. Which one is an experiment, not a formula.",
			"Raising gpu_memory_utilization helps only where it capped the cache: vLLM 0.34 and later size the KV cache from the memory free after warmup and default to 1.0, so above that point it adds nothing (a tuning session's boot result says which limit applied).",
		})
	}
	return nil
}

// modelLenSlack is V3: max_model_len reserves far more context than any
// request used, which costs KV room for concurrency.
func modelLenSlack(in Input, _ []StageView) *spec.Recommendation {
	length, ok := knob(in, target.KnobVLLMMaxModelLen)
	if !ok || !in.HasTokens || in.TokensP999 <= 0 || in.TokensP999 >= ModelLenSlackFraction*length {
		return nil
	}
	recommended := math.Ceil(ModelLenMargin*in.TokensP999/ModelLenStep) * ModelLenStep
	if recommended >= length {
		return nil
	}
	recommendation := in.recommendation(RuleModelLenSlack, target.KnobVLLMMaxModelLen, "tokens", recommended, []spec.RecommendationEvidence{{
		Text: fmt.Sprintf("p99.9 of prompt plus generation tokens per request was %s, against max_model_len %s",
			derive.FormatNumber(in.TokensP999), derive.FormatNumber(length)),
	}}, []string{
		"Safe only if production requests are no longer than this workload's: a longer request is rejected. Size from production lengths when they differ.",
	})
	recommendation.Max = &length
	return recommendation
}

// failingStage is the first stage whose SLOs did not all hold, or nil.
func failingStage(stages []StageView) (*StageView, map[spec.SloName]spec.SloResult) {
	for i := range stages {
		results := map[spec.SloName]spec.SloResult{}
		failed := false
		for _, result := range stages[i].SLOs {
			results[result.Slo] = result
			failed = failed || !result.IsMet
		}
		if failed {
			return &stages[i], results
		}
	}
	return nil, nil
}

// prefillHeavy is V4: TTFT fails while ITL holds.
func prefillHeavy(in Input, stages []StageView) *spec.Recommendation {
	stage, results := failingStage(stages)
	ttft, hasTTFT := results[spec.SloNameTtftP95]
	itl, hasITL := results[spec.SloNameItlP95]
	if stage == nil || !hasTTFT || !hasITL || ttft.IsMet || !itl.IsMet {
		return nil
	}
	return in.recommendation(RulePrefillHeavy, target.KnobVLLMMaxNumBatchedTokens, "tokens", nil, []spec.RecommendationEvidence{
		stageEvidence(*stage, "TTFT p95 %s s broke its %s s SLO while ITL p95 %s s held its %s s", observed(ttft), derive.FormatNumber(ttft.Threshold), observed(itl), derive.FormatNumber(itl.Threshold)),
	}, []string{
		"Prefill-heavy: a larger max_num_batched_tokens (chunked prefill) improves TTFT at the cost of ITL. Sweep it in an experiment.",
		"First rule out queueing: a TTFT that grows with load at a pinned sequence cap is V1, not prefill.",
	})
}

// decodeHeavy is V5: ITL fails while TTFT holds.
func decodeHeavy(in Input, stages []StageView) *spec.Recommendation {
	stage, results := failingStage(stages)
	ttft, hasTTFT := results[spec.SloNameTtftP95]
	itl, hasITL := results[spec.SloNameItlP95]
	if stage == nil || !hasTTFT || !hasITL || !ttft.IsMet || itl.IsMet {
		return nil
	}
	return in.recommendation(RuleDecodeHeavy, target.KnobVLLMMaxNumBatchedTokens, "tokens", nil, []spec.RecommendationEvidence{
		stageEvidence(*stage, "ITL p95 %s s broke its %s s SLO while TTFT p95 %s s held its %s s", observed(itl), derive.FormatNumber(itl.Threshold), observed(ttft), derive.FormatNumber(ttft.Threshold)),
	}, []string{
		"Decode interference: lower max_num_batched_tokens, or max_num_seqs, to cut the prefill work mixed into each decode step. Sweep it in an experiment.",
	})
}

func observed(result spec.SloResult) string {
	if result.Observed == nil {
		return "n/a"
	}
	return derive.FormatNumber(*result.Observed)
}

// prefixCacheHits is V6. Without a shared prefix in the workload, a low hit
// rate says the workload does not reuse prefixes, so a TTFT win must not be
// credited to prefix caching. With one, the expected rate is about the
// prefix's share of the prompt; far less means caching is off or the KV pool
// evicts the prefixes before they are reused.
func prefixCacheHits(in Input, stages []StageView) *spec.Recommendation {
	var hits, queries float64
	for _, stage := range stages {
		if stage.HasPrefix {
			hits += stage.PrefixHits
			queries += stage.PrefixQueries
		}
	}
	if queries <= 0 {
		return nil
	}
	rate := hits / queries
	observed := spec.RecommendationEvidence{
		Text: fmt.Sprintf("Prefix cache hit rate %.1f%% (%.0f of %.0f prompt tokens) across the usable stages", 100*rate, hits, queries),
	}
	if in.SharedPrefixShare > 0 {
		if rate >= PrefixHitShortfall*in.SharedPrefixShare {
			return nil
		}
		return in.recommendation(RulePrefixCacheHit, target.KnobVLLMEnablePrefixCaching, "", nil, []spec.RecommendationEvidence{
			observed,
			{Text: fmt.Sprintf("The workload's shared prefix is %.1f%% of each prompt, so about that share should hit", 100*in.SharedPrefixShare)},
		}, []string{
			"The workload reuses prefixes but the cache barely serves them: check --enable-prefix-caching is on, and whether KV pressure (V2) evicts the prefixes before they are reused.",
		})
	}
	if rate >= PrefixHitFloor {
		return nil
	}
	return in.recommendation(RulePrefixCacheHit, target.KnobVLLMEnablePrefixCaching, "", nil, []spec.RecommendationEvidence{observed}, []string{
		"This workload does not reuse prefixes, so prefix caching cannot explain a TTFT difference here; check it is on for production traffic that does (shared system prompts, multi-turn).",
	})
}
