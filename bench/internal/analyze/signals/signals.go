// Package signals derives the quantities the analyzer needs (input.Signal)
// from the raw catalog entries of prometheus/. It is the one place that knows
// which catalog entry feeds which check, how a pod is told apart from another,
// and how replicas are combined: sum for totals and queues, max for per-pod
// peaks, min for ready counts. Nothing here invents a series; a signal whose
// entries were not collected is simply not found.
package signals

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func ref(catalog spec.CatalogName, name string) input.SeriesRef {
	return input.SeriesRef{Catalog: catalog, Name: name}
}

// The catalog entries (bench/internal/prometheus/catalogs/*.yaml) the signals
// read.
var (
	CPUCores        = ref(spec.CatalogNameKubernetes, "cpu_cores")
	CPULimitCores   = ref(spec.CatalogNameKubernetes, "cpu_limit_cores")
	CPURequestCores = ref(spec.CatalogNameKubernetes, "cpu_request_cores")
	CPUThrottle     = ref(spec.CatalogNameKubernetes, "cpu_throttle_ratio")
	PodReady        = ref(spec.CatalogNameKubernetes, "pod_ready")
	PodRestarts     = ref(spec.CatalogNameKubernetes, "restarts")

	VLLMRunning      = ref(spec.CatalogNameVllm, "num_requests_running")
	VLLMGenerated    = ref(spec.CatalogNameVllm, "generation_tokens_total")
	VLLMWaiting      = ref(spec.CatalogNameVllm, "num_requests_waiting")
	VLLMPrefixHits   = ref(spec.CatalogNameVllm, "prefix_cache_hits_total")
	VLLMPrefixQuery  = ref(spec.CatalogNameVllm, "prefix_cache_queries_total")
	VLLMPreemptions  = ref(spec.CatalogNameVllm, "num_preemptions_total")
	VLLMKVCacheUsage = ref(spec.CatalogNameVllm, "kv_cache_usage")
	VLLMPromptTokens = ref(spec.CatalogNameVllm, "prompt_tokens_total")
	VLLMQueueTime    = ref(spec.CatalogNameVllm, "queue_time_seconds")
	VLLMPrefillTime  = ref(spec.CatalogNameVllm, "prefill_time_seconds")
	VLLMDecodeTime   = ref(spec.CatalogNameVllm, "decode_time_seconds")

	LiteLLMQueueTime = ref(spec.CatalogNameLitellm, "request_queue_time_seconds")
	BifrostActive    = ref(spec.CatalogNameBifrost, "active_requests")

	DCGMEngineActive = ref(spec.CatalogNameDcgm, "gr_engine_util_ratio")
	DCGMGPUUtil      = ref(spec.CatalogNameDcgm, "gpu_util_ratio")
	DCGMFBUsed       = ref(spec.CatalogNameDcgm, "fb_used_bytes")
	DCGMFBFree       = ref(spec.CatalogNameDcgm, "fb_free_bytes")
	DCGMFBCapacity   = ref(spec.CatalogNameDcgm, "fb_capacity_bytes")
	DCGMMemoryUtil   = ref(spec.CatalogNameDcgm, "memory_util_ratio")
	DCGMSMActive     = ref(spec.CatalogNameDcgm, "sm_util_ratio")
	DCGMTensorActive = ref(spec.CatalogNameDcgm, "tensor_util_ratio")
	DCGMPower        = ref(spec.CatalogNameDcgm, "board_power_watts")
	DCGMSMClock      = ref(spec.CatalogNameDcgm, "sm_clock_hertz")
)

