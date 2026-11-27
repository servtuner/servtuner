package compare

import (
	"fmt"
	"sort"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// NoiseFloor is the default run-to-run noise. With a single
// sample there is no measured variance, so only a lead of more than twice
// this is called out.
const NoiseFloor = 0.05

// highConfidenceRepeats is the repeats every configuration needs for a high
// confidence result.
const highConfidenceRepeats = 3

// judgement is what a ranking's significance and confidence depend on beyond
// the two intervals.
type judgement struct {
	comparable   bool
	minRepeats   int
	fullCoverage bool
}

type ranked struct {
	id       string
	interval spec.ObjectiveInterval
}

// rank orders the configurations on one objective, best first, and decides
// whether the best one wins. It returns false when some configuration has no
// value for the objective: a ranking over a subset would hide the gap.
func rank(configurations []spec.KnobConfiguration, name spec.ObjectiveName, judge judgement) (spec.ObjectiveRanking, bool) {
	var entries []ranked
	for _, config := range configurations {
		interval, ok := objectiveOf(config, name)
		if !ok || interval.Value == nil {
			return spec.ObjectiveRanking{}, false
		}
		entries = append(entries, ranked{config.Id, interval})
	}
	dir := direction(name)
	sort.SliceStable(entries, func(i, j int) bool { return better(*entries[i].interval.Value, *entries[j].interval.Value, dir) })
	ranking := spec.ObjectiveRanking{Objective: name, Direction: dir, Order: []spec.RankedConfiguration{}, Confidence: spec.ComparisonConfidenceLow}
	for _, entry := range entries {
		ranking.Order = append(ranking.Order, spec.RankedConfiguration{ConfigurationId: entry.id, Value: *entry.interval.Value})
	}
	switch {
	case !judge.comparable:
		ranking.Reason = "the runs are not comparable (see comparability): nothing wins"
	case len(entries) < 2:
		ranking.Reason = "only one configuration: there is nothing to compare it with"
	default:
		ranking.IsSignificant, ranking.Reason = significant(entries[0], entries[1], dir)
	}
	if ranking.IsSignificant {
		winner := entries[0].id
		ranking.WinnerId = &winner
		ranking.Confidence = spec.ComparisonConfidenceMedium
		if judge.minRepeats >= highConfidenceRepeats && judge.fullCoverage {
			ranking.Confidence = spec.ComparisonConfidenceHigh
		}
	}
	return ranking, true
}

// significant applies the comparison rule: intervals that do not overlap, or with a
// single sample, a lead of more than twice the noise floor.
func significant(best, runnerUp ranked, dir spec.ObjectiveDirection) (bool, string) {
	b, r := best.interval, runnerUp.interval
	if r.IsLowerBound && dir == spec.ObjectiveDirectionMaximize {
		return false, fmt.Sprintf("%s never failed its SLOs within the ladder, so its value is a lower bound and may exceed %s's: extend the ladder", runnerUp.id, best.id)
	}
	if r.IsUpperBound && dir == spec.ObjectiveDirectionMinimize {
		return false, fmt.Sprintf("%s never failed its SLOs within the ladder, so its value is an upper bound and may be below %s's: extend the ladder", runnerUp.id, best.id)
	}
	if b.CiLow == nil || r.CiLow == nil {
		lead := relativeLead(*b.Value, *r.Value, dir)
		threshold := 2 * NoiseFloor
		if lead > threshold {
			return true, fmt.Sprintf("single sample: %s leads %s by %.1f%%, more than twice the %.0f%% noise floor", best.id, runnerUp.id, 100*lead, 100*NoiseFloor)
		}
		return false, fmt.Sprintf("single sample: %s leads %s by %.1f%%, within twice the %.0f%% noise floor; run repeats to resolve it", best.id, runnerUp.id, 100*lead, 100*NoiseFloor)
	}
	separated := *b.CiLow > *r.CiHigh
	if dir == spec.ObjectiveDirectionMinimize {
		separated = *b.CiHigh < *r.CiLow
	}
	intervals := fmt.Sprintf("%s's 95%% interval (%s to %s) and %s's (%s to %s)", best.id, format(*b.CiLow), format(*b.CiHigh), runnerUp.id, format(*r.CiLow), format(*r.CiHigh))
	if separated {
		return true, intervals + " do not overlap"
	}
	return false, intervals + " overlap: no configuration wins"
}

// relativeLead is how much better best is than runnerUp, as a fraction of
// runnerUp.
func relativeLead(best, runnerUp float64, dir spec.ObjectiveDirection) float64 {
	if runnerUp == 0 {
		if best == runnerUp {
			return 0
		}
		if dir == spec.ObjectiveDirectionMaximize {
			return 1
		}
		return 0
	}
	if dir == spec.ObjectiveDirectionMaximize {
		return (best - runnerUp) / runnerUp
	}
	return (runnerUp - best) / runnerUp
}

func better(a, b float64, dir spec.ObjectiveDirection) bool {
	if dir == spec.ObjectiveDirectionMinimize {
		return a < b
	}
	return a > b
}

func format(value float64) string { return fmt.Sprintf("%.4g", value) }

func objectiveOf(config spec.KnobConfiguration, name spec.ObjectiveName) (spec.ObjectiveInterval, bool) {
	for _, estimate := range config.Objectives {
		if estimate.Objective == name {
			return estimate.Estimate, true
		}
	}
	return spec.ObjectiveInterval{}, false
}

// pareto returns the configurations not dominated on (primary objective, p95
// TTFT at the reference stage), in configuration order, or nil when either is
// missing for some configuration.
func pareto(configurations []spec.KnobConfiguration, primary spec.ObjectiveName) []string {
	type point struct {
		id              string
		objective, ttft float64
	}
	var points []point
	for _, config := range configurations {
		interval, ok := objectiveOf(config, primary)
		if !ok || interval.Value == nil || config.ReferenceTtftP95Seconds == nil || config.ReferenceTtftP95Seconds.Value == nil {
			return nil
		}
		points = append(points, point{config.Id, *interval.Value, *config.ReferenceTtftP95Seconds.Value})
	}
	dir := direction(primary)
	atLeastAsGood := func(a, b float64) bool { return a == b || better(a, b, dir) }
	var front []string
	for _, p := range points {
		dominated := false
		for _, q := range points {
			if q.id == p.id {
				continue
			}
			if atLeastAsGood(q.objective, p.objective) && q.ttft <= p.ttft && (better(q.objective, p.objective, dir) || q.ttft < p.ttft) {
				dominated = true
				break
			}
		}
		if !dominated {
			front = append(front, p.id)
		}
	}
	return front
}

// recommend is the primary objective's winner, with only the varied knobs,
// when the runs are comparable and the win is significant.
func recommend(result spec.Comparison, configurations []configuration) *spec.KnobRecommendation {
	if !result.Comparability.IsComparable || len(result.Rankings) == 0 {
		return nil
	}
	primary := result.Rankings[0]
	if primary.Objective != result.Objective || primary.WinnerId == nil {
		return nil
	}
	for _, config := range configurations {
		if config.id != *primary.WinnerId {
			continue
		}
		knobs := spec.KnobRecommendationKnobs{}
		for _, name := range result.VariedKnobs {
			if value, ok := config.knobs[name]; ok {
				knobs[name] = value
			}
		}
		return &spec.KnobRecommendation{
			ConfigurationId: config.id, Knobs: knobs, Objective: result.Objective,
			Confidence: primary.Confidence, Tier: spec.KnobRecommendationTierEmpirical,
		}
	}
	return nil
}
