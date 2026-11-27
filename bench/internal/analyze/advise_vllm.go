package analyze

import (
	"fmt"
	"net/http"

	"github.com/servtuner/servtuner/bench/internal/analyze/advisors/vllm"
	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/histogram"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/signals"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// adviseVLLM runs the vLLM advisor (V1 to V6) when the
// target is vLLM, adding its recommendations to the sizing's.
func adviseVLLM(run input.Run, analysis spec.AnalysisResult, stages []input.Stage, interval aggregate.ScrapeInterval, outputs *sizing.Outputs) error {
	if run.Spec.Target.Type != spec.TargetTypeVllm {
		return nil
	}
	matrix := run.Matrix
	if matrix == nil {
		matrix = input.NoMatrix{}
	}
	series := signals.New(signals.Config{
		Matrix: matrix, BenchPod: run.BenchPod, ShardPods: run.ShardPods, TargetPods: signals.PodsOf(run.PreSnapshot, run.PostSnapshot),
		ReplicaPods: signals.ReplicaPodsOf(run.PreSnapshot, run.PostSnapshot),
	})
	in := vllm.Input{
		Target: vllmTarget(run, outputs.Usage), Knobs: effectiveKnobs(run), Confidence: spec.RecommendationConfidenceMedium,
		SharedPrefixShare: sharedPrefixShare(run.Spec.Workload.Dataset),
	}
	if knee := analysis.Capacity.Knee; knee != nil {
		index := knee.StageIndex
		in.KneeIndex = &index
	}
	if sloMax := analysis.Capacity.SloMax; sloMax != nil {
		index := sloMax.ValidStageIndex
		in.SLOMaxIndex = &index
	}
	histograms, err := vllmHistograms(run)
	if err != nil {
		return err
	}
	for i, stage := range stages {
		quality := analysis.Quality.Stages[i]
		if !quality.IsValid || quality.IsExcluded {
			continue
		}
		view, err := vllmStageView(series, histograms, stage, analysis.Stages[i], interval)
		if err != nil {
			return fmt.Errorf("vllm advisor, stage %d: %w", stage.Index, err)
		}
		in.Stages = append(in.Stages, view)
	}
	p999, ok, err := tokensP999(run.Records)
	if err != nil {
		return err
	}
	in.TokensP999, in.HasTokens = p999, ok
	outputs.Recommendations.Recommendations = append(outputs.Recommendations.Recommendations, vllm.Advise(in)...)
	return nil
}

// sharedPrefixShare is the share of prompt tokens a shared prefix makes
// cacheable, or 0 without one. input_tokens is the whole prompt.
func sharedPrefixShare(dataset *spec.Dataset) float64 {
	if dataset == nil || dataset.SharedPrefix == nil || dataset.InputTokens.Mean <= 0 {
		return 0
	}
	return float64(dataset.SharedPrefix.Tokens) / dataset.InputTokens.Mean
}

// vllmTarget is the target workload as sizing found it or, for a vLLM outside
// Kubernetes (no snapshot, so nothing was sized), the target role in the
// namespace target.metrics labels it with.
func vllmTarget(run input.Run, usage []sizing.ContainerUsage) spec.RecommendationTarget {
	for _, container := range usage {
		if container.Target.Role == spec.WorkloadRoleTarget {
			workload := container.Target
			workload.Container = nil
			return workload
		}
	}
	namespace := offClusterNamespace
	if metrics := run.Spec.Target.Metrics; metrics != nil {
		namespace = metrics.Namespace // the identity its series carry
	}
	return spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: run.Spec.Target.Type, Namespace: namespace}
}

// offClusterNamespace names the namespace of a target outside Kubernetes whose
// RunSpec gives none (no workload, no target.metrics); a recommendation's
// target always names one.
const offClusterNamespace = "external"

// effectiveKnobs are the captured knobs, with a declared value where nothing
// was captured (vLLM exposes no max_num_seqs outside dev mode).
func effectiveKnobs(run input.Run) map[string]spec.KnobValue {
	knobs := map[string]spec.KnobValue{}
	for name, value := range run.Spec.Knobs {
		knobs[name] = value
	}
	for name, value := range run.CapturedKnobs {
		knobs[name] = value
	}
	return knobs
}

// vllmPhaseHistograms are the target's server-side queue and prefill
// histograms, for V10's resolution check.
type vllmPhaseHistograms struct{ queue, prefill []input.Series }

var (
	vllmQueueHistogram   = input.SeriesRef{Catalog: spec.CatalogNameVllm, Name: "queue_time_seconds"}
	vllmPrefillHistogram = input.SeriesRef{Catalog: spec.CatalogNameVllm, Name: "prefill_time_seconds"}
)

func vllmHistograms(run input.Run) (vllmPhaseHistograms, error) {
	var histograms vllmPhaseHistograms
	if run.Matrix == nil {
		return histograms, nil
	}
	for ref, into := range map[input.SeriesRef]*[]input.Series{vllmQueueHistogram: &histograms.queue, vllmPrefillHistogram: &histograms.prefill} {
		series, _, err := run.Matrix.Matrix(ref)
		if err != nil {
			return histograms, fmt.Errorf("read %s/%s: %w", ref.Catalog, ref.Name, err)
		}
		*into = series
	}
	return histograms, nil
}

