package capacity

import (
	"math"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var testStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ladderStage builds a 60s stage with the given achieved RPS, p95 TTFT and
// SLO results (the ttft SLO threshold is 0.5s).
func ladderStage(index int, rps, ttftP95 float64) Stage {
	start := testStart.Add(time.Duration(index) * 60 * time.Second)
	value, low, high := ttftP95, ttftP95*0.99, ttftP95*1.01
	summary := &spec.LatencySummary{P95Seconds: spec.BootstrapInterval{Value: &value, CiLow: &low, CiHigh: &high, Samples: 500}}
	return Stage{
		Window: input.Stage{Index: index, OfferedRPS: rps, Start: start, End: start.Add(60 * time.Second)},
		Aggregate: spec.StageAggregate{
			StageIndex: index, OfferedRps: rps, AchievedRps: rps, Requests: int(rps * 60), Ttft: summary,
			SloResults: []spec.SloResult{{Slo: spec.SloNameTtftP95, Threshold: 0.5, Observed: &value, IsMet: value <= 0.5}},
		},
		MeanE2ESeconds: 2,
	}
}

func build(t *testing.T, stages []Stage, series input.SignalSource) spec.CapacityModel {
	t.Helper()
	if series == nil {
		series = input.NoSignals{}
	}
	model, err := Build(Input{Stages: stages, Series: series, Interval: aggregate.ScrapeInterval{Duration: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func TestSLOMaxInterpolatesBetweenTheValidAndTheFailedStage(t *testing.T) {
	// p95 TTFT 0.30 at 10 RPS and 0.70 at 14 RPS; the 0.5s threshold is
	// crossed half way: 12 RPS.
	model := build(t, []Stage{ladderStage(0, 6, 0.2), ladderStage(1, 10, 0.30), ladderStage(2, 14, 0.70), ladderStage(3, 18, 2)}, nil)
	slo := model.SloMax
	if slo == nil || !slo.IsInterpolated || slo.ValidStageIndex != 1 || *slo.FailedStageIndex != 2 || *slo.LimitingSlo != spec.SloNameTtftP95 {
		t.Fatalf("slo_max = %+v", slo)
	}
	if math.Abs(slo.Rps-12) > 1e-9 {
		t.Errorf("interpolated rps = %v, want 12", slo.Rps)
	}
}

func TestSLOMaxWithoutAFailedStageIsTheTopOfTheLadder(t *testing.T) {
	model := build(t, []Stage{ladderStage(0, 6, 0.2), ladderStage(1, 10, 0.3)}, nil)
	slo := model.SloMax
	if slo == nil || slo.IsInterpolated || slo.FailedStageIndex != nil || slo.Rps != 10 || slo.ValidStageIndex != 1 {
		t.Errorf("slo_max = %+v", slo)
	}
}

func TestSLOMaxIsAbsentWhenTheFirstStageFailsOrNoSLOIsSet(t *testing.T) {
	if model := build(t, []Stage{ladderStage(0, 6, 0.9), ladderStage(1, 10, 0.3)}, nil); model.SloMax != nil {
		t.Errorf("the first stage fails, so no capacity can be claimed: %+v", model.SloMax)
	}
	noSLO := ladderStage(0, 6, 0.1)
	noSLO.Aggregate.SloResults = nil
	if model := build(t, []Stage{noSLO}, nil); model.SloMax != nil {
		t.Errorf("without an SLO there is no SLO-max: %+v", model.SloMax)
	}
	if model := build(t, nil, nil); model.SloMax != nil || model.Knee != nil || model.PerReplica != nil {
		t.Errorf("an empty ladder gives an empty model: %+v", model)
	}
}

func TestSLOMaxStopsAtTheFirstFailureEvenIfALaterStagePasses(t *testing.T) {
	model := build(t, []Stage{ladderStage(0, 6, 0.2), ladderStage(1, 10, 0.9), ladderStage(2, 14, 0.3)}, nil)
	slo := model.SloMax
	if slo == nil || slo.ValidStageIndex != 0 || *slo.FailedStageIndex != 1 {
		t.Errorf("a later pass does not undo a failure below it: %+v", slo)
	}
}

func TestSLOMaxFailedStageIsAlwaysAboveTheValidOne(t *testing.T) {
	model := build(t, []Stage{ladderStage(0, 6, 0.2), ladderStage(1, 10, 0.9)}, nil)
	if *model.SloMax.FailedStageIndex <= model.SloMax.ValidStageIndex {
		t.Errorf("failed %d must be above valid %d", *model.SloMax.FailedStageIndex, model.SloMax.ValidStageIndex)
	}
}

func TestSLOMaxDoesNotInterpolateWithoutObservedValues(t *testing.T) {
	failed := ladderStage(1, 10, 0.9)
	failed.Aggregate.SloResults[0].Observed = nil // too few samples to observe p95
	model := build(t, []Stage{ladderStage(0, 6, 0.2), failed}, nil)
	slo := model.SloMax
	if slo.IsInterpolated || slo.Rps != 6 || *slo.FailedStageIndex != 1 || *slo.LimitingSlo != spec.SloNameTtftP95 {
		t.Errorf("slo_max = %+v", slo)
	}
}

func TestSLOMaxPicksTheEarliestCrossingAmongFailingSLOs(t *testing.T) {
	twoSLOs := func(index int, rps, ttft, e2e float64) Stage {
		stage := ladderStage(index, rps, ttft)
		stage.Aggregate.SloResults = []spec.SloResult{
			{Slo: spec.SloNameTtftP95, Threshold: 0.5, Observed: &ttft, IsMet: ttft <= 0.5},
			{Slo: spec.SloNameE2EP95, Threshold: 5, Observed: &e2e, IsMet: e2e <= 5},
		}
		return stage
	}
	// ttft crosses 0.5 at 25% of the way (0.4 to 0.8); e2e crosses 5 at 50% (4 to 6).
	model := build(t, []Stage{twoSLOs(0, 10, 0.4, 4), twoSLOs(1, 20, 0.8, 6)}, nil)
	slo := model.SloMax
	if !slo.IsInterpolated || *slo.LimitingSlo != spec.SloNameTtftP95 || math.Abs(slo.Rps-12.5) > 1e-9 {
		t.Errorf("slo_max = %+v", slo)
	}
}

func TestSLOMaxRPSNeverExceedsTheFailedStage(t *testing.T) {
	// The failed value is barely above the threshold: the crossing is at the end.
	model := build(t, []Stage{ladderStage(0, 6, 0.2), ladderStage(1, 10, 0.5000001)}, nil)
	if model.SloMax.Rps > 10 || model.SloMax.Rps < 6 {
		t.Errorf("rps %v outside the bracket", model.SloMax.Rps)
	}
}

func TestExcludedStagesNeverContribute(t *testing.T) {
	stages := []Stage{ladderStage(0, 6, 0.2), ladderStage(1, 10, 0.3), ladderStage(2, 14, 0.9), ladderStage(3, 18, 0.3)}
	stages[2].IsExcluded = true // its failure must not bracket the capacity
	model := build(t, stages, nil)
	if model.SloMax.ValidStageIndex != 3 || model.SloMax.FailedStageIndex != nil {
		t.Errorf("an excluded stage is invisible to slo_max: %+v", model.SloMax)
	}
}

func TestKneeFromLatencySlope(t *testing.T) {
	// RPS grows 25% per stage. p95 grows 12.5% (ratio 0.5), then 60% (ratio 2.4).
	model := build(t, []Stage{ladderStage(0, 8, 0.2), ladderStage(1, 10, 0.225), ladderStage(2, 12.5, 0.36)}, nil)
	knee := model.Knee
	if knee == nil || knee.StageIndex != 2 || knee.Reason != spec.KneeReasonLatencySlope || math.Abs(*knee.SlopeRatio-2.4) > 1e-9 {
		t.Errorf("knee = %+v", knee)
	}
}

func TestKneeSlopeRatioMustExceedTwo(t *testing.T) {
	// RPS +25%, p95 +50%: the ratio is exactly 2, which is not above 2.
	model := build(t, []Stage{ladderStage(0, 8, 0.2), ladderStage(1, 10, 0.3)}, nil)
	if model.Knee != nil {
		t.Errorf("a slope ratio of exactly 2 is not a knee: %+v", model.Knee)
	}
}

func TestKneeIgnoresExcludedStagesWhenMeasuringTheSlope(t *testing.T) {
	stages := []Stage{ladderStage(0, 8, 0.2), ladderStage(1, 10, 0.21), ladderStage(2, 12, 5), ladderStage(3, 14, 0.22)}
	stages[2].IsExcluded = true
	if model := build(t, stages, nil); model.Knee != nil {
		t.Errorf("the excluded stage's latency spike must not make a knee: %+v", model.Knee)
	}
}

func series(values ...float64) []input.Sample {
	// Values are per 60s stage, sampled every 5s from the first stage on.
	var samples []input.Sample
	for stage, value := range values {
		for offset := 0; offset < 60; offset += 5 {
			samples = append(samples, input.Sample{At: testStart.Add(time.Duration(stage*60+offset) * time.Second), Value: value})
		}
	}
	return samples
}

func flatStages(count int) []Stage {
	stages := make([]Stage, count)
	for i := range stages {
		stages[i] = ladderStage(i, float64(10+i), 0.2)
	}
	return stages
}

func TestKneeFromSustainedQueueing(t *testing.T) {
	waiting := func(values ...float64) input.MapSignals {
		return input.MapSignals{input.RequestsWaiting: series(values...)}
	}
	if model := build(t, flatStages(3), waiting(0, 0, 2)); model.Knee == nil || model.Knee.StageIndex != 2 || model.Knee.Reason != spec.KneeReasonQueueing {
		t.Errorf("knee = %+v", model.Knee)
	}
	if model := build(t, flatStages(3), waiting(0, 0, 0)); model.Knee != nil {
		t.Errorf("an empty queue is not a knee: %+v", model.Knee)
	}
	// Waiting above zero in only a third of the samples is a blip, not sustained.
	blip := series(0, 0, 0)
	for i := 0; i < 4; i++ {
		blip[24+i].Value = 3
	}
	if model := build(t, flatStages(3), input.MapSignals{input.RequestsWaiting: blip}); model.Knee != nil {
		t.Errorf("a short blip is not sustained queueing: %+v", model.Knee)
	}
}

// queueSignals renders a router's queue-time histogram as cumulative sum and
// count signals: ten requests per second, each queued for the stage's mean.
func queueSignals(means ...float64) input.MapSignals {
	var sums, counts []input.Sample
	var sum, count float64
	for stage, mean := range means {
		for offset := 0; offset < 60; offset += 5 {
			at := testStart.Add(time.Duration(stage*60+offset) * time.Second)
			sums = append(sums, input.Sample{At: at, Value: sum})
			counts = append(counts, input.Sample{At: at, Value: count})
			sum += mean * 50
			count += 50
		}
	}
	return input.MapSignals{input.QueueSecondsSum: sums, input.QueueCount: counts}
}

func TestKneeFromRouterQueueTimeAgainstTheFirstStage(t *testing.T) {
	// Baseline 0.010s, so 3x is 0.030s.
	if model := build(t, flatStages(3), queueSignals(0.010, 0.020, 0.0299)); model.Knee != nil {
		t.Errorf("below 3x the baseline is not a knee: %+v", model.Knee)
	}
	model := build(t, flatStages(3), queueSignals(0.010, 0.020, 0.0301))
	if model.Knee == nil || model.Knee.StageIndex != 2 || model.Knee.Reason != spec.KneeReasonQueueing {
		t.Errorf("knee = %+v", model.Knee)
	}
	// A near-zero baseline does not turn a tiny queue time into a knee.
	if model := build(t, flatStages(2), queueSignals(0, 0.002)); model.Knee != nil {
		t.Errorf("2ms of queueing against a zero baseline: %+v", model.Knee)
	}
}

func TestWithoutAQueueTimeHistogramThereIsNoQueueingEvidence(t *testing.T) {
	// Bifrost has no queue-time series: a run without one reports no knee from it.
	if model := build(t, flatStages(3), input.MapSignals{input.RouterConcurrency: series(0.1, 0.5, 0.9)}); model.Knee != nil {
		t.Errorf("knee = %+v", model.Knee)
	}
}

func TestKneeFromPreemptions(t *testing.T) {
	counter := func(values ...float64) []input.Sample {
		// A cumulative counter that rises by values[i] across stage i.
		var samples []input.Sample
		var total float64
		for stage, increase := range values {
			for offset := 0; offset < 60; offset += 5 {
				samples = append(samples, input.Sample{At: testStart.Add(time.Duration(stage*60+offset) * time.Second), Value: total + increase*float64(offset)/55})
			}
			total += increase
		}
		return samples
	}
	model := build(t, flatStages(3), input.MapSignals{input.Preemptions: counter(0, 0, 7)})
	if model.Knee == nil || model.Knee.StageIndex != 2 || model.Knee.Reason != spec.KneeReasonPreemptions {
		t.Errorf("knee = %+v", model.Knee)
	}
	if model := build(t, flatStages(3), input.MapSignals{input.Preemptions: counter(0, 0, 0)}); model.Knee != nil {
		t.Errorf("a flat counter is no knee: %+v", model.Knee)
	}
}

func TestLowResolutionStagesHaveNoServerSideEvidence(t *testing.T) {
	stages := flatStages(3)
	stages[2].IsLowResolution = true
	model := build(t, stages, input.MapSignals{input.RequestsWaiting: series(0, 0, 9)})
	if model.Knee != nil {
		t.Errorf("a low-resolution stage cannot show queueing: %+v", model.Knee)
	}
}

func kneeAtStageOne(extra input.MapSignals) spec.CapacityModel {
	extra[input.RequestsWaiting] = series(0, 3)
	stages := flatStages(2)
	model, err := Build(Input{Stages: stages, Series: extra, Interval: aggregate.ScrapeInterval{Duration: 5 * time.Second}})
	if err != nil {
		panic(err)
	}
	return model
}

func TestBottleneckIsTheResourceClosestToItsCeiling(t *testing.T) {
	model := kneeAtStageOne(input.MapSignals{
		input.KVCacheUsage:      series(0.2, 0.62),
		input.GPUEngineActive:   series(0.2, 0.81),
		input.TargetCPUUsage:    series(0.1, 0.4),
		input.RouterConcurrency: series(0.1, 0.3),
		input.GPUUtil:           series(0.2, 0.99), // ignored: engine activity exists
	})
	bottleneck := model.Knee.Bottleneck
	if bottleneck == nil || bottleneck.Resource != spec.BottleneckResourceGpuEngineActivity || bottleneck.IsWeakSignal || math.Abs(bottleneck.Utilization-0.81) > 1e-9 {
		t.Errorf("bottleneck = %+v", bottleneck)
	}
}

func TestBottleneckFallsBackToWeakGPUUtilWithoutProfilingMetrics(t *testing.T) {
	model := kneeAtStageOne(input.MapSignals{input.KVCacheUsage: series(0.2, 0.5), input.GPUUtil: series(0.2, 0.95)})
	if b := model.Knee.Bottleneck; b == nil || b.Resource != spec.BottleneckResourceGpuUtil || !b.IsWeakSignal {
		t.Errorf("bottleneck = %+v", b)
	}
	model = kneeAtStageOne(input.MapSignals{input.KVCacheUsage: series(0.2, 0.97), input.GPUUtil: series(0.2, 0.95)})
	if b := model.Knee.Bottleneck; b.Resource != spec.BottleneckResourceKvCache || b.IsWeakSignal {
		t.Errorf("a stronger real signal beats the weak one: %+v", b)
	}
}

func TestBottleneckCPUUsesTheWorseOfUsageAndThrottle(t *testing.T) {
	model := kneeAtStageOne(input.MapSignals{input.TargetCPUUsage: series(0.2, 0.5), input.TargetCPUThrottle: series(0, 0.7)})
	if b := model.Knee.Bottleneck; b.Resource != spec.BottleneckResourceCpu || math.Abs(b.Utilization-0.7) > 1e-9 {
		t.Errorf("bottleneck = %+v", b)
	}
}

func TestBottleneckClampsAndBreaksTiesInCandidateOrder(t *testing.T) {
	model := kneeAtStageOne(input.MapSignals{input.KVCacheUsage: series(0, 1.3), input.GPUEngineActive: series(0, 1)})
	if b := model.Knee.Bottleneck; b.Resource != spec.BottleneckResourceKvCache || b.Utilization != 1 {
		t.Errorf("bottleneck = %+v", b)
	}
}

func TestBottleneckIsAbsentWithoutCandidateSeries(t *testing.T) {
	if model := kneeAtStageOne(input.MapSignals{}); model.Knee == nil || model.Knee.Bottleneck != nil {
		t.Errorf("knee = %+v", model.Knee)
	}
}

func TestBottleneckIsJudgedAtTheKneeStage(t *testing.T) {
	// KV cache is full in stage 0 but low at the knee in stage 1.
	model := kneeAtStageOne(input.MapSignals{input.KVCacheUsage: series(0.99, 0.3), input.GPUEngineActive: series(0.1, 0.6)})
	if b := model.Knee.Bottleneck; b.Resource != spec.BottleneckResourceGpuEngineActivity {
		t.Errorf("only the knee stage counts: %+v", b)
	}
}

func TestPerReplicaAndPerGPU(t *testing.T) {
	slo := &spec.SloMaxRps{Rps: 12}
	got := perReplica(slo, 3, 2)
	if got.ReadyReplicas != 3 || got.RpsPerReplica != 4 || *got.TotalGpus != 6 || *got.RpsPerGpu != 2 {
		t.Errorf("per_replica = %+v", got)
	}
	if got := perReplica(slo, 3, 0); got.TotalGpus != nil || got.RpsPerGpu != nil {
		t.Errorf("no GPUs means no per-GPU figure: %+v", got)
	}
	if perReplica(slo, 0, 1) != nil || perReplica(nil, 2, 1) != nil {
		t.Error("unknown replicas or no slo_max give no per_replica")
	}
}

func TestReadyReplicasIsTheMinimumDuringTheUsableStages(t *testing.T) {
	stages := []input.Stage{{Start: testStart, End: testStart.Add(time.Minute)}, {Start: testStart.Add(time.Minute), End: testStart.Add(2 * time.Minute)}}
	pre := &spec.TargetSnapshot{Workloads: []spec.Workload_2{{Role: spec.WorkloadRoleTarget, Replicas: &spec.Replicas{Desired: 4, Ready: 4}}}}
	post := &spec.TargetSnapshot{Workloads: []spec.Workload_2{{Role: spec.WorkloadRoleTarget, Replicas: &spec.Replicas{Desired: 4, Ready: 3}}}}

	got, err := ReadyReplicas(input.NoSignals{}, stages, pre, post)
	if err != nil || got != 4 {
		t.Errorf("from snapshots = %d, %v; want the pre-run 4, which the usable stages ran on", got, err)
	}
	if got, _ := ReadyReplicas(input.NoSignals{}, stages, nil, post); got != 3 {
		t.Errorf("without a pre-run snapshot the post-run one is used, got %d", got)
	}
	collected := input.MapSignals{input.ReadyPods: {
		{At: testStart.Add(-time.Hour), Value: 1}, // before the run: ignored
		{At: testStart, Value: 4}, {At: testStart.Add(time.Minute), Value: 2}, {At: testStart.Add(2 * time.Minute), Value: 0},
	}}
	if got, _ := ReadyReplicas(collected, stages, pre, post); got != 2 {
		t.Errorf("from the series = %d, want 2 (the sample at the end belongs after the run)", got)
	}
	if got, _ := ReadyReplicas(collected, stages[:1], pre, post); got != 4 {
		t.Errorf("a stage that is left out, such as one after a scale-down, must not lower the count: got %d", got)
	}
	if got, _ := ReadyReplicas(input.NoSignals{}, stages, nil, nil); got != 0 {
		t.Errorf("nothing known = %d, want 0", got)
	}
}

func TestGPUsPerReplica(t *testing.T) {
	gpus := 4
	snapshot := &spec.TargetSnapshot{Workloads: []spec.Workload_2{{Role: spec.WorkloadRoleTarget, HardwareProfile: &spec.HardwareProfile{GpuCountPerPod: &gpus}}}}
	if GPUsPerReplica(snapshot) != 4 || GPUsPerReplica(nil) != 0 {
		t.Error("GPU count must come from the target's hardware profile")
	}
}

func TestASaturatedStageEndsTheLadderAndBracketsTheSLOMax(t *testing.T) {
	// The vLLM baseline: fine to 40 RPS, then the target falls behind at 60
	// and the stage is excluded as target_saturated, its TTFT 30s.
	stages := []Stage{ladderStage(0, 10, 0.05), ladderStage(1, 20, 0.08), ladderStage(2, 40, 0.19), ladderStage(3, 60, 30)}
	stages[3].IsExcluded, stages[3].IsTargetSaturated = true, true
	model := build(t, stages, nil)
	slo := model.SloMax
	if slo == nil || slo.ValidStageIndex != 2 || slo.FailedStageIndex == nil || *slo.FailedStageIndex != 3 {
		t.Fatalf("the saturated stage is where the target failed: %+v", slo)
	}
	if !slo.IsInterpolated || slo.Rps <= 40 || slo.Rps >= 41 {
		t.Errorf("interpolated toward 60 RPS at 30s TTFT, the 0.5s crossing is just above 40: %+v", slo)
	}
	// Excluded for another reason, the same stage stays invisible, and the
	// SLO-max is the top of the measured ladder.
	stages[3].IsTargetSaturated = false
	if slo := build(t, stages, nil).SloMax; slo.FailedStageIndex != nil || slo.Rps != 40 {
		t.Errorf("an excluded stage that is not saturated never brackets: %+v", slo)
	}
}

func TestASaturatedStageNeverExtendsTheLadder(t *testing.T) {
	// Even if its latency looks fine, a stage where the target fell behind
	// did not sustain its rate: it can only fail.
	stages := []Stage{ladderStage(0, 10, 0.05), ladderStage(1, 20, 0.08), ladderStage(2, 40, 0.1)}
	stages[2].IsExcluded, stages[2].IsTargetSaturated = true, true
	slo := build(t, stages, nil).SloMax
	if slo == nil || slo.ValidStageIndex != 1 || slo.FailedStageIndex == nil || *slo.FailedStageIndex != 2 {
		t.Errorf("slo_max = %+v", slo)
	}
}

// underpowered is a stage whose TTFT p95 has too few samples (Rule A): no observed value.
func underpowered(index int, rps float64) Stage {
	stage := ladderStage(index, rps, 0.1)
	stage.Aggregate.Ttft = nil
	stage.Aggregate.SloResults[0].Observed, stage.Aggregate.SloResults[0].IsMet = nil, false
	return stage
}

// The RTX 3070 baseline of 2026-11-24: 1 and 2 RPS had 90 and 180 requests, below the 200 a p95
// needs; 4 to 16 held; 32 failed. The short bottom stages must not hide the SLO-max.
func TestSLOMaxSkipsUnderpoweredStagesAtTheBottom(t *testing.T) {
	model := build(t, []Stage{underpowered(0, 1), underpowered(1, 2), ladderStage(2, 4, 0.07), ladderStage(3, 8, 0.09),
		ladderStage(4, 16, 0.24), ladderStage(5, 32, 22.8)}, nil)
	slo := model.SloMax
	if slo == nil || slo.ValidStageIndex != 4 || *slo.FailedStageIndex != 5 {
		t.Fatalf("slo_max = %+v", slo)
	}
}

func TestSLOMaxStillFailsOnTooFewSamplesAfterAPass(t *testing.T) {
	model := build(t, []Stage{ladderStage(0, 4, 0.1), underpowered(1, 8), ladderStage(2, 16, 0.2)}, nil)
	if slo := model.SloMax; slo == nil || slo.ValidStageIndex != 0 || *slo.FailedStageIndex != 1 {
		t.Fatalf("slo_max = %+v", slo)
	}
	if model := build(t, []Stage{underpowered(0, 1), underpowered(1, 2)}, nil); model.SloMax != nil {
		t.Fatalf("no stage was judged, so there is no SLO-max: %+v", model.SloMax)
	}
	// A stage that misses an SLO it did observe is a real failure, even at the bottom.
	if model := build(t, []Stage{ladderStage(0, 1, 0.9), ladderStage(1, 4, 0.1)}, nil); model.SloMax != nil {
		t.Fatalf("an observed failure at the bottom ends the scan: %+v", model.SloMax)
	}
}
