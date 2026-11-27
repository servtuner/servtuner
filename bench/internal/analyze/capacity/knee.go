package capacity

import (
	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// findKnee returns the first usable stage where headroom disappears:
// p95 latency growing more than 2x faster than RPS,
// queueing, or preemptions. The server-side reasons are judged only when their
// series exist. It returns nil when no stage qualifies.
func findKnee(usable []Stage, signals *signalReader) (*spec.Knee, error) {
	for i, stage := range usable {
		if i > 0 {
			if ratio, isKnee := latencySlopeRatio(usable[i-1], stage); isKnee {
				return &spec.Knee{StageIndex: stage.Window.Index, Reason: spec.KneeReasonLatencySlope, SlopeRatio: &ratio}, nil
			}
		}
		isQueueing, err := isQueueing(usable[0], stage, signals)
		if err != nil {
			return nil, err
		}
		if isQueueing {
			return &spec.Knee{StageIndex: stage.Window.Index, Reason: spec.KneeReasonQueueing}, nil
		}
		increase, ok, err := signals.increase(stage, input.Preemptions)
		if err != nil {
			return nil, err
		}
		if ok && increase > 0 {
			return &spec.Knee{StageIndex: stage.Window.Index, Reason: spec.KneeReasonPreemptions}, nil
		}
	}
	return nil, nil
}

// latencySlopeRatio is the largest, over TTFT, ITL and E2E, of the relative
// growth of p95 latency divided by the relative growth of achieved RPS between
// two consecutive usable stages. isKnee is true above KneeSlopeRatio.
func latencySlopeRatio(previous, current Stage) (ratio float64, isKnee bool) {
	rpsGrowth := relativeGrowth(previous.Aggregate.AchievedRps, current.Aggregate.AchievedRps)
	if rpsGrowth <= 0 {
		return 0, false
	}
	for _, pair := range [][2]*spec.LatencySummary{
		{previous.Aggregate.Ttft, current.Aggregate.Ttft},
		{previous.Aggregate.Itl, current.Aggregate.Itl},
		{previous.Aggregate.E2E, current.Aggregate.E2E},
	} {
		before, after := p95Of(pair[0]), p95Of(pair[1])
		if before == nil || after == nil || *before <= 0 {
			continue
		}
		ratio = max(ratio, relativeGrowth(*before, *after)/rpsGrowth)
	}
	return ratio, ratio > KneeSlopeRatio
}

func p95Of(summary *spec.LatencySummary) *float64 {
	if summary == nil {
		return nil
	}
	return summary.P95Seconds.Value
}

func relativeGrowth(before, after float64) float64 {
	if before <= 0 {
		return 0
	}
	return after/before - 1
}

// isQueueing reports sustained queueing in the stage: vLLM's waiting gauge
// above zero for most of the stage, or a router's queue time more than
// QueueTimeBaselineMultiple times its value in the first usable stage.
func isQueueing(baseline, stage Stage, signals *signalReader) (bool, error) {
	waiting, ok, err := signals.stats(stage, input.RequestsWaiting, aggregate.Gauge)
	if err != nil {
		return false, err
	}
	if ok && waiting.PositiveFraction > QueueingSustainedFraction {
		return true, nil
	}
	queue, ok, err := signals.queueMean(stage)
	if err != nil || !ok {
		return false, err
	}
	base, ok, err := signals.queueMean(baseline)
	if err != nil {
		return false, err
	}
	return ok && baseline.Window.Index != stage.Window.Index &&
		queue > QueueTimeBaselineMultiple*max(base, QueueTimeBaselineFloorSeconds), nil
}

// queueMean is a router's mean queue time during the stage: the growth of the
// histogram's sum over the growth of its count, both cumulative across
// replicas, over the stage trimmed as a derived series (Rule B). Only LiteLLM
// has a queue-time histogram; for other routers there is no evidence.
func (r *signalReader) queueMean(stage Stage) (float64, bool, error) {
	if stage.IsLowResolution {
		return 0, false, nil
	}
	start, end, ok := aggregate.TrimWindow(stage.Window, aggregate.Derived, r.interval, secondsToDuration(stage.MeanE2ESeconds))
	if !ok {
		return 0, false, nil
	}
	var growth [2]float64
	for i, signal := range []input.Signal{input.QueueSecondsSum, input.QueueCount} {
		samples, found, err := r.samples(signal)
		if err != nil || !found {
			return 0, false, err
		}
		increase, ok := aggregate.CounterIncrease(samples, start, end)
		if !ok {
			return 0, false, nil
		}
		growth[i] = increase
	}
	if growth[1] <= 0 {
		return 0, false, nil
	}
	return growth[0] / growth[1], true, nil
}
