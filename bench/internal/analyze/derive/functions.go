package derive

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// function is one formula function. arity -1 takes one or more arguments.
type function struct {
	arity int
	apply func(args []float64) (float64, error)
}

// roundingSlack absorbs binary floating-point error before rounding:
// 190 × 1.1 is 209.00000000000003, and its ceiling must be 209, not 210.
const roundingSlack = 1e-9

// functions are the only ones a formula may call. Each is documented where a
// formula of the analysis needs it.
var functions = map[string]function{
	"ceil": {1, func(a []float64) (float64, error) {
		return math.Ceil(a[0] - roundingSlack*math.Max(1, math.Abs(a[0]))), nil
	}},
	"floor": {1, func(a []float64) (float64, error) {
		return math.Floor(a[0] + roundingSlack*math.Max(1, math.Abs(a[0]))), nil
	}},
	"sqrt": {1, func(a []float64) (float64, error) {
		if a[0] < 0 {
			return 0, errors.New("sqrt of a negative number")
		}
		return math.Sqrt(a[0]), nil
	}},
	"max": {-1, func(a []float64) (float64, error) { return extreme(a, math.Max), nil }},
	"min": {-1, func(a []float64) (float64, error) { return extreme(a, math.Min), nil }},
	// clamp(x, lo, hi), as in "clamped to [C_emp, C_cons]".
	"clamp": {3, func(a []float64) (float64, error) {
		if a[1] > a[2] {
			return 0, fmt.Errorf("clamp bounds are reversed: [%s, %s]", FormatNumber(a[1]), FormatNumber(a[2]))
		}
		return math.Min(math.Max(a[0], a[1]), a[2]), nil
	}},
	// roundup(x, step): up to a multiple of step, as in "rounded up to 50m".
	"roundup": {2, func(a []float64) (float64, error) {
		if a[1] <= 0 {
			return 0, errors.New("roundup step must be positive")
		}
		steps := a[0] / a[1]
		return snap(math.Ceil(steps-roundingSlack*math.Max(1, math.Abs(steps))) * a[1]), nil
	}},
	// poisson_q(p, mean): the smallest n with P(N ≤ n) ≥ p for N ~ Poisson(mean),
	// the queueing model's "Poisson check".
	"poisson_q": {2, func(a []float64) (float64, error) { return PoissonQuantile(a[0], a[1]) }},
	// erlang_c(servers, load): the probability an arrival waits in an M/M/c
	// queue with c servers and offered load a = λ·W (approximate).
	"erlang_c": {2, func(a []float64) (float64, error) { return ErlangC(a[0], a[1]) }},
}

// snap removes binary noise from a product of a step: 6 × 0.05 is
// 0.30000000000000004, which a recommendation must show as 0.3. It keeps
// twelve significant digits, far beyond any step a formula uses.
func snap(value float64) float64 {
	if value == 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return value
	}
	scale := math.Pow(10, 12-math.Ceil(math.Log10(math.Abs(value))))
	return math.Round(value*scale) / scale
}

func extreme(values []float64, pick func(a, b float64) float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		result = pick(result, value)
	}
	return result
}

// ErlangC is the probability that an arrival waits in an M/M/c queue with
// servers c and offered load a (Erlangs). It is 1 when a ≥ c: the queue grows
// without bound. Computed with the stable Erlang-B recursion.
func ErlangC(servers, load float64) (float64, error) {
	if servers < 1 || servers != math.Trunc(servers) {
		return 0, fmt.Errorf("erlang_c needs a whole number of servers ≥ 1, got %s", FormatNumber(servers))
	}
	if load < 0 {
		return 0, errors.New("erlang_c load must not be negative")
	}
	if load >= servers {
		return 1, nil
	}
	blocking := 1.0 // Erlang B with zero servers
	for k := 1.0; k <= servers; k++ {
		blocking = load * blocking / (k + load*blocking)
	}
	rho := load / servers
	return blocking / (1 - rho + rho*blocking), nil
}

// poissonExactLimit is the mean up to which the quantile is summed exactly;
// above it the normal approximation with a continuity correction is within
// one of the exact answer for p ≤ 0.9999.
const poissonExactLimit = 1000

// PoissonQuantile is the smallest n with P(N ≤ n) ≥ p for N ~ Poisson(mean).
func PoissonQuantile(p, mean float64) (float64, error) {
	if p <= 0 || p >= 1 {
		return 0, fmt.Errorf("poisson quantile probability %s must be in (0, 1)", FormatNumber(p))
	}
	if mean < 0 {
		return 0, errors.New("poisson mean must not be negative")
	}
	if mean == 0 {
		return 0, nil
	}
	if mean > poissonExactLimit {
		z := normalQuantile(p)
		return math.Max(0, math.Ceil(mean+z*math.Sqrt(mean)-0.5)), nil
	}
	// Sum the probabilities in log space, so a large mean does not underflow e^-mean.
	logTerm := -mean
	cumulative := math.Exp(logTerm)
	n := 0.0
	for cumulative < p {
		n++
		logTerm += math.Log(mean) - math.Log(n)
		cumulative += math.Exp(logTerm)
		if n > mean+50*math.Sqrt(mean)+50 {
			break // numerical floor; the quantile is certainly below this
		}
	}
	return n, nil
}

// normalQuantile is the standard normal inverse CDF (Acklam's rational
// approximation, relative error below 1.2e-9).
func normalQuantile(p float64) float64 {
	a := []float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02, 1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := []float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02, 6.680131188771972e+01, -1.328068155288572e+01}
	c := []float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00, -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := []float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00, 3.754408661907416e+00}
	const low = 0.02425
	switch {
	case p < low:
		q := math.Sqrt(-2 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p > 1-low:
		q := math.Sqrt(-2 * math.Log(1-p))
		return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	default:
		q := p - 0.5
		r := q * q
		return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q / (((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
	}
}

func sortStrings(values []string) { sort.Strings(values) }
