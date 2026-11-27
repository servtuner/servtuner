package pacer

import (
	"fmt"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// Linear returns a pacer whose rate ramps from startRequestsPerSecond to
// endRequestsPerSecond across stageDuration (the load.pacing "linear" rule in
// the RunSpec schema). It reuses vegeta's LinearPacer; elapsed time is counted
// from the start of the stage.
func Linear(startRequestsPerSecond, endRequestsPerSecond float64, stageDuration time.Duration) (vegeta.Pacer, error) {
	if stageDuration <= 0 {
		return nil, fmt.Errorf("linear pacer needs a positive stage duration, got %s", stageDuration)
	}
	startRate, err := toVegetaRate(startRequestsPerSecond)
	if err != nil {
		return nil, fmt.Errorf("linear pacer start rate: %w", err)
	}
	if _, err := toVegetaRate(endRequestsPerSecond); err != nil {
		return nil, fmt.Errorf("linear pacer end rate: %w", err)
	}
	slope := (endRequestsPerSecond - startRequestsPerSecond) / stageDuration.Seconds()
	return vegeta.LinearPacer{StartAt: startRate, Slope: slope}, nil
}
