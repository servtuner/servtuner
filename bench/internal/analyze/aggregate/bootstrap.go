package aggregate

import (
	"fmt"
	"hash/fnv"
	"math/bits"
	"sort"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// Bootstrap parameters: 95% confidence intervals from the per-request records.
const (
	// BootstrapResamples is the number of resamples of each interval. 1000 puts
	// the 2.5th and 97.5th percentiles of the resampled statistic on 25 values
	// each, which is steady to about the third significant digit.
	BootstrapResamples = 1000
	// BootstrapLowerFraction and BootstrapUpperFraction bound the 95% interval.
	BootstrapLowerFraction = 0.025
	BootstrapUpperFraction = 0.975
	// BootstrapMaxPoints caps the sample a resample draws from, so the cost
	// stays near 1000 x 20k even for a pooled ITL of millions of tokens. A
	// larger sample is first thinned to evenly spaced order statistics, which
	// keeps its distribution and makes the interval a little wider, never
	// narrower.
	BootstrapMaxPoints = 20000
)

// PercentileRule is one reported percentile and the Rule A sample count it
// needs: about 10 observations beyond the
// percentile, and never fewer than the table's 100 for p50.
type PercentileRule struct {
	Name       spec.PercentileName
	Fraction   float64
	MinSamples int
}

// Percentiles are the four reported percentiles. p90 is not in the
// minimum-samples table; it follows the table's own rule of 10 observations beyond the
// percentile, 10 / (1 - 0.90) = 100.
var Percentiles = []PercentileRule{
	{Name: spec.PercentileNameP50, Fraction: 0.50, MinSamples: 100},
	{Name: spec.PercentileNameP90, Fraction: 0.90, MinSamples: 100},
	{Name: spec.PercentileNameP95, Fraction: 0.95, MinSamples: 200},
	{Name: spec.PercentileNameP99, Fraction: 0.99, MinSamples: 1000},
}

// RuleFor returns the Rule A requirement of a percentile.
func RuleFor(name spec.PercentileName) PercentileRule {
	for _, rule := range Percentiles {
		if rule.Name == name {
			return rule
		}
	}
	panic(fmt.Sprintf("unknown percentile %q", name))
}

// splitMix64 is a small fixed generator (Steele, Lea and Flood). It is written
// out here, not taken from math/rand, because the bootstrap must give the same
// intervals when the bundle is analysed again years later, and a standard
// library generator's stream is not a compatibility promise.
type splitMix64 struct{ state uint64 }

func (g *splitMix64) next() uint64 {
	g.state += 0x9e3779b97f4a7c15
	z := g.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// below returns a value in [0, bound) without modulo bias worth caring about
// for bound <= 20000 (Lemire's multiply-shift without the rejection step: the
// bias is below 2^-49).
func (g *splitMix64) below(bound int) int {
	high, _ := bits.Mul64(g.next(), uint64(bound))
	return int(high)
}

// seedFor derives the generator state from the run id and a label naming the
// statistic ("stage-2/ttft"), so every interval has its own stream and none
// depends on the order in which the intervals are computed.
func seedFor(runID, label string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(runID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(label))
	return hash.Sum64()
}

// Summarize builds the latency summary of values (seconds): mean, max, and the
// four percentiles with percentile-bootstrap 95% confidence intervals. A
// percentile whose sample is below its Rule A count has no number, only the
// reason. It returns nil for no values.
func Summarize(runID, label string, values []float64) *spec.LatencySummary {
	if len(values) == 0 {
		return nil
	}
	sorted := stats.SortedCopy(values)
	mean := Mean(sorted)
	maximum := sorted[len(sorted)-1]
	summary := &spec.LatencySummary{MeanSeconds: &mean, MaxSeconds: &maximum}

	var enough []PercentileRule
	for _, rule := range Percentiles {
		if len(sorted) >= rule.MinSamples {
			enough = append(enough, rule)
		}
	}
	intervals := bootstrapIntervals(seedFor(runID, label), sorted, enough)
	targets := map[spec.PercentileName]*spec.BootstrapInterval{
		spec.PercentileNameP50: &summary.P50Seconds,
		spec.PercentileNameP90: &summary.P90Seconds,
		spec.PercentileNameP95: &summary.P95Seconds,
		spec.PercentileNameP99: &summary.P99Seconds,
	}
	for _, rule := range Percentiles {
		target := targets[rule.Name]
		if len(sorted) < rule.MinSamples {
			reason := fmt.Sprintf("%d samples, %s needs at least %d", len(sorted), rule.Name, rule.MinSamples)
			*target = spec.BootstrapInterval{Samples: len(sorted), InsufficientSamplesReason: &reason}
			continue
		}
		*target = intervals[rule.Name]
	}
	return summary
}

// bootstrapIntervals resamples sorted with replacement BootstrapResamples
// times and returns, for each rule, the point value on all of sorted and the
// 2.5th and 97.5th percentiles of the resampled statistic. A resample is drawn
// as counts per sorted index, so each percentile is found in one pass without
// sorting the resample.
func bootstrapIntervals(seed uint64, sorted []float64, rules []PercentileRule) map[spec.PercentileName]spec.BootstrapInterval {
	result := map[spec.PercentileName]spec.BootstrapInterval{}
	if len(rules) == 0 {
		return result
	}
	pool := thin(sorted, BootstrapMaxPoints)
	generator := splitMix64{state: seed}
	counts := make([]int32, len(pool))
	resampled := make([][]float64, len(rules))
	for i := range resampled {
		resampled[i] = make([]float64, 0, BootstrapResamples)
	}
	for range BootstrapResamples {
		clear(counts)
		for range pool {
			counts[generator.below(len(pool))]++
		}
		for i, rule := range rules {
			resampled[i] = append(resampled[i], pool[resampleRank(counts, stats.RankIndex(len(pool), rule.Fraction))])
		}
	}
	for i, rule := range rules {
		draws := resampled[i]
		sort.Float64s(draws)
		value := stats.NearestRank(sorted, rule.Fraction)
		// A percentile bootstrap interval can miss the point estimate, rarely,
		// on discrete data or after thinning. The reported interval always
		// contains the value it belongs to (schema/CLAUDE.md self-check).
		low := min(stats.NearestRank(draws, BootstrapLowerFraction), value)
		high := max(stats.NearestRank(draws, BootstrapUpperFraction), value)
		result[rule.Name] = spec.BootstrapInterval{Value: &value, CiLow: &low, CiHigh: &high, Samples: len(sorted)}
	}
	return result
}

// resampleRank returns the sorted index holding the order statistic at the
// zero-based rank of a resample given as counts per index.
func resampleRank(counts []int32, rank int) int {
	var cumulative int
	for index, count := range counts {
		cumulative += int(count)
		if cumulative > rank {
			return index
		}
	}
	return len(counts) - 1
}

// thin returns at most limit evenly spaced order statistics of sorted.
func thin(sorted []float64, limit int) []float64 {
	if len(sorted) <= limit {
		return sorted
	}
	thinned := make([]float64, limit)
	for i := range thinned {
		thinned[i] = sorted[int(int64(i)*int64(len(sorted))/int64(limit))]
	}
	return thinned
}
