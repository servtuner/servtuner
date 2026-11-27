package pacer

import (
	"fmt"
	"math"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// vegetaRatePrecision scales a requests-per-second value into vegeta's integer
// Freq field. A frequency of rate*1e6 per 1e6 seconds is the same rate, with
// micro-request precision, so fractional stage rates such as 12.5 survive.
const vegetaRatePrecision = 1_000_000

// toVegetaRate expresses a positive requests-per-second value as vegeta's
// integer frequency per time unit. Vegeta treats a zero Freq as an infinite
// rate, so a rate that rounds to zero is rejected instead of passed on.
func toVegetaRate(requestsPerSecond float64) (vegeta.Rate, error) {
	if math.IsNaN(requestsPerSecond) || math.IsInf(requestsPerSecond, 0) || requestsPerSecond <= 0 {
		return vegeta.Rate{}, fmt.Errorf("rate must be positive and finite, got %v requests per second", requestsPerSecond)
	}
	frequency := math.Round(requestsPerSecond * vegetaRatePrecision)
	if frequency < 1 || frequency > math.MaxInt32*vegetaRatePrecision {
		return vegeta.Rate{}, fmt.Errorf("rate %v requests per second is outside the supported range", requestsPerSecond)
	}
	return vegeta.Rate{Freq: int(frequency), Per: vegetaRatePrecision * time.Second}, nil
}

// validateRate is the one rate-range check every pacer applies, so no pacer
// accepts a rate another rejects. A rate below the lower bound could not send
// a single request in any realistic stage, and converting its gap to a
// time.Duration would overflow into a flood.
func validateRate(requestsPerSecond float64) error {
	_, err := toVegetaRate(requestsPerSecond)
	return err
}
