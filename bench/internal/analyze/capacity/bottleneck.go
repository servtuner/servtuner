package capacity

import (
	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// candidate is a resource and the series that say how close to its ceiling it
// is. The order is the tie-break: the first of equally loaded resources wins.
type candidate struct {
	resource spec.BottleneckResource
	signals  []input.Signal
}

var bottleneckCandidates = []candidate{
	{spec.BottleneckResourceKvCache, []input.Signal{input.KVCacheUsage}},
	{spec.BottleneckResourceGpuEngineActivity, []input.Signal{input.GPUEngineActive}},
	{spec.BottleneckResourceCpu, []input.Signal{input.TargetCPUUsage, input.TargetCPUThrottle}},
	{spec.BottleneckResourceRouterConcurrency, []input.Signal{input.RouterConcurrency}},
	// upstream_capacity has no catalog entry to read, so it is never a candidate.
}

// findBottleneck names the resource closest to its ceiling at the knee stage,
// by the p95 of its utilisation over the stage. GPU
// engine activity needs DCGM profiling metrics; when they are missing, plain
// GPU utilisation stands in as a weak signal. It returns nil when no
// candidate series exists for the stage.
func findBottleneck(stage Stage, signals *signalReader) (*spec.Bottleneck, error) {
	var best *spec.Bottleneck
	consider := func(resource spec.BottleneckResource, utilization float64, isWeak bool) {
		utilization = min(max(utilization, 0), 1)
		if best == nil || utilization > best.Utilization {
			best = &spec.Bottleneck{Resource: resource, Utilization: utilization, IsWeakSignal: isWeak}
		}
	}
	hasEngineActivity := false
	for _, candidate := range bottleneckCandidates {
		utilization, ok, err := peakUtilization(stage, signals, candidate.signals)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		hasEngineActivity = hasEngineActivity || candidate.resource == spec.BottleneckResourceGpuEngineActivity
		consider(candidate.resource, utilization, false)
	}
	if !hasEngineActivity {
		utilization, ok, err := peakUtilization(stage, signals, []input.Signal{input.GPUUtil})
		if err != nil {
			return nil, err
		}
		if ok {
			consider(spec.BottleneckResourceGpuUtil, utilization, true)
		}
	}
	return best, nil
}

// peakUtilization is the largest p95 among a resource's series.
func peakUtilization(stage Stage, signals *signalReader, signalList []input.Signal) (float64, bool, error) {
	var peak float64
	var found bool
	for _, signal := range signalList {
		stats, ok, err := signals.stats(stage, signal, aggregate.Gauge)
		if err != nil {
			return 0, false, err
		}
		if ok {
			peak, found = max(peak, stats.P95), true
		}
	}
	return peak, found, nil
}