// Table maps each signal to the catalog entries it is derived from. It is the
// single statement of that mapping; Derive follows it, and a test checks that
// every entry exists in the catalogs.
var Table = map[input.Signal][]input.SeriesRef{
	input.BenchCPUUsage:           {CPUCores, CPULimitCores},
	input.BenchCPUThrottle:        {CPUThrottle},
	input.TargetCPUUsage:          {CPUCores, CPULimitCores, CPURequestCores},
	input.TargetCPUThrottle:       {CPUThrottle},
	input.ReadyPods:               {PodReady},
	input.Restarts:                {PodRestarts},
	input.RequestsRunning:         {VLLMRunning},
	input.GenerationTokens:        {VLLMGenerated},
	input.RequestsWaiting:         {VLLMWaiting},
	input.PrefixCacheHits:         {VLLMPrefixHits},
	input.PrefixCacheQueries:      {VLLMPrefixQuery},
	input.Preemptions:             {VLLMPreemptions},
	input.KVCacheUsage:            {VLLMKVCacheUsage},
	input.QueueSecondsSum:         {LiteLLMQueueTime},
	input.QueueCount:              {LiteLLMQueueTime},
	input.RouterConcurrency:       {BifrostActive},
	input.GPUEngineActive:         {DCGMEngineActive},
	input.GPUUtil:                 {DCGMGPUUtil},
	input.GPUMemoryUsedBytes:      {DCGMFBUsed},
	input.GPUMemoryFreeFraction:   {DCGMFBUsed, DCGMFBFree},
	input.PromptTokens:            {VLLMPromptTokens},
	input.VLLMQueueSecondsSum:     {VLLMQueueTime},
	input.VLLMQueueCount:          {VLLMQueueTime},
	input.VLLMPrefillSecondsSum:   {VLLMPrefillTime},
	input.VLLMPrefillCount:        {VLLMPrefillTime},
	input.VLLMDecodeSecondsSum:    {VLLMDecodeTime},
	input.VLLMDecodeCount:         {VLLMDecodeTime},
	input.GPUMemoryCapacityBytes:  {DCGMFBCapacity},
	input.GPUMemoryControllerUtil: {DCGMMemoryUtil},
	input.GPUSMActive:             {DCGMSMActive},
	input.GPUTensorActive:         {DCGMTensorActive},
	input.GPUPowerWatts:           {DCGMPower},
	input.GPUSMClockHertz:         {DCGMSMClock},
}

// Config says which pods are whose.
type Config struct {
	Matrix input.MatrixSource
	// BenchPod is the bench pod's name; empty when bench did not run in a pod.
	BenchPod string
	// ShardPods are the follower shards' bench pods of a sharded run.
	ShardPods []string
	// TargetPods are the pods of every workload in the snapshot. Empty means
	// every pod that is not the bench pod.
	TargetPods map[string]bool
	// ReplicaPods are the pods of the workload under test only (role target),
	// without its upstreams: the replicas that per-replica capacity divides by.
	// Empty means TargetPods.
	ReplicaPods map[string]bool
	// BifrostConcurrency is the bifrost.concurrency knob as captured from the
	// target; 0 when unknown, which leaves RouterConcurrency without evidence.
	BifrostConcurrency float64
}

// Deriver implements input.SignalSource over a MatrixSource.
type Deriver struct {
	config Config
}

var _ input.SignalSource = (*Deriver)(nil)

// New returns a Deriver.
func New(config Config) *Deriver { return &Deriver{config: config} }

// PodsOf lists the pod names of every workload of the snapshots.
func PodsOf(snapshots ...*spec.TargetSnapshot) map[string]bool {
	return podsWhere(func(spec.Workload_2) bool { return true }, snapshots)
}

// ReplicaPodsOf lists the pod names of the workload under test (role target),
// leaving out its upstreams.
func ReplicaPodsOf(snapshots ...*spec.TargetSnapshot) map[string]bool {
	return podsWhere(func(workload spec.Workload_2) bool { return workload.Role == spec.WorkloadRoleTarget }, snapshots)
}

func podsWhere(keep func(spec.Workload_2) bool, snapshots []*spec.TargetSnapshot) map[string]bool {
	pods := map[string]bool{}
	for _, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		for _, workload := range snapshot.Workloads {
			if !keep(workload) {
				continue
			}
			for _, pod := range workload.Pods {
				pods[pod.Name] = true
			}
		}
	}
	return pods
}

