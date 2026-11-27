package pacer

import (
	"fmt"
	"math"
	"sort"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// sine offers a rate that oscillates around a mean, starting at the mean and
// rising: rate(t) = mean + amplitude*sin(2*pi*t/period). vegeta's SinePacer is
// not used because it finds the next hit time with a Newton iteration that
// diverges where the rate approaches zero (amplitude ratios near 1), flooding
// the target. This type solves the same equation by bisection, which cannot
// diverge because the cumulative hit count is monotone.
type sine struct {
	meanRequestsPerSecond      float64
	amplitudeRequestsPerSecond float64
	periodSeconds              float64
}

var (
	_ vegeta.Pacer = sine{}
	_ cumulative   = sine{}
)

// Sine returns a pacer whose rate oscillates around meanRequestsPerSecond with
// a peak deviation of amplitudeRatio times the mean. A ratio of 1 touches zero
// at the trough, which is allowed.
func Sine(meanRequestsPerSecond, amplitudeRatio float64, period time.Duration) (vegeta.Pacer, error) {
	if period <= 0 {
		return nil, fmt.Errorf("sine pacer needs a positive period, got %s", period)
	}
	if math.IsNaN(amplitudeRatio) || amplitudeRatio <= 0 || amplitudeRatio > 1 {
		return nil, fmt.Errorf("sine pacer needs an amplitude ratio in (0, 1], got %v", amplitudeRatio)
	}
	if err := validateRate(meanRequestsPerSecond); err != nil {
		return nil, fmt.Errorf("sine pacer mean rate: %w", err)
	}
	return sine{
		meanRequestsPerSecond:      meanRequestsPerSecond,
		amplitudeRequestsPerSecond: amplitudeRatio * meanRequestsPerSecond,
		periodSeconds:              period.Seconds(),
	}, nil
}

// Rate is the instantaneous rate at the given elapsed time.
func (s sine) Rate(elapsed time.Duration) float64 {
	return s.meanRequestsPerSecond + s.amplitudeRequestsPerSecond*math.Sin(2*math.Pi*elapsed.Seconds()/s.periodSeconds)
}

// cumulativeHits is the integral of Rate from 0 to elapsed. It never decreases
// because the amplitude does not exceed the mean.
func (s sine) cumulativeHits(elapsed time.Duration) float64 {
	seconds := elapsed.Seconds()
	radiansPerSecond := 2 * math.Pi / s.periodSeconds
	return s.meanRequestsPerSecond*seconds + s.amplitudeRequestsPerSecond/radiansPerSecond*(1-math.Cos(radiansPerSecond*seconds))
}

// Pace returns how long to wait for hit number hits+1 (the first hit is number
// 1). When the sender is behind schedule it returns zero, as the other pacers
// do. The wait is never negative.
func (s sine) Pace(elapsed time.Duration, hits uint64) (time.Duration, bool) {
	target := float64(hits + 1)
	if s.cumulativeHits(elapsed) >= target {
		return 0, false
	}
	// cumulativeHits(t) >= mean*t, so the target is reached by target/mean.
	upperBound := time.Duration(math.Ceil(target/s.meanRequestsPerSecond*float64(time.Second))) + 1
	if upperBound <= elapsed {
		return 0, false
	}
	reached := elapsed + time.Duration(sort.Search(int(upperBound-elapsed), func(offset int) bool {
		return s.cumulativeHits(elapsed+time.Duration(offset)) >= target
	}))
	return reached - elapsed, false
}
