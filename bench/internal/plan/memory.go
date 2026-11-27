package plan

import (
	"cmp"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Node names of the memory model.
const (
	nodePayload         = "payload_bytes"
	nodePayloadMeasured = "payload_bytes_measured"
	nodeBaselineMemory  = "ws0"
	nodePerInFlight     = "m"
	nodePerInFlightLow  = "m_low"
	nodeMeasuredM       = "m_measured"
	// nodeSeriesPayload and nodeSeriesM are the payload series' points,
	// numbered from 1 by size: payload_1 and m_1 are the smallest.
	nodeSeriesPayload = "payload"
	nodeSeriesM       = "m"
	nodeSlopeLow      = "k_low"
	nodeSlopeHigh     = "k_high"
	nodeQueueSize     = "queue_size"
	nodeLimitHeadroom = "h_lim"
	nodeWorkingSet    = "working_set"
	nodeWorkingSetLow = "working_set_low"
)

// sizeTolerance treats a size within a part per million of a measured bound
// as inside it: measured sizes are means, so "833" means 833.0007.
const sizeTolerance = 1e-6

// The one-size assumption of the payload-size model: one buffered copy of the body
// (k = 1) up to body, parsed form and re-encoding (k = 3).
const (
	assumedSlopeLow  = 1.0
	assumedSlopeHigh = 3.0
)

// memoryPoint is one run's bytes per in-flight request at its request size.
type memoryPoint struct {
	runID              string
	payload, perFlight float64
}

// memoryPlan is how a tier's m(s) is known.
type memoryPlan struct {
	basis spec.PlanTierMemoryBasis
	// points are the runs of the line; min and max bound their sizes.
	points   []memoryPoint
	min, max float64
}

// payloadSeries matches payload-series models to the tiers they measured: the
// same target type, knobs and hardware, at another request size. Every one
// must match a tier, or the series would be silently ignored.
func payloadSeries(tiers []*tierPlan, series []spec.RunCapacityModel) error {
	for _, model := range series {
		if model.Memory == nil || model.Memory.RequestBytesMean == nil {
			return fmt.Errorf("payload model of run %s has no memory line with a request size: it cannot measure m(s)", model.RunId)
		}
		matched := false
		for _, tier := range tiers {
			if sameConfiguration(tier.model.Identity, model.Identity) {
				tier.series = append(tier.series, model)
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("payload model of run %s matches no tier: it must share a tier's target type, knobs and hardware profile", model.RunId)
		}
	}
	return nil
}

func sameConfiguration(a, b spec.ModelIdentity) bool {
	return a.TargetType == b.TargetType && reflect.DeepEqual(a.Knobs, b.Knobs) &&
		reflect.DeepEqual(a.HardwareProfileId, b.HardwareProfileId)
}

// addMemory puts m(s) and the working set at the planned C + B into the
// graph. It needs the tier's memory line and its concurrency node.
func (t *tierPlan) addMemory(graph *derive.Graph) {
	memory := t.model.Memory
	if memory == nil || t.model.Queueing == nil {
		return
	}
	source := &derive.Source{File: "capacity_model.json"}
	zero, one := 0.0, 1.0
	graph.AddMeasured(t.node(nodeBaselineMemory), "Working set with nothing in flight (WS_0)", "bytes", memory.BaselineBytes, source)
	queueSize := 0.0
	if memory.QueueSize != nil {
		queueSize = float64(*memory.QueueSize)
	}
	graph.AddMeasured(t.node(nodeQueueSize), "Recommended queue size B of the run", "requests", queueSize, source)
	headroom := 0.0
	if memory.LimitHeadroom != nil {
		headroom = *memory.LimitHeadroom
	}
	graph.AddParameter(t.node(nodeLimitHeadroom), "Limit headroom h_lim", "", headroom, &derive.Bounds{Min: &zero, Max: &one}, "sizing policy: memory")
	workingSet := func(perFlight string) string {
		return fmt.Sprintf("(%s + %s * (%s + %s)) * (1 + %s)", t.node(nodeBaselineMemory), perFlight, t.node(nodeConcurrency), t.node(nodeQueueSize), t.node(nodeLimitHeadroom))
	}

	t.memory = &memoryPlan{basis: spec.PlanTierMemoryBasisMeasured}
	if memory.RequestBytesMean == nil {
		// No request size: m is the run's own, at its own payload.
		graph.AddMeasured(t.node(nodePerInFlight), "Bytes per in-flight request m", "bytes", memory.BytesPerInFlight, source)
		graph.AddDerived(t.node(nodeWorkingSet), "Working set at C + B, with the limit's headroom", "bytes", workingSet(t.node(nodePerInFlight)))
		return
	}
	graph.AddMeasured(t.node(nodePayload), "Request size s", "bytes", *memory.RequestBytesMean, source)
	points := []memoryPoint{{runID: t.model.RunId, payload: *memory.RequestBytesMean, perFlight: memory.BytesPerInFlight}}
	for _, model := range t.series {
		points = append(points, memoryPoint{runID: model.RunId, payload: *model.Memory.RequestBytesMean, perFlight: model.Memory.BytesPerInFlight})
	}
	t.memory.points = points
	t.memory.min, t.memory.max = math.Inf(1), math.Inf(-1)
	for _, point := range points {
		t.memory.min, t.memory.max = math.Min(t.memory.min, point.payload), math.Max(t.memory.max, point.payload)
	}
	if sizes := bySize(points); len(sizes) >= 2 {
		t.memory.basis = spec.PlanTierMemoryBasisFitted
		for i, point := range sizes {
			graph.AddMeasured(t.node(fmt.Sprintf("%s_%d", nodeSeriesPayload, i+1)), fmt.Sprintf("Request size of payload point %d", i+1), "bytes", point.payload, source)
			graph.AddMeasured(t.node(fmt.Sprintf("%s_%d", nodeSeriesM, i+1)), fmt.Sprintf("Bytes per in-flight request at payload point %d", i+1), "bytes", point.perFlight, source)
		}
		graph.AddDerived(t.node(nodePerInFlight), "Bytes per in-flight request m(s), interpolated between the measured sizes", "bytes", t.interpolation(len(sizes)))
		graph.AddDerived(t.node(nodeWorkingSet), "Working set at C + B, with the limit's headroom", "bytes", workingSet(t.node(nodePerInFlight)))
		return
	}
	// One size: k is unknown, so m(s) is a range from the measured point.
	graph.AddMeasured(t.node(nodePayloadMeasured), "Request size the run measured", "bytes", *memory.RequestBytesMean, source)
	graph.AddMeasured(t.node(nodeMeasuredM), "Bytes per in-flight request at the measured size", "bytes", memory.BytesPerInFlight, source)
	graph.AddParameter(t.node(nodeSlopeLow), "Assumed k, low: one buffered copy", "", assumedSlopeLow, &derive.Bounds{Min: &zero}, "payload-size model (assumption)")
	graph.AddParameter(t.node(nodeSlopeHigh), "Assumed k, high: body, parsed and re-encoded", "", assumedSlopeHigh, &derive.Bounds{Min: &zero}, "payload-size model (assumption)")
	perFlight := func(slope string) string {
		return fmt.Sprintf("max(0, %s + %s * (%s - %s))", t.node(nodeMeasuredM), slope, t.node(nodePayload), t.node(nodePayloadMeasured))
	}
	graph.AddDerived(t.node(nodePerInFlightLow), "Bytes per in-flight request m(s), at k low", "bytes", perFlight(t.node(nodeSlopeLow)))
	graph.AddDerived(t.node(nodePerInFlight), "Bytes per in-flight request m(s), at k high", "bytes", perFlight(t.node(nodeSlopeHigh)))
	graph.AddDerived(t.node(nodeWorkingSetLow), "Working set at C + B at k low, with the limit's headroom", "bytes", workingSet(t.node(nodePerInFlightLow)))
	graph.AddDerived(t.node(nodeWorkingSet), "Working set at C + B at k high, with the limit's headroom", "bytes", workingSet(t.node(nodePerInFlight)))
}

// bySize averages the points at each request size and sorts them by size.
func bySize(points []memoryPoint) []memoryPoint {
	sums := map[float64][2]float64{}
	for _, point := range points {
		sum := sums[point.payload]
		sums[point.payload] = [2]float64{sum[0] + point.perFlight, sum[1] + 1}
	}
	sizes := make([]memoryPoint, 0, len(sums))
	for payload, sum := range sums {
		sizes = append(sizes, memoryPoint{payload: payload, perFlight: sum[0] / sum[1]})
	}
	slices.SortFunc(sizes, func(a, b memoryPoint) int { return cmp.Compare(a.payload, b.payload) })
	return sizes
}

// interpolation is m(s) through n points sorted by size (m at a
// size within the measured range is interpolated): linear between
// neighbours, and the end segments extended outside the range. As a sum of
// clamped segments it stays one formula, so an override of the payload size
// re-evaluates it:
//
//	m_1 + slope_1 × min(s − s_1, s_2 − s_1) + Σ slope_i × clamp(s − s_i, 0, s_(i+1) − s_i) + slope_(n−1) × max(s − s_(n−1), 0)
func (t *tierPlan) interpolation(n int) string {
	at := func(name string, i int) string { return t.node(fmt.Sprintf("%s_%d", name, i)) }
	slope := func(i int) string {
		return fmt.Sprintf("(%s - %s) / (%s - %s)", at(nodeSeriesM, i+1), at(nodeSeriesM, i), at(nodeSeriesPayload, i+1), at(nodeSeriesPayload, i))
	}
	s := t.node(nodePayload)
	if n == 2 {
		return fmt.Sprintf("max(0, %s + %s * (%s - %s))", at(nodeSeriesM, 1), slope(1), s, at(nodeSeriesPayload, 1))
	}
	terms := []string{
		at(nodeSeriesM, 1),
		fmt.Sprintf("%s * min(%s - %s, %s - %s)", slope(1), s, at(nodeSeriesPayload, 1), at(nodeSeriesPayload, 2), at(nodeSeriesPayload, 1)),
	}
	for i := 2; i <= n-2; i++ {
		terms = append(terms, fmt.Sprintf("%s * clamp(%s - %s, 0, %s - %s)", slope(i), s, at(nodeSeriesPayload, i), at(nodeSeriesPayload, i+1), at(nodeSeriesPayload, i)))
	}
	terms = append(terms, fmt.Sprintf("%s * max(%s - %s, 0)", slope(n-1), s, at(nodeSeriesPayload, n-1)))
	return "max(0, " + strings.Join(terms, " + ") + ")"
}

// memoryResult reads the tier's memory block and runs the memory checks:
// the working set against the per-replica limit (Memory limit check),
// and the basis of m(s).
func (t *tierPlan) memoryResult(evaluated *derive.Result, add func(spec.PlanCheckStatus, string)) *spec.PlanTierMemory {
	if t.memory == nil {
		return nil
	}
	value := func(name string) (float64, bool) { return evaluated.Value(t.node(name)) }
	perFlight, hasPerFlight := value(nodePerInFlight)
	workingSet, hasWorkingSet := value(nodeWorkingSet)
	if !hasPerFlight || !hasWorkingSet {
		return nil
	}
	result := &spec.PlanTierMemory{Basis: t.memory.basis, BytesPerInFlight: perFlight, WorkingSetBytes: workingSet}
	for _, point := range t.memory.points {
		result.Runs = append(result.Runs, point.runID)
	}
	payload, hasPayload := value(nodePayload)
	if hasPayload {
		result.PayloadBytes = &payload
	}
	switch {
	case !hasPayload:
	case t.memory.basis == spec.PlanTierMemoryBasisFitted && (payload < t.memory.min*(1-sizeTolerance) || payload > t.memory.max*(1+sizeTolerance)):
		result.Basis = spec.PlanTierMemoryBasisExtrapolated
		add(spec.PlanCheckStatusWarning, fmt.Sprintf("m(s) is extrapolated: %s-byte requests are outside the %s to %s bytes the payload series measured.",
			derive.FormatNumber(payload), derive.FormatNumber(t.memory.min), derive.FormatNumber(t.memory.max)))
	case t.memory.basis == spec.PlanTierMemoryBasisMeasured && math.Abs(payload-t.memory.min) > sizeTolerance*t.memory.min:
		result.Basis = spec.PlanTierMemoryBasisAssumed
		low, _ := value(nodePerInFlightLow)
		workingSetLow, _ := value(nodeWorkingSetLow)
		result.BytesPerInFlightLow, result.WorkingSetBytesLow = &low, &workingSetLow
		add(spec.PlanCheckStatusWarning, fmt.Sprintf("Only %s-byte requests were measured, so m(s) at %s bytes assumes k between %s× and %s× (%s to %s bytes per request). Run the payload-size template to measure it.",
			derive.FormatNumber(t.memory.min), derive.FormatNumber(payload), derive.FormatNumber(assumedSlopeLow), derive.FormatNumber(assumedSlopeHigh),
			derive.FormatNumber(low), derive.FormatNumber(perFlight)))
	}
	limit := t.model.PerReplica
	if limit == nil || limit.MemoryLimitBytes == nil {
		return result
	}
	least := workingSet
	if result.WorkingSetBytesLow != nil {
		least = *result.WorkingSetBytesLow
	}
	remedy := "lower B, lower C per replica and add replicas, or raise the memory limit"
	switch {
	case least > *limit.MemoryLimitBytes:
		add(spec.PlanCheckStatusViolatesMeasured, fmt.Sprintf("At C + B the working set could reach %s bytes, past the %s-byte memory limit: %s.",
			derive.FormatNumber(least), derive.FormatNumber(*limit.MemoryLimitBytes), remedy))
	case workingSet > *limit.MemoryLimitBytes:
		add(spec.PlanCheckStatusWarning, fmt.Sprintf("At the high assumption the working set at C + B could reach %s bytes, past the %s-byte memory limit: %s.",
			derive.FormatNumber(workingSet), derive.FormatNumber(*limit.MemoryLimitBytes), remedy))
	default:
		add(spec.PlanCheckStatusOk, fmt.Sprintf("The working set at C + B, %s bytes, fits the %s-byte memory limit.",
			derive.FormatNumber(workingSet), derive.FormatNumber(*limit.MemoryLimitBytes)))
	}
	return result
}
