package analyze

import (
	"fmt"
	"math"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/servtuner/servtuner/bench/internal/analyze/charts"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/signals"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// panelValue says how a panel's series follows from its signals.
type panelValue int

const (
	// asIs draws the signal itself (a gauge or a ratio).
	asIs panelValue = iota
	// perSecond draws the increase of a cumulative signal per second.
	perSecond
	// perIncrement draws the increase of the signal over the increase of its
	// denominator in each scrape interval: a histogram's sum over its count is
	// the mean of the observations in that interval, hits over queries a hit rate.
	perIncrement
)

// serverPanels are the derived signals the Server and Resources tabs draw,
// with the definition shown beside each chart. A panel
// whose signals were not collected is left out.
var serverPanels = []struct {
	signal      input.Signal
	denominator input.Signal // perIncrement only
	value       panelValue
	group       spec.PanelGroup
	title       string
	unit        spec.PanelUnit
	definition  string
}{
	{signal: input.RequestsRunning, group: spec.PanelGroupServer, title: "Requests running", unit: spec.PanelUnitRequests, definition: "vLLM num_requests_running or SGLang num_running_reqs, summed across replicas: the requests the scheduler is serving."},
	{signal: input.RequestsWaiting, group: spec.PanelGroupServer, title: "Requests waiting", unit: spec.PanelUnitRequests, definition: "vLLM num_requests_waiting or SGLang num_queue_reqs, summed across replicas: requests queued for a running slot."},
	{signal: input.KVCacheUsage, group: spec.PanelGroupServer, title: "KV cache usage", unit: spec.PanelUnitFraction, definition: "vLLM kv_cache_usage_perc or SGLang token_usage of the fullest replica, 0 to 1."},
	{signal: input.GenerationTokens, value: perSecond, group: spec.PanelGroupServer, title: "Output tokens per second", unit: spec.PanelUnitTokensPerSecond, definition: "The increase of vLLM or SGLang generation_tokens_total per second, summed across replicas."},
	{signal: input.PromptTokens, value: perSecond, group: spec.PanelGroupServer, title: "Input tokens per second", unit: spec.PanelUnitTokensPerSecond, definition: "The increase of vLLM or SGLang prompt_tokens_total per second, summed across replicas: prefill work, including tokens served from the prefix cache."},
	{signal: input.PrefixCacheHits, denominator: input.PrefixCacheQueries, value: perIncrement, group: spec.PanelGroupServer, title: "Prefix cache hit rate", unit: spec.PanelUnitFraction, definition: "vLLM prefix-cache hits over queries, or SGLang cached prompt tokens over prompt tokens, in each scrape interval, 0 to 1. Intervals with no queries are left out."},
	{signal: input.Preemptions, value: perSecond, group: spec.PanelGroupServer, title: "Preemptions per second", unit: spec.PanelUnitPerSecond, definition: "The increase of vLLM num_preemptions_total (or SGLang num_retracted_requests_total) per second: requests evicted to free KV cache, which must be recomputed."},
	{signal: input.VLLMQueueSecondsSum, denominator: input.VLLMQueueCount, value: perIncrement, group: spec.PanelGroupServer, title: "Mean queue time (server)", unit: spec.PanelUnitSeconds, definition: "vLLM request_queue_time_seconds: the mean wait before scheduling of the requests that left the queue in each scrape interval. A mean, not a tail."},
	{signal: input.VLLMPrefillSecondsSum, denominator: input.VLLMPrefillCount, value: perIncrement, group: spec.PanelGroupServer, title: "Mean prefill time (server)", unit: spec.PanelUnitSeconds, definition: "vLLM request_prefill_time_seconds: the mean prefill time of the requests that finished prefill in each scrape interval. A mean, not a tail."},
	{signal: input.VLLMDecodeSecondsSum, denominator: input.VLLMDecodeCount, value: perIncrement, group: spec.PanelGroupServer, title: "Mean decode time (server)", unit: spec.PanelUnitSeconds, definition: "vLLM request_decode_time_seconds: the mean decode time of the requests that finished in each scrape interval. A mean, not a tail."},
	{signal: input.RouterConcurrency, group: spec.PanelGroupServer, title: "Router concurrency in use", unit: spec.PanelUnitFraction, definition: "Bifrost active requests as a fraction of the provider's concurrency knob, busiest replica. Streaming, a slot is held until the first token."},
	{signal: input.GPUEngineActive, group: spec.PanelGroupServer, title: "GPU engine activity", unit: spec.PanelUnitFraction, definition: "DCGM PROF_GR_ENGINE_UTIL_RATIO of the busiest target GPU, 0 to 1."},
	{signal: input.GPUUtil, group: spec.PanelGroupServer, title: "GPU utilization", unit: spec.PanelUnitFraction, definition: "DCGM GPU_UTIL_RATIO of the busiest target GPU, 0 to 1: coarse, a kernel running at all counts as busy."},
	{signal: input.GPUSMActive, group: spec.PanelGroupServer, title: "GPU SM activity", unit: spec.PanelUnitFraction, definition: "DCGM PROF_SM_UTIL_RATIO of the busiest target GPU, 0 to 1: the share of streaming multiprocessors with work. A profiling metric, which consumer GPUs do not export."},
	{signal: input.GPUTensorActive, group: spec.PanelGroupServer, title: "GPU tensor core activity", unit: spec.PanelUnitFraction, definition: "DCGM PROF_TENSOR_UTIL_RATIO of the busiest target GPU, 0 to 1. A profiling metric, which consumer GPUs do not export."},
	{signal: input.GPUMemoryUsedBytes, group: spec.PanelGroupServer, title: "GPU memory used", unit: spec.PanelUnitBytes, definition: "DCGM FB_USED_BYTES of the fullest target GPU, against its capacity. vLLM reserves its KV cache at start, so this is flat by design; KV cache usage shows how much of it is in use."},
	{signal: input.GPUMemoryControllerUtil, group: spec.PanelGroupServer, title: "GPU memory controller utilization", unit: spec.PanelUnitFraction, definition: "DCGM MEMORY_UTIL_RATIO of the busiest target GPU, 0 to 1: the share of time the memory controller was reading or writing. Decode is usually bound by it."},
	{signal: input.GPUPowerWatts, group: spec.PanelGroupServer, title: "GPU power", unit: spec.PanelUnitWatts, definition: "DCGM BOARD_POWER_WATTS of the target GPU drawing the most."},
	{signal: input.GPUSMClockHertz, group: spec.PanelGroupServer, title: "GPU SM clock", unit: spec.PanelUnitHertz, definition: "DCGM SM_CLOCK_HERTZ of the slowest target GPU: a drop under load is power or thermal throttling."},
	{signal: input.ReadyPods, group: spec.PanelGroupServer, title: "Ready target pods", unit: spec.PanelUnitPods, definition: "Ready pods of the target workload (kube-state-metrics), a trailing minimum."},
	{signal: input.TargetCPUUsage, group: spec.PanelGroupResources, title: "Target CPU against its limit or request", unit: spec.PanelUnitFraction, definition: "The busiest target container's CPU as a fraction of its limit, or of its request when it has no limit (so above 100% is normal then)."},
	{signal: input.BenchCPUUsage, group: spec.PanelGroupResources, title: "Bench pod CPU against its limit", unit: spec.PanelUnitFraction, definition: "The load generator's own CPU as a fraction of its limit: near 1, the client may be the bottleneck (client_bound)."},
}

var (
	containerCPU    = input.SeriesRef{Catalog: spec.CatalogNameKubernetes, Name: "cpu_cores"}
	containerMemory = input.SeriesRef{Catalog: spec.CatalogNameKubernetes, Name: "memory_working_set_bytes"}
)

// buildServerChart builds charts/server.json from the run's collected series.
func buildServerChart(run input.Run, windows []input.Stage) (spec.ServerChart, error) {
	if len(windows) == 0 {
		return spec.ServerChart{}, fmt.Errorf("no stages to chart")
	}
	start, end := windows[0].Start, windows[len(windows)-1].End
	width := charts.BucketSeconds(end.Sub(start))
	document := spec.ServerChart{AnalyzerVersion: AnalyzerVersion, Start: spec.NewUTCTimestamp(start), BucketSeconds: width, Panels: []spec.ServerPanel{}}
	matrix := run.Matrix
	if matrix == nil {
		matrix = input.NoMatrix{}
	}
	series := signals.New(signals.Config{
		Matrix: matrix, BenchPod: run.BenchPod, ShardPods: run.ShardPods, TargetPods: signals.PodsOf(run.PreSnapshot, run.PostSnapshot),
		ReplicaPods:        signals.ReplicaPodsOf(run.PreSnapshot, run.PostSnapshot),
		BifrostConcurrency: knobNumber(run.CapturedKnobs, bifrostConcurrencyKnob),
	})
	for _, panel := range serverPanels {
		samples, found, err := series.Signal(panel.signal)
		if err != nil {
			return spec.ServerChart{}, fmt.Errorf("signal %s: %w", panel.signal, err)
		}
		switch panel.value {
		case asIs:
			// The signal is drawn as it is.
		case perSecond:
			samples = increasePerSecond(samples)
		case perIncrement:
			denominator, hasDenominator, err := series.Signal(panel.denominator)
			if err != nil {
				return spec.ServerChart{}, fmt.Errorf("signal %s: %w", panel.denominator, err)
			}
			found = found && hasDenominator
			samples = increaseRatio(samples, denominator)
		}
		points := bucketize(samples, start, end, width)
		if !found || len(points) == 0 {
			continue
		}
		definition := panel.definition
		chart := spec.ServerPanel{
			Id: string(panel.signal), Group: panel.group, Title: panel.title, Unit: panel.unit, Definition: &definition,
			Series: []spec.PanelSeries{{Name: panel.title, Points: points}},
		}
		if panel.signal == input.GPUMemoryUsedBytes {
			chart.ReferenceLines, err = gpuCapacityLine(series)
			if err != nil {
				return spec.ServerChart{}, err
			}
		}
		document.Panels = append(document.Panels, chart)
	}
	resources, err := containerPanels(matrix, run.PreSnapshot, start, end, width)
	if err != nil {
		return spec.ServerChart{}, err
	}
	document.Panels = append(document.Panels, resources...)
	return document, nil
}

// containerPanels draws each container of the workloads under test: CPU and
// working set on the busiest pod in each bucket, with requests and limits.
func containerPanels(matrix input.MatrixSource, snapshot *spec.TargetSnapshot, start, end time.Time, width int) ([]spec.ServerPanel, error) {
	if snapshot == nil {
		return nil, nil
	}
	cpu, _, err := matrix.Matrix(containerCPU)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", containerCPU.Name, err)
	}
	memory, _, err := matrix.Matrix(containerMemory)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", containerMemory.Name, err)
	}
	var panels []spec.ServerPanel
	for _, workload := range snapshot.Workloads {
		pods := map[string]bool{}
		for _, pod := range workload.Pods {
			pods[pod.Name] = true
		}
		if len(workload.Pods) == 0 {
			continue
		}
		namespace := ""
		for _, container := range workload.Pods[0].Containers {
			for _, one := range append(append([]input.Series{}, cpu...), memory...) {
				if pods[one.Labels["pod"]] {
					namespace = one.Labels["namespace"]
					break
				}
			}
			label := fmt.Sprintf("%s/%s", namespace, container.Name)
			if points := busiestPerBucket(cpu, pods, container.Name, start, end, width); len(points) > 0 {
				panels = append(panels, spec.ServerPanel{
					Id: "cpu/" + label, Group: spec.PanelGroupResources, Title: "CPU, " + label, Unit: spec.PanelUnitCores,
					Series:         []spec.PanelSeries{{Name: "busiest pod", Points: points}},
					ReferenceLines: referenceLines(container, "cpu"),
				})
			}
			if points := busiestPerBucket(memory, pods, container.Name, start, end, width); len(points) > 0 {
				panels = append(panels, spec.ServerPanel{
					Id: "memory/" + label, Group: spec.PanelGroupResources, Title: "Memory working set, " + label, Unit: spec.PanelUnitBytes,
					Series:         []spec.PanelSeries{{Name: "busiest pod", Points: points}},
					ReferenceLines: referenceLines(container, "memory"),
				})
			}
		}
	}
	return panels, nil
}

