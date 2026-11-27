package queueing

import "math"

// MemoryFit is the working set against requests in flight, per replica:
// WS ≈ baseline + bytes per in-flight request × L.
type MemoryFit struct {
	BytesPerInFlight float64
	BaselineBytes    float64
	Stages           int
}

// MemoryPoint is one usable stage: mean in flight per replica and mean
// working set per pod.
type MemoryPoint struct {
	InFlight, WorkingSetBytes float64
}

// minimumMemoryStages: a line needs two stages, and they must differ in load.
const minimumMemoryStages = 2

// FitMemory is the least-squares line through the stages. ok is false with
// fewer than two stages, no spread in load, or a slope that is not positive
// (memory that does not grow with load says nothing per request).
func FitMemory(points []MemoryPoint) (MemoryFit, bool) {
	if len(points) < minimumMemoryStages {
		return MemoryFit{}, false
	}
	var meanX, meanY float64
	for _, point := range points {
		meanX += point.InFlight / float64(len(points))
		meanY += point.WorkingSetBytes / float64(len(points))
	}
	var sxx, sxy float64
	for _, point := range points {
		sxx += (point.InFlight - meanX) * (point.InFlight - meanX)
		sxy += (point.InFlight - meanX) * (point.WorkingSetBytes - meanY)
	}
	if sxx == 0 {
		return MemoryFit{}, false
	}
	slope := sxy / sxx
	if slope <= 0 || math.IsNaN(slope) {
		return MemoryFit{}, false
	}
	return MemoryFit{BytesPerInFlight: slope, BaselineBytes: math.Max(0, meanY-slope*meanX), Stages: len(points)}, true
}
