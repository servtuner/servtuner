// Package stats holds the small statistics shared by the analysis code and the
// engines, so neither has to import the other.
package stats

import (
	"math"
	"sort"
)

// NearestRank returns the nearest-rank percentile of values that are already
// sorted ascending. Nearest rank always returns an observed value, which keeps
// the percentile equal to what a bootstrap resample can produce. It returns 0
// for an empty slice, so callers check the sample count first.
func NearestRank(sorted []float64, fraction float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[RankIndex(len(sorted), fraction)]
}

// RankIndex is the zero-based index of the nearest-rank percentile in a sample
// of the given size.
func RankIndex(size int, fraction float64) int {
	return min(max(int(math.Ceil(fraction*float64(size)))-1, 0), size-1)
}

// Percentile is NearestRank on an unsorted copy of values.
func Percentile(values []float64, fraction float64) float64 {
	return NearestRank(SortedCopy(values), fraction)
}

// SortedCopy returns values sorted ascending, leaving the input untouched.
func SortedCopy(values []float64) []float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted
}