func vllmStageView(series input.SignalSource, histograms vllmPhaseHistograms, stage input.Stage, aggregated spec.StageAggregate, interval aggregate.ScrapeInterval) (vllm.StageView, error) {
	view := vllm.StageView{Index: stage.Index, AchievedRPS: aggregated.AchievedRps, SLOs: aggregated.SloResults, Requests: aggregated.Requests}
	rejected := aggregate.HTTPErrorClass(http.StatusServiceUnavailable)
	for _, class := range aggregated.ErrorsByClass {
		if class.ErrorClass == rejected {
			view.Rejected = class.Count
		}
	}
	start, end, ok := aggregate.TrimWindow(stage, aggregate.Gauge, interval.Duration, 0)
	if !ok {
		return view, nil
	}
	view.QueueP95, view.HasQueueP95 = histogram.Quantile(histograms.queue, 0.95, start, end)
	view.PrefillP95, view.HasPrefillP95 = histogram.Quantile(histograms.prefill, 0.95, start, end)
	gauge := func(signal input.Signal) (aggregate.WindowStats, bool, error) {
		samples, found, err := series.Signal(signal)
		if err != nil || !found {
			return aggregate.WindowStats{}, false, err
		}
		windowStats, ok := aggregate.StatsInWindow(samples, start, end)
		return windowStats, ok, nil
	}
	counter := func(signal input.Signal) (float64, bool, error) {
		samples, found, err := series.Signal(signal)
		if err != nil || !found {
			return 0, false, err
		}
		increase, ok := aggregate.CounterIncrease(samples, start, end)
		return increase, ok, nil
	}
	running, hasRunning, err := gauge(input.RequestsRunning)
	if err != nil {
		return view, err
	}
	view.RunningMax, view.HasRunning = running.Max, hasRunning
	waiting, hasWaiting, err := gauge(input.RequestsWaiting)
	if err != nil {
		return view, err
	}
	view.WaitingMean, view.HasWaiting = waiting.Mean, hasWaiting
	kv, hasKV, err := gauge(input.KVCacheUsage)
	if err != nil {
		return view, err
	}
	view.KVMax, view.HasKV = kv.Max, hasKV
	if view.Preemptions, view.HasPreemptions, err = counter(input.Preemptions); err != nil {
		return view, err
	}
	hits, hasHits, err := counter(input.PrefixCacheHits)
	if err != nil {
		return view, err
	}
	queries, hasQueries, err := counter(input.PrefixCacheQueries)
	if err != nil {
		return view, err
	}
	view.PrefixHits, view.PrefixQueries, view.HasPrefix = hits, queries, hasHits && hasQueries
	return view, gpuStageView(&view, gauge)
}

// gpuStageView adds the GPU and target-CPU evidence of V7 to V9. Engine
// activity needs profiling metrics; without them, GPU utilization stands in.
func gpuStageView(view *vllm.StageView, gauge func(input.Signal) (aggregate.WindowStats, bool, error)) error {
	engine, hasEngine, err := gauge(input.GPUEngineActive)
	if err != nil {
		return err
	}
	if hasEngine {
		view.GPUActivity, view.HasGPUActivity = engine.Mean, true
	} else {
		util, hasUtil, err := gauge(input.GPUUtil)
		if err != nil {
			return err
		}
		view.GPUActivity, view.HasGPUActivity, view.GPUActivityIsUtil = util.Mean, hasUtil, hasUtil
	}
	memory, hasMemory, err := gauge(input.GPUMemoryUsedBytes)
	if err != nil {
		return err
	}
	view.GPUMemoryMean, view.GPUMemoryMax, view.HasGPUMemory = memory.Mean, memory.Max, hasMemory
	free, hasFree, err := gauge(input.GPUMemoryFreeFraction)
	if err != nil {
		return err
	}
	view.GPUFreeMin, view.HasGPUFree = free.Min, hasFree
	cpu, hasCPU, err := gauge(input.TargetCPUUsage)
	if err != nil {
		return err
	}
	view.TargetCPUMax, view.HasTargetCPU = cpu.Max, hasCPU
	return nil
}

// tokensP999 is the p99.9 of prompt plus generation tokens per stage request
// with both counts, or false when no request has them.
func tokensP999(records input.RecordSource) (float64, bool, error) {
	if records == nil {
		return 0, false, nil
	}
	var totals []float64
	for row, err := range records.Records() {
		if err != nil {
			return 0, false, fmt.Errorf("read request records: %w", err)
		}
		if row.StageIndex == nil || aggregate.IsWarmupRow(row) || row.InputTokens == nil || row.OutputTokens == nil {
			continue
		}
		totals = append(totals, float64(*row.InputTokens+*row.OutputTokens))
	}
	if len(totals) == 0 {
		return 0, false, nil
	}
	return stats.NearestRank(stats.SortedCopy(totals), 0.999), true, nil
}