// Signal implements input.SignalSource.
func (d *Deriver) Signal(signal input.Signal) ([]input.Sample, bool, error) {
	var samples []input.Sample
	var err error
	switch signal {
	case input.BenchCPUUsage:
		samples, err = d.cpuRatio(d.isBench, false)
	case input.TargetCPUUsage:
		samples, err = d.cpuRatio(d.isTarget, true)
	case input.BenchCPUThrottle:
		samples, err = d.combined(CPUThrottle, d.isBench, largest)
	case input.TargetCPUThrottle:
		samples, err = d.combined(CPUThrottle, d.isTarget, largest)
	case input.ReadyPods:
		samples, err = d.combined(PodReady, d.isReplica, total)
	case input.Restarts:
		samples, err = d.combined(PodRestarts, d.isTarget, total)
	case input.RequestsRunning:
		samples, err = d.combined(VLLMRunning, d.isTargetOrUnlabelled, total)
	case input.GenerationTokens:
		samples, err = d.cumulative(VLLMGenerated, "")
	case input.RequestsWaiting:
		samples, err = d.combined(VLLMWaiting, d.isTargetOrUnlabelled, total)
	case input.PrefixCacheHits:
		samples, err = d.cumulative(VLLMPrefixHits, "")
	case input.PrefixCacheQueries:
		samples, err = d.cumulative(VLLMPrefixQuery, "")
	case input.Preemptions:
		samples, err = d.cumulative(VLLMPreemptions, "")
	case input.KVCacheUsage:
		samples, err = d.combined(VLLMKVCacheUsage, d.isTargetOrUnlabelled, largest)
	case input.QueueSecondsSum:
		samples, err = d.cumulative(LiteLLMQueueTime, "_sum")
	case input.QueueCount:
		samples, err = d.cumulative(LiteLLMQueueTime, "_count")
	case input.RouterConcurrency:
		samples, err = d.routerConcurrency()
	case input.GPUEngineActive:
		samples, err = d.combined(DCGMEngineActive, d.isGPUOfTarget, largest)
	case input.GPUUtil:
		samples, err = d.combined(DCGMGPUUtil, d.isGPUOfTarget, largest)
	case input.GPUMemoryUsedBytes:
		samples, err = d.combined(DCGMFBUsed, d.isGPUOfTarget, largest)
	case input.GPUMemoryFreeFraction:
		samples, err = d.framebufferFree()
	case input.PromptTokens:
		samples, err = d.cumulative(VLLMPromptTokens, "")
	case input.VLLMQueueSecondsSum:
		samples, err = d.cumulative(VLLMQueueTime, "_sum")
	case input.VLLMQueueCount:
		samples, err = d.cumulative(VLLMQueueTime, "_count")
	case input.VLLMPrefillSecondsSum:
		samples, err = d.cumulative(VLLMPrefillTime, "_sum")
	case input.VLLMPrefillCount:
		samples, err = d.cumulative(VLLMPrefillTime, "_count")
	case input.VLLMDecodeSecondsSum:
		samples, err = d.cumulative(VLLMDecodeTime, "_sum")
	case input.VLLMDecodeCount:
		samples, err = d.cumulative(VLLMDecodeTime, "_count")
	case input.GPUMemoryCapacityBytes:
		samples, err = d.combined(DCGMFBCapacity, d.isGPUOfTarget, largest)
	case input.GPUMemoryControllerUtil:
		samples, err = d.combined(DCGMMemoryUtil, d.isGPUOfTarget, largest)
	case input.GPUSMActive:
		samples, err = d.combined(DCGMSMActive, d.isGPUOfTarget, largest)
	case input.GPUTensorActive:
		samples, err = d.combined(DCGMTensorActive, d.isGPUOfTarget, largest)
	case input.GPUPowerWatts:
		samples, err = d.combined(DCGMPower, d.isGPUOfTarget, largest)
	case input.GPUSMClockHertz:
		samples, err = d.combined(DCGMSMClock, d.isGPUOfTarget, smallest)
	default:
		return nil, false, fmt.Errorf("unknown signal %q", signal)
	}
	return samples, len(samples) > 0, err
}

func (d *Deriver) isBench(labels map[string]string) bool {
	pod := labels["pod"]
	return pod != "" && (pod == d.config.BenchPod || slices.Contains(d.config.ShardPods, pod))
}

func (d *Deriver) isTarget(labels map[string]string) bool {
	pod := labels["pod"]
	if d.isBench(labels) {
		return false
	}
	return len(d.config.TargetPods) == 0 || d.config.TargetPods[pod]
}

