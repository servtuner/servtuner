// Package histogram reads per-stage statistics from raw Prometheus histogram
// and counter series as the catalogs store them: every series of a
// "{__name__=~"x_(bucket|sum|count)"}" query, cumulative and labelled per pod,
// provider, model and so on. A stage's value is the increase of the series
// inside the stage window, summed over the label sets (server-side
// percentiles come from histogram deltas).
package histogram

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
)

// Suffixes of a Prometheus histogram's series.
const (
	bucketSuffix = "_bucket"
	sumSuffix    = "_sum"
	countSuffix  = "_count"
	nameLabel    = "__name__"
	boundLabel   = "le"
)

// Increase is the growth of every series whose name ends in suffix (or of
// every series when suffix is empty) between the last sample at or before
// start and the last sample before end, summed over series. A drop is a
// counter reset: the counter restarted from zero, so its new value is all
// increase.
func Increase(series []input.Series, suffix string, start, end time.Time) (float64, bool) {
	var total float64
	found := false
	for _, one := range series {
		if suffix != "" && !strings.HasSuffix(one.Labels[nameLabel], suffix) {
			continue
		}
		increase, ok := increaseOf(one.Samples, start, end)
		if !ok {
			continue
		}
		total += increase
		found = true
	}
	return total, found
}

// increaseOf walks one series from the last sample at or before start to the
// last sample before end. A series that starts inside the window starts from
// zero: its first sample is all increase.
func increaseOf(samples []input.Sample, start, end time.Time) (float64, bool) {
	var previous float64
	var increase float64
	seen := false
	for _, sample := range samples {
		if !sample.At.Before(end) {
			break
		}
		if !sample.At.After(start) {
			previous, seen = sample.Value, true
			increase = 0
			continue
		}
		if !seen {
			previous, seen = 0, true
		}
		if sample.Value >= previous {
			increase += sample.Value - previous
		} else {
			increase += sample.Value
		}
		previous = sample.Value
	}
	return increase, seen
}

// Mean is the stage mean of a histogram: increase of _sum over increase of
// _count. ok is false when nothing was observed.
func Mean(series []input.Series, start, end time.Time) (float64, bool) {
	sum, hasSum := Increase(series, sumSuffix, start, end)
	count, hasCount := Increase(series, countSuffix, start, end)
	if !hasSum || !hasCount || count <= 0 {
		return 0, false
	}
	return sum / count, true
}

// Count is the number of observations in the stage.
func Count(series []input.Series, start, end time.Time) (float64, bool) {
	return Increase(series, countSuffix, start, end)
}

// Quantile is the stage's q quantile from the bucket increases, interpolated
// linearly inside the bucket as Prometheus's histogram_quantile does. ok is
// false without observations; a quantile in the +Inf bucket is the highest
// finite bound.
func Quantile(series []input.Series, q float64, start, end time.Time) (float64, bool) {
	byBound := map[float64]float64{}
	for _, one := range series {
		if !strings.HasSuffix(one.Labels[nameLabel], bucketSuffix) {
			continue
		}
		bound, err := strconv.ParseFloat(one.Labels[boundLabel], 64)
		if err != nil {
			continue
		}
		increase, ok := increaseOf(one.Samples, start, end)
		if ok {
			byBound[bound] += increase
		}
	}
	bounds := make([]float64, 0, len(byBound))
	for bound := range byBound {
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)
	if len(bounds) == 0 || byBound[bounds[len(bounds)-1]] <= 0 {
		return 0, false
	}
	total := byBound[bounds[len(bounds)-1]] // +Inf holds every observation
	rank := q * total
	lowerBound, lowerCount := 0.0, 0.0
	for _, bound := range bounds {
		count := byBound[bound]
		if count >= rank {
			if math.IsInf(bound, 1) {
				return lowerBound, true
			}
			if count == lowerCount {
				return bound, true
			}
			return lowerBound + (bound-lowerBound)*(rank-lowerCount)/(count-lowerCount), true
		}
		lowerBound, lowerCount = bound, count
	}
	return lowerBound, true
}
