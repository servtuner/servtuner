package analyzetest

import (
	"math"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// BenchPod is the bench pod's name; TargetPods are the target's. The raw
// entries carry both, as the shared kubernetes catalog does, plus a pod that
// belongs to neither, so a check that picked the wrong pods would be caught.
const BenchPod = "bench-0"

var (
	// TargetPods are the pods of the synthetic target.
	TargetPods = []string{"vllm-0", "vllm-1"}
	// strangerPod is not part of the run.
	strangerPod = "other-0"
)

func ref(catalog spec.CatalogName, name string) input.SeriesRef {
	return input.SeriesRef{Catalog: catalog, Name: name}
}

func capturedKnobs(runSpec spec.RunSpec, snapshot *spec.TargetSnapshot) (map[string]spec.KnobValue, error) {
	adapter, err := target.AdapterFor(runSpec.Target.Type)
	if err != nil {
		return nil, err
	}
	return adapter.KnobsFromWorkload(snapshot.Workloads[0]), nil
}

// rawEntry is one catalog entry being built: its series by label set.
type rawEntry struct {
	series []input.Series
}

func (e *rawEntry) add(labels map[string]string, samples []input.Sample) {
	e.series = append(e.series, input.Series{Labels: labels, Samples: samples})
}

func podLabels(pod, container string) map[string]string {
	return map[string]string{"namespace": "servtuner", "pod": pod, "container": container}
}

// signalValue is the value of a signal's per-stage list at a time.
func (s Scenario) signalValue(signal input.Signal, at time.Time) float64 {
	values := s.Signals[signal]
	if len(values) == 0 {
		return 0
	}
	return s.valueAt(values, at, signal == input.Preemptions)
}

func (s Scenario) hasSignal(signal input.Signal) bool { _, ok := s.Signals[signal]; return ok }

// timeline is the sample times of every entry: every seriesStep from before
// the first stage to after the last.
func (s Scenario) timeline() []time.Time {
	runStart := StartTime.Add(-30 * time.Second)
	runEnd := s.stageWindow(len(s.Stages) - 1).End.Add(seriesStep)
	var times []time.Time
	for at := runStart; !at.After(runEnd); at = at.Add(seriesStep) {
		times = append(times, at)
	}
	return times
}

func (s Scenario) sampled(value func(at time.Time) float64) []input.Sample {
	var samples []input.Sample
	for _, at := range s.timeline() {
		samples = append(samples, input.Sample{At: at, Value: round(value(at))})
	}
	return samples
}

func round(value float64) float64 { return math.Round(value*1e6) / 1e6 }

func constant(value float64) func(time.Time) float64 { return func(time.Time) float64 { return value } }

// Entries renders the scenario's signals as the real catalog entries they are
// derived from. Replicated signals are split over two target pods the way the
// derivation must recombine them: totals summed, per-pod peaks maxed.
func (s Scenario) Entries() map[input.SeriesRef][]input.Series {
	entries := map[input.SeriesRef]*rawEntry{}
	entry := func(r input.SeriesRef) *rawEntry {
		if entries[r] == nil {
			entries[r] = &rawEntry{}
		}
		return entries[r]
	}
	per := func(signal input.Signal, fraction float64) func(time.Time) float64 {
		return func(at time.Time) float64 { return s.signalValue(signal, at) * fraction }
	}

	cpuEntries := s.hasSignal(input.BenchCPUUsage) || s.hasSignal(input.TargetCPUUsage)
	if cpuEntries {
		const benchLimit, targetLimit = 2.0, 4.0
		entry(ref(spec.CatalogNameKubernetes, "cpu_cores")).add(podLabels(BenchPod, "bench"), s.sampled(func(at time.Time) float64 { return s.signalValue(input.BenchCPUUsage, at) * benchLimit }))
		entry(ref(spec.CatalogNameKubernetes, "cpu_limit_cores")).add(podLabels(BenchPod, "bench"), s.sampled(constant(benchLimit)))
		targetUsage := func(fraction float64) func(time.Time) float64 {
			return func(at time.Time) float64 {
				if !s.hasSignal(input.TargetCPUUsage) {
					return 0.1 * targetLimit * fraction
				}
				return s.signalValue(input.TargetCPUUsage, at) * targetLimit * fraction
			}
		}
		for i, pod := range TargetPods {
			if s.Bifrost != nil {
				break // the Bifrost target renders its own pods
			}
			entry(ref(spec.CatalogNameKubernetes, "cpu_cores")).add(podLabels(pod, "vllm"), s.sampled(targetUsage(1/float64(i+1))))
			entry(ref(spec.CatalogNameKubernetes, "cpu_limit_cores")).add(podLabels(pod, "vllm"), s.sampled(constant(targetLimit)))
		}
		// The stranger would dominate a max across all pods.
		entry(ref(spec.CatalogNameKubernetes, "cpu_cores")).add(podLabels(strangerPod, "app"), s.sampled(constant(99)))
		entry(ref(spec.CatalogNameKubernetes, "cpu_limit_cores")).add(podLabels(strangerPod, "app"), s.sampled(constant(100)))
	}
	if s.hasSignal(input.BenchCPUThrottle) || s.hasSignal(input.TargetCPUThrottle) {
		throttle := ref(spec.CatalogNameKubernetes, "cpu_throttle_ratio")
		entry(throttle).add(podLabels(BenchPod, "bench"), s.sampled(per(input.BenchCPUThrottle, 1)))
		entry(throttle).add(podLabels(TargetPods[0], "vllm"), s.sampled(per(input.TargetCPUThrottle, 1)))
		entry(throttle).add(podLabels(TargetPods[1], "vllm"), s.sampled(per(input.TargetCPUThrottle, 0.5)))
		entry(throttle).add(podLabels(strangerPod, "app"), s.sampled(constant(1)))
	}
	if s.hasSignal(input.ReadyPods) {
		// Pod i is ready while more than i pods are; the bench pod is always ready
		// and must not be counted as a replica.
		for i, pod := range TargetPods {
			entry(ref(spec.CatalogNameKubernetes, "pod_ready")).add(podLabels(pod, "vllm"), s.sampled(func(at time.Time) float64 {
				if s.signalValue(input.ReadyPods, at) > float64(i) {
					return 1
				}
				return 0
			}))
		}
		entry(ref(spec.CatalogNameKubernetes, "pod_ready")).add(podLabels(BenchPod, "bench"), s.sampled(constant(1)))
	}
	if s.hasSignal(input.Restarts) {
		entry(ref(spec.CatalogNameKubernetes, "restarts")).add(podLabels(TargetPods[0], "vllm"), s.sampled(per(input.Restarts, 1)))
		entry(ref(spec.CatalogNameKubernetes, "restarts")).add(podLabels(TargetPods[1], "vllm"), s.sampled(constant(0)))
	}
	if s.hasSignal(input.RequestsWaiting) {
		for _, pod := range TargetPods {
			entry(ref(spec.CatalogNameVllm, "num_requests_waiting")).add(map[string]string{"pod": pod}, s.sampled(per(input.RequestsWaiting, 0.5)))
		}
	}
	if s.hasSignal(input.Preemptions) {
		for i, pod := range TargetPods {
			entry(ref(spec.CatalogNameVllm, "num_preemptions_total")).add(map[string]string{"pod": pod}, s.sampled(per(input.Preemptions, 0.25+0.5*float64(i))))
		}
	}
	if s.hasSignal(input.KVCacheUsage) {
		entry(ref(spec.CatalogNameVllm, "kv_cache_usage")).add(map[string]string{"pod": TargetPods[0]}, s.sampled(per(input.KVCacheUsage, 1)))
		entry(ref(spec.CatalogNameVllm, "kv_cache_usage")).add(map[string]string{"pod": TargetPods[1]}, s.sampled(per(input.KVCacheUsage, 0.5)))
	}
	if s.QueueMeanSeconds != nil {
		queue := ref(spec.CatalogNameLitellm, "request_queue_time_seconds")
		count := func(at time.Time) float64 { return at.Sub(StartTime).Seconds() * 10 }
		for _, pod := range TargetPods {
			entry(queue).add(map[string]string{"__name__": "litellm_request_queue_time_seconds_count", "pod": pod}, s.sampled(func(at time.Time) float64 { return count(at) / 2 }))
			entry(queue).add(map[string]string{"__name__": "litellm_request_queue_time_seconds_sum", "pod": pod}, s.sampled(func(at time.Time) float64 { return s.queueSum(at) / 2 }))
		}
	}
	if s.hasSignal(input.GPUEngineActive) {
		gpus := ref(spec.CatalogNameDcgm, "gr_engine_util_ratio")
		entry(gpus).add(map[string]string{"pod": TargetPods[0], "gpu": "0"}, s.sampled(per(input.GPUEngineActive, 1)))
		entry(gpus).add(map[string]string{"pod": TargetPods[0], "gpu": "1"}, s.sampled(per(input.GPUEngineActive, 0.5)))
		entry(gpus).add(map[string]string{"pod": strangerPod, "gpu": "0"}, s.sampled(constant(1)))
	}
	if s.hasSignal(input.GPUUtil) {
		entry(ref(spec.CatalogNameDcgm, "gpu_util_ratio")).add(map[string]string{"pod": TargetPods[0], "gpu": "0"}, s.sampled(per(input.GPUUtil, 1)))
		entry(ref(spec.CatalogNameDcgm, "gpu_util_ratio")).add(map[string]string{"pod": strangerPod, "gpu": "0"}, s.sampled(constant(1)))
	}

	if s.Bifrost != nil {
		s.bifrostEntries(entry)
	}

	result := map[input.SeriesRef][]input.Series{}
	for r, e := range entries {
		result[r] = e.series
	}
	return result
}

// queueSum is the cumulative queue seconds at a time: the integral of the
// per-stage mean times the request rate of 10 per second.
func (s Scenario) queueSum(at time.Time) float64 {
	var total float64
	for i := range s.Stages {
		window := s.stageWindow(i)
		if !at.After(window.Start) {
			break
		}
		overlap := math.Min(at.Sub(window.Start).Seconds(), window.Duration().Seconds())
		total += s.QueueMeanSeconds[min(i, len(s.QueueMeanSeconds)-1)] * overlap * 10
	}
	return total
}

// Matrix returns the scenario's raw entries as a MatrixSource, with the gaps applied.
func (s Scenario) Matrix() input.MatrixSource {
	matrix := input.MapMatrix{}
	runStart := StartTime.Add(-30 * time.Second)
	for r, series := range s.Entries() {
		gap, hasGap := s.Gaps[r]
		for i := range series {
			if !hasGap {
				continue
			}
			var kept []input.Sample
			for _, sample := range series[i].Samples {
				if offset := sample.At.Sub(runStart); offset >= gap[0] && offset < gap[1] {
					continue
				}
				kept = append(kept, sample)
			}
			series[i].Samples = kept
		}
		matrix[r] = series
	}
	return matrix
}

// EntryRefs lists the scenario's rendered entries in a fixed order.
func (s Scenario) EntryRefs() []input.SeriesRef {
	var refs []input.SeriesRef
	for r := range s.Entries() {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Catalog != refs[j].Catalog {
			return refs[i].Catalog < refs[j].Catalog
		}
		return refs[i].Name < refs[j].Name
	})
	return refs
}