func referenceLines(container spec.Container, name string) []spec.ReferenceLine {
	var lines []spec.ReferenceLine
	for _, entry := range []struct {
		label      string
		quantities spec.ResourceQuantities
	}{{"request", container.Requests}, {"limit", container.Limits}} {
		text, ok := entry.quantities[name]
		if !ok {
			continue
		}
		quantity, err := resource.ParseQuantity(text)
		if err != nil {
			continue
		}
		lines = append(lines, spec.ReferenceLine{Name: entry.label, Value: quantity.AsApproximateFloat64()})
	}
	return lines
}

// busiestPerBucket buckets each matching pod's series and keeps, per bucket,
// the pod with the highest mean: per-replica sizing must fit the busiest one.
func busiestPerBucket(all []input.Series, pods map[string]bool, container string, start, end time.Time, width int) []spec.ServerPoint {
	best := map[float64]spec.ServerPoint{}
	for _, one := range all {
		if !pods[one.Labels["pod"]] || one.Labels["container"] != container {
			continue
		}
		for _, point := range bucketize(one.Samples, start, end, width) {
			if current, ok := best[point.OffsetSeconds]; !ok || point.Mean > current.Mean {
				best[point.OffsetSeconds] = point
			}
		}
	}
	points := make([]spec.ServerPoint, 0, len(best))
	for _, point := range best {
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].OffsetSeconds < points[j].OffsetSeconds })
	return points
}

