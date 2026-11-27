// Package pacer holds the arrival-rate pacers of the loadgen engine. Open-loop scheduling means the pacer, not the responses, decides when
// the next request is due. Pacers implement vegeta's Pacer interface, so
// vegeta's other pacers (sine, linear) can be used interchangeably.
package pacer

import (
	"fmt"
	"math"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// constant offers a fixed rate. vegeta's own ConstantPacer takes an integer
// frequency per time unit and truncates elapsed/Per with integer division, so
// it cannot express a rate such as 500.5 requests per second without losing
// its catch-up behaviour. This type keeps the same semantics in floating point.
type constant struct {
	requestsPerSecond float64
}

var _ vegeta.Pacer = constant{}

// Constant returns a pacer that offers requestsPerSecond requests per second.
func Constant(requestsPerSecond float64) (vegeta.Pacer, error) {
	if err := validateRate(requestsPerSecond); err != nil {
		return nil, fmt.Errorf("constant pacer: %w", err)
	}
	return constant{requestsPerSecond: requestsPerSecond}, nil
}

// Pace returns how long to wait before the next hit. When the sender is behind
// schedule it returns zero, so a slow moment is caught up rather than lost.
func (c constant) Pace(elapsed time.Duration, hits uint64) (time.Duration, bool) {
	expectedHits := c.requestsPerSecond * elapsed.Seconds()
	if float64(hits) < math.Floor(expectedHits) {
		return 0, false
	}
	nextHitAt := time.Duration(float64(hits+1) / c.requestsPerSecond * float64(time.Second))
	return nextHitAt - elapsed, false
}

// Rate is the same at every moment of the attack.
func (c constant) Rate(time.Duration) float64 {
	return c.requestsPerSecond
}
