package vllm

import (
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

func input(stages ...StageView) Input {
	return Input{
		Target: spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeVllm},
		Stages: stages, Confidence: spec.RecommendationConfidenceMedium,
		Knobs: map[string]spec.KnobValue{target.KnobVLLMMaxNumSeqs: 64.0, target.KnobVLLMMaxModelLen: 4096.0},
	}
}

func pinned(index int) StageView {
	return StageView{Index: index, HasRunning: true, RunningMax: 64, HasWaiting: true, WaitingMean: 120, HasKV: true, KVMax: 0.07, HasPreemptions: true}
}

func byRule(recommendations []spec.Recommendation, rule string) *spec.Recommendation {
	for i := range recommendations {
		if recommendations[i].Rule == rule {
			return &recommendations[i]
		}
	}
	return nil
}

func met(slo spec.SloName, isMet bool, value float64) spec.SloResult {
	return spec.SloResult{Slo: slo, Threshold: 0.5, Observed: &value, IsMet: isMet}
}

func TestV1RaisesAPinnedSequenceCapTowardTheKVCeiling(t *testing.T) {
	recommendation := byRule(Advise(input(pinned(0))), RuleSeqCapBound)
	if recommendation == nil {
		t.Fatal("V1 must fire: running pinned at 64 with requests waiting and the KV cache at 7%")
	}
	if recommendation.Recommended != 128.0 || *recommendation.Min != 64 || *recommendation.Max != 822 {
		t.Errorf("recommended %v (min %v, max %v), want a doubling to 128 within C_kv floor(0.9 × 64 / 0.07) = 822",
			recommendation.Recommended, *recommendation.Min, *recommendation.Max)
	}
	nearlyFull := pinned(0)
	nearlyFull.KVMax = 0.6 // C_kv = floor(0.9 × 64 / 0.6) = 96, short of a doubling
	if got := byRule(Advise(input(nearlyFull)), RuleSeqCapBound); got == nil || got.Recommended != 96.0 {
		t.Errorf("C_kv bounds the step: %+v", got)
	}
}

func TestV1NeedsThePinnedCapWaitingRoomAndNoPreemption(t *testing.T) {
	for name, mutate := range map[string]func(*StageView){
		"below the cap":   func(s *StageView) { s.RunningMax = 50 },
		"nothing waiting": func(s *StageView) { s.WaitingMean = 0 },
		"KV at 80%":       func(s *StageView) { s.KVMax = 0.8 },
		"preemptions":     func(s *StageView) { s.Preemptions = 3 },
		"no KV series":    func(s *StageView) { s.HasKV = false },
	} {
		stage := pinned(0)
		mutate(&stage)
		if byRule(Advise(input(stage)), RuleSeqCapBound) != nil {
			t.Errorf("%s: V1 must not fire", name)
		}
	}
}

func TestRulesLookOnlyAtOrBelowTheKnee(t *testing.T) {
	in := input(StageView{Index: 0, HasRunning: true, RunningMax: 10, HasWaiting: true, HasKV: true, KVMax: 0.02}, pinned(1))
	knee := 0
	in.KneeIndex = &knee
	if byRule(Advise(in), RuleSeqCapBound) != nil {
		t.Error("a pinned cap past the knee is overload, not a tuning signal")
	}
}

func TestV2OnAFullKVCacheOrPreemptions(t *testing.T) {
	full := StageView{Index: 0, HasKV: true, KVMax: 0.97}
	if got := byRule(Advise(input(full)), RuleKVBound); got == nil || got.Recommended != nil {
		t.Errorf("V2 is advice without a number: %+v", got)
	}
	preempted := StageView{Index: 0, HasKV: true, KVMax: 0.5, HasPreemptions: true, Preemptions: 4}
	if got := byRule(Advise(input(preempted)), RuleKVBound); got == nil || !strings.Contains(got.Evidence[0].Text, "4 requests were preempted") {
		t.Errorf("preemptions: %+v", got)
	}
	noKV := StageView{Index: 0, HasPreemptions: true, Preemptions: 2}
	if got := byRule(Advise(input(noKV)), RuleKVBound); got == nil || strings.Contains(got.Evidence[0].Text, "KV") {
		t.Errorf("without a KV series the evidence must not quote a KV figure: %+v", got)
	}
	if byRule(Advise(input(StageView{Index: 0, HasKV: true, KVMax: 0.9})), RuleKVBound) != nil {
		t.Error("90% KV without preemptions is not KV-bound")
	}
}

func TestV3TrimsASlackContextLength(t *testing.T) {
	in := input()
	in.TokensP999, in.HasTokens = 490, true
	got := byRule(Advise(in), RuleModelLenSlack)
	if got == nil || got.Recommended != 768.0 || *got.Max != 4096 {
		t.Fatalf("1.2 × 490 = 588, rounded up to 768: %+v", got)
	}
	in.TokensP999 = 2500 // above half of 4096
	if byRule(Advise(in), RuleModelLenSlack) != nil {
		t.Error("no slack worth trimming")
	}
}

