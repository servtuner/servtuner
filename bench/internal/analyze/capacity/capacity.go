// Package capacity builds the capacity model from the
// ladder of valid stages: the SLO-max RPS, the knee, the bottleneck at the knee
// and the per-replica normalisation. A stage that the quality gate excluded
// never contributes to any of them.
package capacity

import (
	"fmt"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Knee thresholds.
const (
	// KneeSlopeRatio: p95 latency growing more than 2x faster than RPS.
	KneeSlopeRatio = 2.0
	// QueueTimeBaselineMultiple: a router's queue time more than 3x its
	// stage-1 baseline.
	QueueTimeBaselineMultiple = 3.0
	// QueueTimeBaselineFloorSeconds keeps a baseline of about zero from
	// turning every later stage into a knee.
	QueueTimeBaselineFloorSeconds = 0.001
	// QueueingSustainedFraction turns "waiting > 0 sustained" into a number the
	// docs do not give: more than half of the stage's samples are above zero.
	QueueingSustainedFraction = 0.5
)

// Stage is one stage as the capacity model sees it.
type Stage struct {
	Window          input.Stage
	Aggregate       spec.StageAggregate
	MeanE2ESeconds  float64
	IsExcluded      bool
	IsLowResolution bool
	// IsTargetSaturated marks an excluded stage where the target fell behind
	// (quality.IsTargetSaturated). It is no measurement at its offered rate,
	// but it shows the target could not sustain that rate, so the SLO-max
	// scan counts it as a failing stage.
	IsTargetSaturated bool
}

// Input is the capacity model's input.
type Input struct {
	// Stages are ordered by stage index.
	Stages   []Stage
	Series   input.SignalSource
	Interval aggregate.ScrapeInterval
	// ReadyReplicas is the minimum number of ready target replicas during the
	// run; 0 when unknown.
	ReadyReplicas int
	// GPUsPerReplica is the GPUs of one replica; 0 when unknown or none.
	GPUsPerReplica int
}

// Build computes the capacity model. Members the run cannot support are
// omitted, never guessed.
func Build(in Input) (spec.CapacityModel, error) {
	var model spec.CapacityModel
	model.SloMax = sloMax(ladderStages(in.Stages))

	// The knee stays on usable stages: its stage's aggregates are read as a
	// measurement (the self-check refuses a knee on an excluded stage).
	usable := usableStages(in.Stages)
	signals := newSignalReader(in)
	knee, err := findKnee(usable, signals)
	if err != nil {
		return spec.CapacityModel{}, err
	}
	model.Knee = knee
	if knee != nil {
		bottleneck, err := findBottleneck(stageByIndex(usable, knee.StageIndex), signals)
		if err != nil {
			return spec.CapacityModel{}, err
		}
		knee.Bottleneck = bottleneck
	}
	model.PerReplica = perReplica(model.SloMax, in.ReadyReplicas, in.GPUsPerReplica)
	return model, nil
}

func usableStages(stages []Stage) []Stage {
	var usable []Stage
	for _, stage := range stages {
		if !stage.IsExcluded {
			usable = append(usable, stage)
		}
	}
	return usable
}

// ladderStages are the stages the SLO-max scan reads: the usable ones
// plus those where the target fell behind. A saturated stage can only fail an
// SLO (its latencies are the target's own, measured from the late send), so it
// can end the ladder but never extend it.
func ladderStages(stages []Stage) []Stage {
	var ladder []Stage
	for _, stage := range stages {
		if !stage.IsExcluded || stage.IsTargetSaturated {
			ladder = append(ladder, stage)
		}
	}
	return ladder
}

func stageByIndex(stages []Stage, index int) Stage {
	for _, stage := range stages {
		if stage.Window.Index == index {
			return stage
		}
	}
	panic(fmt.Sprintf("stage %d is not in the usable ladder", index))
}

// signalReader reads server-side series for a stage, trimmed to
// whole scrape intervals, and caches each series so a stage is not
// parsed twice.
type signalReader struct {
	series   input.SignalSource
	interval time.Duration
	cache    map[input.Signal][]input.Sample
	missing  map[input.Signal]bool
}

func newSignalReader(in Input) *signalReader {
	return &signalReader{
		series:   in.Series,
		interval: in.Interval.Duration,
		cache:    map[input.Signal][]input.Sample{},
		missing:  map[input.Signal]bool{},
	}
}

func (r *signalReader) samples(signal input.Signal) ([]input.Sample, bool, error) {
	if r.missing[signal] {
		return nil, false, nil
	}
	if samples, ok := r.cache[signal]; ok {
		return samples, true, nil
	}
	samples, found, err := r.series.Signal(signal)
	if err != nil {
		return nil, false, fmt.Errorf("read signal %s: %w", signal, err)
	}
	if !found {
		r.missing[signal] = true
		return nil, false, nil
	}
	r.cache[signal] = samples
	return samples, true, nil
}

// stats returns the statistics of a series inside the stage's trimmed window.
// A low-resolution stage has no server-side statistics.
func (r *signalReader) stats(stage Stage, signal input.Signal, kind aggregate.SeriesKind) (aggregate.WindowStats, bool, error) {
	if stage.IsLowResolution {
		return aggregate.WindowStats{}, false, nil
	}
	samples, found, err := r.samples(signal)
	if err != nil || !found {
		return aggregate.WindowStats{}, false, err
	}
	start, end, ok := aggregate.TrimWindow(stage.Window, kind, r.interval, secondsToDuration(stage.MeanE2ESeconds))
	if !ok {
		return aggregate.WindowStats{}, false, nil
	}
	stats, ok := aggregate.StatsInWindow(samples, start, end)
	return stats, ok, nil
}

// increase is how much a cumulative counter grew during the stage.
func (r *signalReader) increase(stage Stage, signal input.Signal) (float64, bool, error) {
	if stage.IsLowResolution {
		return 0, false, nil
	}
	samples, found, err := r.samples(signal)
	if err != nil || !found {
		return 0, false, err
	}
	start, end, ok := aggregate.TrimWindow(stage.Window, aggregate.Gauge, r.interval, 0)
	if !ok {
		return 0, false, nil
	}
	value, ok := aggregate.CounterIncrease(samples, start, end)
	return value, ok, nil
}

func secondsToDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
