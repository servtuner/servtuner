package sizing

import (
	"fmt"
	"math"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Catalog entries the sizing reads (bench/internal/prometheus/catalogs/kubernetes.yaml).
var (
	cpuCores      = input.SeriesRef{Catalog: spec.CatalogNameKubernetes, Name: "cpu_cores"}
	workingSet    = input.SeriesRef{Catalog: spec.CatalogNameKubernetes, Name: "memory_working_set_bytes"}
	throttleRatio = input.SeriesRef{Catalog: spec.CatalogNameKubernetes, Name: "cpu_throttle_ratio"}
)

// cpuRateWindow is the rate window of cpu_cores: a sample covers the minute
// before it, so the first minute of a stage still holds the previous stage.
const cpuRateWindow = time.Minute

// Leak detection: the working set grows in every usable stage,
// each fit with at least leakMinimumSamples distinct samples and a t statistic
// of at least leakMinimumT, over at least leakMinimumStages stages of at least
// leakMinimumWindow each. Shorter stages cannot tell a leak from the heap
// growing to a new load level, which a Go router does after every step up.
const (
	leakMinimumSamples = 6
	leakMinimumT       = 3
	leakMinimumStages  = 2
	leakMinimumWindow  = 5 * time.Minute
)

// usage is what one container used, over all its pods.
type usage struct {
	cpuP95, cpuPeak       float64
	memoryP95, memoryPeak float64
	hasCPU, hasMemory     bool
	// throttle is the highest CPU throttle ratio in any usable stage.
	throttle      float64
	throttleStage int
	// leakSlope is the working-set growth in bytes per second when it is
	// significant in every usable stage, else 0.
	leakSlope float64
	// stageMemory is each usable stage's mean working set per pod, and pods
	// how many pods had data: the inputs of bytes per in-flight request.
	stageMemory map[int]float64
	pods        int
}

// measurer reads one container's series.
type measurer struct {
	matrix   input.MatrixSource
	interval time.Duration
	// operating is the operating-point stage; usable are all usable stages.
	operating input.Stage
	usable    []input.Stage
}

func (m measurer) measure(container containerRef) (usage, error) {
	var result usage
	cpu, err := m.seriesOf(cpuCores, container)
	if err != nil {
		return usage{}, err
	}
	result.cpuP95, result.cpuPeak, result.hasCPU = busiestPod(cpu, m.operating, max(m.interval, cpuRateWindow))
	memory, err := m.seriesOf(workingSet, container)
	if err != nil {
		return usage{}, err
	}
	result.memoryP95, result.memoryPeak, result.hasMemory = busiestPod(memory, m.operating, m.interval)
	throttle, err := m.seriesOf(throttleRatio, container)
	if err != nil {
		return usage{}, err
	}
	result.throttle, result.throttleStage = m.highestInAnyStage(throttle)
	result.leakSlope = m.leakSlope(memory)
	result.stageMemory, result.pods = m.meanPerPod(memory)
	return result, nil
}

// meanPerPod is each usable stage's working set averaged over pods, after
// the gauge's first interval.
func (m measurer) meanPerPod(series []input.Series) (map[int]float64, int) {
	means := map[int]float64{}
	pods := 0
	for _, stage := range m.usable {
		var total float64
		var counted int
		for _, one := range series {
			if stats, ok := aggregate.StatsInWindow(one.Samples, stage.Start.Add(m.interval), stage.End); ok {
				total += stats.Mean
				counted++
			}
		}
		if counted > 0 {
			means[stage.Index] = total / float64(counted)
			pods = max(pods, counted)
		}
	}
	return means, pods
}

func (m measurer) seriesOf(ref input.SeriesRef, container containerRef) ([]input.Series, error) {
	all, _, err := m.matrix.Matrix(ref)
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", ref.Catalog, ref.Name, err)
	}
	var matching []input.Series
	for _, series := range all {
		if series.Labels["namespace"] == container.namespace && container.pods[series.Labels["pod"]] && series.Labels["container"] == container.name {
			matching = append(matching, series)
		}
	}
	return matching, nil
}

// busiestPod is the highest p95 and the highest peak of any one pod in the
// stage, after dropping the lag at its start: per-replica sizing must fit the
// busiest replica.
func busiestPod(series []input.Series, stage input.Stage, lag time.Duration) (p95, peak float64, found bool) {
	start, end := stage.Start.Add(lag), stage.End
	if !end.After(start) {
		return 0, 0, false
	}
	for _, one := range series {
		stats, ok := aggregate.StatsInWindow(one.Samples, start, end)
		if !ok {
			continue
		}
		p95, peak, found = math.Max(p95, stats.P95), math.Max(peak, stats.Max), true
	}
	return p95, peak, found
}

func (m measurer) highestInAnyStage(series []input.Series) (highest float64, stageIndex int) {
	for _, stage := range m.usable {
		_, peak, found := busiestPod(series, stage, m.interval)
		if found && peak > highest {
			highest, stageIndex = peak, stage.Index
		}
	}
	return highest, stageIndex
}

// leakSlope fits the working set against time in each usable stage, per pod.
// A slope counts only when it is positive and significant in every stage, and
// the smallest such slope is returned, so the allowance is not inflated by one
// noisy stage. Usable stages are at constant load, so a steady rise there is
// growth, not load.
func (m measurer) leakSlope(series []input.Series) float64 {
	if len(m.usable) < leakMinimumStages || len(series) == 0 {
		return 0
	}
	smallest := math.Inf(1)
	for _, stage := range m.usable {
		if stage.End.Sub(stage.Start.Add(m.interval)) < leakMinimumWindow {
			return 0
		}
		for _, one := range series {
			slope, t, n := fitSlope(one.Samples, stage.Start.Add(m.interval), stage.End)
			if n < leakMinimumSamples || slope <= 0 || t < leakMinimumT {
				return 0
			}
			smallest = math.Min(smallest, slope)
		}
	}
	return smallest
}

// fitSlope is the least-squares slope of value against time (per second) in
// [start, end), with its t statistic and the sample count. A value repeated by
// a query step finer than the exporter's refresh (cAdvisor updates about every
// 15s) is one observation, not several, so repeats are dropped before fitting.
func fitSlope(samples []input.Sample, start, end time.Time) (slope, t float64, n int) {
	var xs, ys []float64
	for _, sample := range samples {
		if sample.At.Before(start) || !sample.At.Before(end) {
			continue
		}
		if len(ys) > 0 && sample.Value == ys[len(ys)-1] {
			continue
		}
		xs = append(xs, sample.At.Sub(start).Seconds())
		ys = append(ys, sample.Value)
	}
	n = len(xs)
	if n < 3 {
		return 0, 0, n
	}
	var meanX, meanY float64
	for i := range xs {
		meanX += xs[i] / float64(n)
		meanY += ys[i] / float64(n)
	}
	var sxx, sxy float64
	for i := range xs {
		sxx += (xs[i] - meanX) * (xs[i] - meanX)
		sxy += (xs[i] - meanX) * (ys[i] - meanY)
	}
	if sxx == 0 {
		return 0, 0, n
	}
	slope = sxy / sxx
	var residuals float64
	for i := range xs {
		fitted := meanY + slope*(xs[i]-meanX)
		residuals += (ys[i] - fitted) * (ys[i] - fitted)
	}
	standardError := math.Sqrt(residuals / float64(n-2) / sxx)
	if standardError == 0 {
		return slope, math.Inf(1), n
	}
	return slope, slope / standardError, n
}