// isReplica accepts a pod of the workload under test, not of an upstream.
func (d *Deriver) isReplica(labels map[string]string) bool {
	if len(d.config.ReplicaPods) == 0 {
		return d.isTarget(labels)
	}
	return d.config.ReplicaPods[labels["pod"]]
}

// isTargetOrUnlabelled accepts server-metric series that carry no pod label
// (scrape configs differ), and otherwise the target's pods.
func (d *Deriver) isTargetOrUnlabelled(labels map[string]string) bool {
	_, hasPod := labels["pod"]
	return !hasPod || d.isTarget(labels)
}

// isGPUOfTarget accepts a DCGM series of a target pod, or one with no pod
// label (a node-level exporter). With the stock exporter (honor_labels false),
// the scraped pod label is the exporter's own pod and the workload's pod is
// in exported_pod, so exported_pod wins when it is set.
func (d *Deriver) isGPUOfTarget(labels map[string]string) bool {
	pod := labels["exported_pod"]
	if pod == "" {
		pod = labels["pod"]
	}
	if pod == "" {
		return true
	}
	return len(d.config.TargetPods) == 0 || d.config.TargetPods[pod]
}

func (d *Deriver) matrix(entry input.SeriesRef) ([]input.Series, error) {
	series, _, err := d.config.Matrix.Matrix(entry)
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", entry.Catalog, entry.Name, err)
	}
	return series, nil
}

// combined merges the matching series of an entry, timestamp by timestamp,
// with op.
func (d *Deriver) combined(entry input.SeriesRef, keep func(map[string]string) bool, op func([]float64) float64) ([]input.Sample, error) {
	series, err := d.matrix(entry)
	if err != nil {
		return nil, err
	}
	var chosen [][]input.Sample
	for _, one := range series {
		if keep == nil || keep(one.Labels) {
			chosen = append(chosen, one.Samples)
		}
	}
	return merge(chosen, op), nil
}

// cpuRatio is the CPU of each container over its limit, and of the busiest
// container among the matching pods. With fallbackToRequest, a container with
// no limit is measured against its request (the target may have none).
func (d *Deriver) cpuRatio(keep func(map[string]string) bool, fallbackToRequest bool) ([]input.Sample, error) {
	usage, err := d.matrix(CPUCores)
	if err != nil {
		return nil, err
	}
	limits, err := d.matrix(CPULimitCores)
	if err != nil {
		return nil, err
	}
	requests, err := d.matrix(CPURequestCores)
	if err != nil {
		return nil, err
	}
	var ratios [][]input.Sample
	for _, one := range usage {
		if !keep(one.Labels) {
			continue
		}
		ceiling := containerSeries(limits, one.Labels)
		if ceiling == nil && fallbackToRequest {
			ceiling = containerSeries(requests, one.Labels)
		}
		if ceiling == nil {
			continue
		}
		var ratio []input.Sample
		for _, sample := range one.Samples {
			if limit := valueAtOrBefore(ceiling, sample.At); limit > 0 {
				ratio = append(ratio, input.Sample{At: sample.At, Value: sample.Value / limit})
			}
		}
		ratios = append(ratios, ratio)
	}
	return merge(ratios, largest), nil
}

func containerSeries(all []input.Series, labels map[string]string) []input.Sample {
	for _, one := range all {
		if one.Labels["pod"] == labels["pod"] && one.Labels["container"] == labels["container"] {
			return one.Samples
		}
	}
	return nil
}

// valueAtOrBefore is the last sample at or before at, or the first sample when
// all are later. Limits are slow kube-state-metrics series with their own
// timestamps, and they hold until they change.
func valueAtOrBefore(samples []input.Sample, at time.Time) float64 {
	if len(samples) == 0 {
		return 0
	}
	index := sort.Search(len(samples), func(i int) bool { return samples[i].At.After(at) })
	if index == 0 {
		return samples[0].Value
	}
	return samples[index-1].Value
}