func TestV4AndV5SplitTTFTFromITL(t *testing.T) {
	prefill := StageView{Index: 2, SLOs: []spec.SloResult{met(spec.SloNameTtftP95, false, 6.8), met(spec.SloNameItlP95, true, 0.03)}}
	recommendations := Advise(input(StageView{Index: 1, SLOs: []spec.SloResult{met(spec.SloNameTtftP95, true, 0.1), met(spec.SloNameItlP95, true, 0.02)}}, prefill))
	if v4 := byRule(recommendations, RulePrefillHeavy); v4 == nil || *v4.Evidence[0].StageIndex != 2 || byRule(recommendations, RuleDecodeHeavy) != nil {
		t.Errorf("TTFT failing with ITL fine is V4 at stage 2: %+v", recommendations)
	}
	decode := StageView{Index: 2, SLOs: []spec.SloResult{met(spec.SloNameTtftP95, true, 0.2), met(spec.SloNameItlP95, false, 0.09)}}
	if recommendations := Advise(input(decode)); byRule(recommendations, RuleDecodeHeavy) == nil || byRule(recommendations, RulePrefillHeavy) != nil {
		t.Errorf("ITL failing with TTFT fine is V5: %+v", recommendations)
	}
}

func TestV6OnALowPrefixHitRate(t *testing.T) {
	low := StageView{Index: 0, HasPrefix: true, PrefixHits: 50, PrefixQueries: 10000}
	if byRule(Advise(input(low)), RulePrefixCacheHit) == nil {
		t.Error("0.5% hit rate: the workload does not reuse prefixes")
	}
	high := StageView{Index: 0, HasPrefix: true, PrefixHits: 4000, PrefixQueries: 10000}
	if byRule(Advise(input(high)), RulePrefixCacheHit) != nil {
		t.Error("a 40% hit rate is reuse")
	}
}

// TestV6WithASharedPrefixWorkload: a workload whose prompts are 75% shared
// prefix should hit about that much; 20% means the cache is off or evicting,
// and the advice says so rather than "the workload does not reuse prefixes".
func TestV6WithASharedPrefixWorkload(t *testing.T) {
	sharedInput := func(view StageView) Input {
		in := input(view)
		in.SharedPrefixShare = 0.75
		return in
	}
	short := byRule(Advise(sharedInput(StageView{Index: 0, HasPrefix: true, PrefixHits: 2000, PrefixQueries: 10000})), RulePrefixCacheHit)
	if short == nil || len(short.Evidence) != 2 || !strings.Contains(short.Evidence[1].Text, "75.0%") {
		t.Fatalf("20%% against an expected 75%% fires V6 with both numbers: %+v", short)
	}
	if strings.Contains(strings.Join(short.Caveats, " "), "does not reuse") {
		t.Error("a shared-prefix workload does reuse prefixes")
	}
	if byRule(Advise(sharedInput(StageView{Index: 0, HasPrefix: true, PrefixHits: 6500, PrefixQueries: 10000})), RulePrefixCacheHit) != nil {
		t.Error("65% against an expected 75% is the cache working")
	}
}

func TestAPinnedSequenceCapExplainsTTFTSoV4StaysSilent(t *testing.T) {
	stage := pinned(1)
	stage.SLOs = []spec.SloResult{met(spec.SloNameTtftP95, false, 4.6), met(spec.SloNameItlP95, true, 0.03)}
	recommendations := Advise(input(stage))
	if byRule(recommendations, RuleSeqCapBound) == nil || byRule(recommendations, RulePrefillHeavy) != nil {
		t.Errorf("queueing behind the cap is V1's cause, not prefill: %+v", recommendations)
	}
}

// healthy is a stage with every V7 to V10 signal present and none firing.
func healthy(index int) StageView {
	const gib = 1073741824 // bytes per GiB
	return StageView{
		Index: index, AchievedRPS: 30, HasWaiting: true, WaitingMean: 0.2,
		HasGPUActivity: true, GPUActivity: 0.8, HasGPUMemory: true, GPUMemoryMean: 6 * gib, GPUMemoryMax: 6.1 * gib,
		HasGPUFree: true, GPUFreeMin: 0.2, HasTargetCPU: true, TargetCPUMax: 0.5,
		HasQueueP95: true, QueueP95: 0.5, HasPrefillP95: true, PrefillP95: 0.4,
	}
}

func TestV7FiresWhenTheFrontendCPUIsFullAndTheGPUIsNot(t *testing.T) {
	stage := healthy(0)
	stage.TargetCPUMax, stage.GPUActivity = 0.95, 0.4
	recommendation := byRule(Advise(input(stage)), RuleFrontendBound)
	if recommendation == nil || recommendation.Subject.Name != target.KnobVLLMAPIServerCount {
		t.Fatalf("V7 = %+v", recommendation)
	}
	queued := stage
	queued.WaitingMean = 5 // requests waiting: the engine, not the frontend, is behind
	if byRule(Advise(input(queued)), RuleFrontendBound) != nil {
		t.Error("V7 needs waiting ≈ 0")
	}
}

