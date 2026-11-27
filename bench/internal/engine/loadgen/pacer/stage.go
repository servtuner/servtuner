package pacer

import (
	"fmt"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// StagePacer is the pacer of one stage with the stage's length. Pacer stops
// (Pace returns stop=true) at Duration on its own clock, which starts at zero
// at the stage start, so an engine cannot overrun the stage.
type StagePacer struct {
	StageIndex int
	Duration   time.Duration
	Pacer      vegeta.Pacer
}

// ForLoad builds one pacer per stage from the RunSpec's load block. seed is the
// RunSpec seed; it only matters for poisson pacing.
func ForLoad(load spec.Load, seed int) ([]StagePacer, error) {
	stagePacers := make([]StagePacer, 0, len(load.Stages))
	for stageIndex, stage := range load.Stages {
		duration, err := stage.Duration.Std()
		if err != nil {
			return nil, fmt.Errorf("stage %d: %w", stageIndex, err)
		}
		pacer, err := forStage(load, seed, stageIndex, duration)
		if err != nil {
			return nil, fmt.Errorf("stage %d: %w", stageIndex, err)
		}
		stagePacers = append(stagePacers, StagePacer{StageIndex: stageIndex, Duration: duration, Pacer: boundedPacer{inner: pacer, duration: duration}})
	}
	return stagePacers, nil
}

func forStage(load spec.Load, seed, stageIndex int, duration time.Duration) (vegeta.Pacer, error) {
	rate := load.Stages[stageIndex].Rate
	switch load.Pacing {
	case spec.PacingConstant, "":
		return Constant(rate)
	case spec.PacingPoisson:
		return Poisson(rate, uint64(seed), stageIndex)
	case spec.PacingLinear:
		if stageIndex == 0 {
			// Stage 0 has no previous stage and holds its rate.
			return Constant(rate)
		}
		return Linear(load.Stages[stageIndex-1].Rate, rate, duration)
	case spec.PacingSine:
		if load.Sine == nil {
			return nil, fmt.Errorf("pacing is sine but load.sine is absent")
		}
		period, err := load.Sine.Period.Std()
		if err != nil {
			return nil, fmt.Errorf("load.sine.period: %w", err)
		}
		return Sine(rate, load.Sine.AmplitudeRatio, period)
	default:
		return nil, fmt.Errorf("unknown pacing %q", load.Pacing)
	}
}

// cumulative is implemented by pacers whose rate is not a straight line, and
// that can integrate it exactly.
type cumulative interface {
	cumulativeHits(elapsed time.Duration) float64
}

// boundedPacer ends a pacer at the stage duration. The inner pacers keep
// producing hits past their end (linear returns nonsense once its ramp is
// over), so the stage boundary is enforced here, once, for all of them.
type boundedPacer struct {
	inner    vegeta.Pacer
	duration time.Duration
}

var _ vegeta.Pacer = boundedPacer{}
var _ cumulative = boundedPacer{}

// Pace stops when the stage is over or the next hit would land at or after its
// end. A hit due on the boundary belongs to the next stage.
func (b boundedPacer) Pace(elapsed time.Duration, hits uint64) (time.Duration, bool) {
	if elapsed >= b.duration {
		return 0, true
	}
	wait, stop := b.inner.Pace(elapsed, hits)
	wait = max(wait, 0)
	if stop || wait >= b.duration-elapsed {
		return 0, true
	}
	return wait, false
}

// Rate passes through to the inner pacer.
func (b boundedPacer) Rate(elapsed time.Duration) float64 {
	return b.inner.Rate(elapsed)
}

func (b boundedPacer) cumulativeHits(elapsed time.Duration) float64 {
	return cumulativeHitsOf(b.inner, elapsed)
}

// cumulativeHitsOf integrates a pacer's rate from 0 to elapsed. Constant,
// Poisson (in expectation) and linear rates are straight lines, so the
// trapezoid is exact; sine integrates itself.
func cumulativeHitsOf(p vegeta.Pacer, elapsed time.Duration) float64 {
	if exact, ok := p.(cumulative); ok {
		return exact.cumulativeHits(elapsed)
	}
	return (p.Rate(0) + p.Rate(elapsed)) / 2 * elapsed.Seconds()
}

// OfferedHits is the number of requests a stage is expected to offer over its
// whole duration: the integral of the rate. The offered-versus-achieved check
// uses it; for sine, mean times duration is wrong when the stage is
// not a whole number of periods.
func OfferedHits(stagePacer StagePacer) float64 {
	return cumulativeHitsOf(stagePacer.Pacer, stagePacer.Duration)
}