// bucketize averages samples into buckets of width seconds from start, with
// each bucket's minimum and maximum; empty buckets are left out.
func bucketize(samples []input.Sample, start, end time.Time, width int) []spec.ServerPoint {
	type bucket struct {
		total, low, high float64
		count            int
	}
	buckets := map[int]*bucket{}
	for _, sample := range samples {
		if sample.At.Before(start) || !sample.At.Before(end) || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			continue
		}
		index := int(sample.At.Sub(start).Seconds()) / width
		b, ok := buckets[index]
		if !ok {
			b = &bucket{low: math.Inf(1), high: math.Inf(-1)}
			buckets[index] = b
		}
		b.total, b.count = b.total+sample.Value, b.count+1
		b.low, b.high = math.Min(b.low, sample.Value), math.Max(b.high, sample.Value)
	}
	indexes := make([]int, 0, len(buckets))
	for index := range buckets {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	points := make([]spec.ServerPoint, len(indexes))
	for i, index := range indexes {
		b := buckets[index]
		points[i] = spec.ServerPoint{OffsetSeconds: float64(index * width), Mean: b.total / float64(b.count), Min: b.low, Max: b.high}
	}
	return points
}

// gpuCapacityLine is the largest target GPU's framebuffer capacity, drawn on
// the GPU memory panel; none when the exporter did not report it.
func gpuCapacityLine(series input.SignalSource) ([]spec.ReferenceLine, error) {
	capacity, found, err := series.Signal(input.GPUMemoryCapacityBytes)
	if err != nil {
		return nil, fmt.Errorf("signal %s: %w", input.GPUMemoryCapacityBytes, err)
	}
	if !found {
		return nil, nil
	}
	largest := 0.0
	for _, sample := range capacity {
		largest = math.Max(largest, sample.Value)
	}
	if largest <= 0 {
		return nil, nil
	}
	return []spec.ReferenceLine{{Name: "capacity", Value: largest}}, nil
}