func TestV8FiresOnFramebufferGrowthOrAFullFramebuffer(t *testing.T) {
	first, later := healthy(0), healthy(1)
	later.GPUMemoryMax = first.GPUMemoryMean * 1.08
	recommendation := byRule(Advise(input(first, later)), RuleGPUMemory)
	if recommendation == nil || recommendation.Subject.Name != target.KnobVLLMGPUMemoryUtil || !strings.Contains(recommendation.Evidence[0].Text, "8.0% above") {
		t.Fatalf("growth: %+v", recommendation)
	}
	full := healthy(0)
	full.GPUFreeMin = 0.01
	if byRule(Advise(input(full)), RuleGPUMemory) == nil {
		t.Error("V8 must fire below 2% free framebuffer")
	}
	if byRule(Advise(input(healthy(0), healthy(1))), RuleGPUMemory) != nil {
		t.Error("a steady plateau with room is no V8")
	}
}

func TestV9FlagsAnIdleGPUAtSLOMaxUnlessAVLLMLimitExplainsIt(t *testing.T) {
	idle := healthy(1)
	idle.GPUActivity, idle.GPUActivityIsUtil = 0.3, true
	in := input(healthy(0), idle)
	sloMax := 1
	in.SLOMaxIndex = &sloMax
	recommendation := byRule(Advise(in), RuleGPUIdle)
	if recommendation == nil || recommendation.Subject.Kind != spec.RecommendationSubjectKindAdvice ||
		!strings.Contains(recommendation.Evidence[0].Text, "GPU utilization (no profiling metrics") {
		t.Fatalf("V9 = %+v", recommendation)
	}
	bound := idle
	bound.HasRunning, bound.RunningMax, bound.WaitingMean, bound.HasKV, bound.KVMax = true, 64, 50, true, 0.1
	in = input(healthy(0), bound)
	in.SLOMaxIndex = &sloMax
	if recommendations := Advise(in); byRule(recommendations, RuleSeqCapBound) == nil || byRule(recommendations, RuleGPUIdle) != nil {
		t.Error("V1 explains the idle GPU, so V9 stays silent")
	}
}

func TestV10FlagsQuantilesInsideTheFirstBucket(t *testing.T) {
	fast := healthy(0)
	fast.QueueP95 = 0.05
	recommendation := byRule(Advise(input(fast)), RuleHistogramRes)
	if recommendation == nil || !strings.Contains(recommendation.Evidence[0].Text, "queue time p95 0.05 s") {
		t.Fatalf("V10 = %+v", recommendation)
	}
	if byRule(Advise(input(healthy(0))), RuleHistogramRes) != nil {
		t.Error("p95s above the first bucket are measured, not interpolated")
	}
}

func TestV11ExplainsHTTP503sAsLoadSheddingWhenAQueueLimitIsSet(t *testing.T) {
	calm, shedding := healthy(0), healthy(1)
	shedding.Requests, shedding.Rejected = 1000, 50
	limited := input(calm, shedding)
	limited.Knobs[target.KnobVLLMMaxNumQueuedTokens] = 64000.0
	recommendation := byRule(Advise(limited), RuleLoadShedding)
	if recommendation == nil || recommendation.Subject.Name != target.KnobVLLMMaxNumQueuedTokens ||
		len(recommendation.Evidence) != 1 || !strings.Contains(recommendation.Evidence[0].Text, "50 of 1000 requests (5.0%)") ||
		!strings.Contains(recommendation.Caveats[0], "max_num_queued_tokens=64000") {
		t.Fatalf("V11 = %+v", recommendation)
	}
	if byRule(Advise(input(calm, shedding)), RuleLoadShedding) != nil {
		t.Error("without a queue limit a 503 is a fault, not shedding")
	}
	quiet := input(calm)
	quiet.Knobs[target.KnobVLLMMaxNumQueuedReqs] = 512.0
	if byRule(Advise(quiet), RuleLoadShedding) != nil {
		t.Error("V11 needs rejected requests")
	}
}

func TestV12FlagsCostlyLoggingFromTheKnobsAlone(t *testing.T) {
	logged := input()
	logged.Knobs[target.KnobVLLMEnableLogRequests] = true
	logged.Knobs[target.KnobVLLMDisableLogStats] = true
	logged.Knobs[target.KnobVLLMIterationDetails] = false
	recommendation := byRule(Advise(logged), RuleLogging)
	if recommendation == nil || recommendation.Subject.Kind != spec.RecommendationSubjectKindAdvice || len(recommendation.Evidence) != 2 ||
		!strings.Contains(recommendation.Evidence[1].Text, "Prometheus metrics included") {
		t.Fatalf("V12 = %+v", recommendation)
	}
	if byRule(Advise(input()), RuleLogging) != nil {
		t.Error("V12 needs a costly logging setting that is on")
	}
}