// cumulative sums the reset-corrected running totals of counter series. A
// restarted pod's counter falls to zero; its new value counts from zero, so a
// restart never subtracts. suffix selects a histogram's _sum or _count series
// by their __name__ label; empty selects all series of the entry.
func (d *Deriver) cumulative(entry input.SeriesRef, suffix string) ([]input.Sample, error) {
	series, err := d.matrix(entry)
	if err != nil {
		return nil, err
	}
	var totals [][]input.Sample
	for _, one := range series {
		if suffix != "" && !strings.HasSuffix(one.Labels["__name__"], suffix) {
			continue
		}
		if !d.isTargetOrUnlabelled(one.Labels) {
			continue
		}
		totals = append(totals, runningTotal(one.Samples))
	}
	return mergeCarryForward(totals), nil
}

func runningTotal(samples []input.Sample) []input.Sample {
	total := make([]input.Sample, len(samples))
	var running float64
	for i, sample := range samples {
		switch {
		case i == 0:
		case sample.Value >= samples[i-1].Value:
			running += sample.Value - samples[i-1].Value
		default:
			running += sample.Value
		}
		total[i] = input.Sample{At: sample.At, Value: running}
	}
	return total
}

func (d *Deriver) routerConcurrency() ([]input.Sample, error) {
	if d.config.BifrostConcurrency <= 0 {
		return nil, nil
	}
	active, err := d.combined(BifrostActive, d.isTargetOrUnlabelled, largest)
	for i := range active {
		active[i].Value /= d.config.BifrostConcurrency
	}
	return active, err
}

// framebufferFree is each target GPU's free framebuffer over its used plus
// free, and of the fullest GPU: FB_RESERVED (the driver's) is in neither, so
// the share is of what processes can use.
func (d *Deriver) framebufferFree() ([]input.Sample, error) {
	used, err := d.matrix(DCGMFBUsed)
	if err != nil {
		return nil, err
	}
	free, err := d.matrix(DCGMFBFree)
	if err != nil {
		return nil, err
	}
	var fractions [][]input.Sample
	for _, one := range free {
		if !d.isGPUOfTarget(one.Labels) {
			continue
		}
		matching := sameLabels(used, one.Labels)
		if matching == nil {
			continue
		}
		var fraction []input.Sample
		for _, sample := range one.Samples {
			if total := sample.Value + valueAtOrBefore(matching, sample.At); total > 0 {
				fraction = append(fraction, input.Sample{At: sample.At, Value: sample.Value / total})
			}
		}
		fractions = append(fractions, fraction)
	}
	return merge(fractions, smallest), nil
}

// sameLabels is the series whose labels equal labels, or nil.
func sameLabels(series []input.Series, labels map[string]string) []input.Sample {
	for _, one := range series {
		if maps.Equal(one.Labels, labels) {
			return one.Samples
		}
	}
	return nil
}

func smallest(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		result = min(result, value)
	}
	return result
}

func total(values []float64) float64 {
	var result float64
	for _, value := range values {
		result += value
	}
	return result
}

func largest(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		result = larger(result, value)
	}
	return result
}

func larger(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

// merge combines series sample by sample on identical timestamps. A series
// that has no sample at a timestamp does not take part in it.
func merge(all [][]input.Sample, op func([]float64) float64) []input.Sample {
	byTime := map[time.Time][]float64{}
	for _, samples := range all {
		for _, sample := range samples {
			byTime[sample.At] = append(byTime[sample.At], sample.Value)
		}
	}
	return flatten(byTime, op)
}

// mergeCarryForward sums running totals, holding each series at its last value
// between its samples, so a series sampled at slightly different instants does
// not make the sum jump.
func mergeCarryForward(all [][]input.Sample) []input.Sample {
	times := map[time.Time]bool{}
	for _, samples := range all {
		for _, sample := range samples {
			times[sample.At] = true
		}
	}
	byTime := map[time.Time][]float64{}
	for at := range times {
		for _, samples := range all {
			if len(samples) > 0 && !samples[0].At.After(at) {
				byTime[at] = append(byTime[at], valueAtOrBefore(samples, at))
			}
		}
	}
	return flatten(byTime, total)
}

func flatten(byTime map[time.Time][]float64, op func([]float64) float64) []input.Sample {
	samples := make([]input.Sample, 0, len(byTime))
	for at, values := range byTime {
		samples = append(samples, input.Sample{At: at, Value: op(values)})
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	return samples
}