// increasePerSecond turns a cumulative signal (already corrected for counter
// resets) into its increase per second between consecutive samples.
func increasePerSecond(cumulative []input.Sample) []input.Sample {
	var rates []input.Sample
	for i := 1; i < len(cumulative); i++ {
		seconds := cumulative[i].At.Sub(cumulative[i-1].At).Seconds()
		if seconds <= 0 {
			continue
		}
		rates = append(rates, input.Sample{At: cumulative[i].At, Value: (cumulative[i].Value - cumulative[i-1].Value) / seconds})
	}
	return rates
}

// increaseRatio is the increase of numerator over the increase of denominator
// between consecutive denominator samples. An interval where the denominator
// did not grow has no value (nothing was observed), and one where the
// numerator has no sample at both ends is skipped.
func increaseRatio(numerator, denominator []input.Sample) []input.Sample {
	at := make(map[time.Time]float64, len(numerator))
	for _, sample := range numerator {
		at[sample.At] = sample.Value
	}
	var ratios []input.Sample
	for i := 1; i < len(denominator); i++ {
		grew := denominator[i].Value - denominator[i-1].Value
		if grew <= 0 {
			continue
		}
		now, okNow := at[denominator[i].At]
		before, okBefore := at[denominator[i-1].At]
		if !okNow || !okBefore {
			continue
		}
		ratios = append(ratios, input.Sample{At: denominator[i].At, Value: (now - before) / grew})
	}
	return ratios
}
