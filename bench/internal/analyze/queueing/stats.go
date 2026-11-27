// Package queueing sizes concurrency limits and queues from the request
// records: Little's law as a check, the tail of the
// in-flight count L(t) for the concurrency C, and the SLO wait budget and the
// measured bursts for the queue B. The formulas are nodes of the run's
// derivation graph (package derive).
package queueing

import (
	"math"
	"slices"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// Resolution of the in-flight series L(t) (Inputs).
const inFlightResolution = 10 * time.Millisecond

// littleTolerance: mean(L) must equal λ × mean(W) within 5%.
const littleTolerance = 0.05

// burstWindow is the window B_min counts arrivals in.
const burstWindow = time.Second

// StageStats is what one stage's records say about queueing.
type StageStats struct {
	Stage input.Stage
	// Requests is the number of records with a known E2E.
	Requests int
	// ArrivalRate λ is requests per second sent during the stage.
	ArrivalRate float64
	// Time in system W: the whole E2E, since a stream holds its slot to the end.
	MeanSeconds, P95Seconds, P99Seconds float64
	// CoefficientOfVariation is σ_W / mean_W.
	CoefficientOfVariation float64
	// In flight over the window where every request in flight is one of this
	// stage's: from p99(W) after the start (earlier requests have finished) to
	// the end.
	InFlightMean, InFlightP99 float64
	InFlightSamples           int
	// LittleError is |mean(L) − λ·mean(W)| / λ·mean(W).
	LittleError float64
	// ArrivalsP99 is the p99 of arrivals per one-second window.
	ArrivalsP99 float64
}

// IsSteady says whether the stage passes Little's law within 5%.
func (s StageStats) IsSteady() bool { return s.Requests > 0 && s.LittleError <= littleTolerance }

// Measure computes a stage's statistics from its records. ok is false when
// the stage has no completed requests or too short a window for L(t).
func Measure(stage input.Stage, rows []engine.RequestRow) (StageStats, bool) {
	return MeasureSlots(stage, rows, WholeRequest)
}

// SlotTime is how long one request holds a concurrency slot, or false when the
// row has no such time.
type SlotTime func(engine.RequestRow) (float64, bool)

// WholeRequest holds a slot for the request's whole end-to-end time.
func WholeRequest(row engine.RequestRow) (float64, bool) {
	return row.E2ESeconds, row.E2ESeconds > 0
}

// UntilFirstToken holds a slot until the first token: a streaming router
// (Bifrost) frees its provider slot once the upstream starts streaming
// (measured on kind, "Streaming through a router").
func UntilFirstToken(row engine.RequestRow) (float64, bool) {
	if row.TTFTSeconds == nil || *row.TTFTSeconds <= 0 {
		return 0, false
	}
	return *row.TTFTSeconds, true
}

// MeasureSlots is Measure with the slot time each request holds: W, L(t) and
// the arrival statistics are all of the slot, not of the whole request.
func MeasureSlots(stage input.Stage, rows []engine.RequestRow, slot SlotTime) (StageStats, bool) {
	result := StageStats{Stage: stage}
	// Every request that arrived and ended is demand (λ, the bursts), even one
	// with no slot time, such as a request the router dropped; W and L(t) are
	// of the requests that held a slot.
	var arrivals, slotArrivals []time.Time
	var durations []float64
	for _, row := range rows {
		if row.E2ESeconds <= 0 {
			continue
		}
		arrivals = append(arrivals, row.SentAt)
		if seconds, ok := slot(row); ok {
			durations = append(durations, seconds)
			slotArrivals = append(slotArrivals, row.SentAt)
		}
	}
	if len(durations) == 0 {
		return result, false
	}
	result.Requests = len(arrivals)
	result.ArrivalRate = float64(len(arrivals)) / stage.Duration().Seconds()
	sorted := stats.SortedCopy(durations)
	result.MeanSeconds, result.CoefficientOfVariation = meanAndCV(sorted)
	result.P95Seconds, result.P99Seconds = stats.NearestRank(sorted, 0.95), stats.NearestRank(sorted, 0.99)

	windowStart := stage.Start.Add(time.Duration(result.P99Seconds * float64(time.Second)))
	inFlight := inFlightSeries(slotArrivals, durations, windowStart, stage.End)
	if len(inFlight) == 0 {
		return result, false
	}
	result.InFlightSamples = len(inFlight)
	var total float64
	for _, count := range inFlight {
		total += count
	}
	result.InFlightMean = total / float64(len(inFlight))
	result.InFlightP99 = stats.Percentile(inFlight, 0.99)
	// Little's law holds for the requests that held a slot.
	expected := float64(len(durations)) / stage.Duration().Seconds() * result.MeanSeconds
	result.LittleError = math.Abs(result.InFlightMean-expected) / expected
	result.ArrivalsP99 = arrivalsP99(arrivals, stage)
	return result, true
}

func meanAndCV(values []float64) (mean, cv float64) {
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	var variance float64
	for _, value := range values {
		variance += (value - mean) * (value - mean)
	}
	if len(values) > 1 {
		variance /= float64(len(values) - 1)
	}
	return mean, math.Sqrt(variance) / mean
}

// inFlightSeries samples the number of requests in flight every 10ms in
// [start, end), from each request's send time and duration.
func inFlightSeries(arrivals []time.Time, durations []float64, start, end time.Time) []float64 {
	if !end.After(start) {
		return nil
	}
	type event struct {
		at    time.Time
		delta int
	}
	events := make([]event, 0, 2*len(arrivals))
	for i, sent := range arrivals {
		events = append(events, event{sent, +1}, event{sent.Add(time.Duration(durations[i] * float64(time.Second))), -1})
	}
	slices.SortFunc(events, func(a, b event) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return a.delta - b.delta // a request ending at an instant is gone before one starting then
	})
	var series []float64
	inFlight, next := 0, 0
	for at := start; at.Before(end); at = at.Add(inFlightResolution) {
		for next < len(events) && !events[next].at.After(at) {
			inFlight += events[next].delta
			next++
		}
		series = append(series, float64(inFlight))
	}
	return series
}

// arrivalsP99 is the p99 of the number of requests sent in each whole
// one-second window of the stage.
func arrivalsP99(arrivals []time.Time, stage input.Stage) float64 {
	windows := int(stage.Duration() / burstWindow)
	if windows == 0 {
		return 0
	}
	counts := make([]float64, windows)
	for _, sent := range arrivals {
		window := int(sent.Sub(stage.Start) / burstWindow)
		if window >= 0 && window < windows {
			counts[window]++
		}
	}
	return stats.Percentile(counts, 0.99)
}

// PeakInFlight is the most requests holding a slot at once during the stage,
// sampled like L(t); ok is false when no request held a slot.
func PeakInFlight(stage input.Stage, rows []engine.RequestRow, slot SlotTime) (float64, bool) {
	var arrivals []time.Time
	var durations []float64
	for _, row := range rows {
		if seconds, ok := slot(row); ok {
			arrivals = append(arrivals, row.SentAt)
			durations = append(durations, seconds)
		}
	}
	series := inFlightSeries(arrivals, durations, stage.Start, stage.End)
	if len(series) == 0 {
		return 0, false
	}
	return slices.Max(series), true
}
