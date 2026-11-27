// Package plan is the capacity planner: from measured
// capacity models and a traffic and SLO request it decides replicas, the HPA,
// totals and queue settings per tier, through the same derivation engine as
// the recommendations, so every number is explainable and tunable.
package plan

import (
	"fmt"
	"sort"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// threshold is one SLO of the request: a latency percentile in seconds, or
// the error rate.
type threshold struct {
	name  string
	limit float64
	value func(spec.ModelStage) (float64, bool)
}

// thresholdsOf turns the request's SLOs into checks against stage curves.
func thresholdsOf(slos spec.PlanRequestSlos) ([]threshold, error) {
	var thresholds []threshold
	latency := func(name string, duration *spec.Duration, pick func(spec.ModelStage) *spec.LatencyPoint, percentile func(*spec.LatencyPoint) *float64) error {
		if duration == nil {
			return nil
		}
		parsed, err := duration.Std()
		if err != nil {
			return fmt.Errorf("slos.%s: %w", name, err)
		}
		thresholds = append(thresholds, threshold{name: name, limit: parsed.Seconds(), value: func(stage spec.ModelStage) (float64, bool) {
			point := pick(stage)
			if point == nil || percentile(point) == nil {
				return 0, false
			}
			return *percentile(point), true
		}})
		return nil
	}
	ttft := func(s spec.ModelStage) *spec.LatencyPoint { return s.TtftSeconds }
	e2e := func(s spec.ModelStage) *spec.LatencyPoint { return s.E2ESeconds }
	itl := func(s spec.ModelStage) *spec.LatencyPoint { return s.ItlSeconds }
	p95 := func(p *spec.LatencyPoint) *float64 { return p.P95 }
	p99 := func(p *spec.LatencyPoint) *float64 { return p.P99 }
	for _, err := range []error{
		latency("ttft_p95", slos.TtftP95, ttft, p95), latency("ttft_p99", slos.TtftP99, ttft, p99),
		latency("e2e_p95", slos.E2EP95, e2e, p95), latency("e2e_p99", slos.E2EP99, e2e, p99),
		latency("itl_p95", slos.ItlP95, itl, p95),
	} {
		if err != nil {
			return nil, err
		}
	}
	if slos.MaxErrorRate != nil {
		thresholds = append(thresholds, threshold{name: "max_error_rate", limit: *slos.MaxErrorRate, value: func(stage spec.ModelStage) (float64, bool) {
			return stage.ErrorRate, true
		}})
	}
	return thresholds, nil
}

// capacity is a model's per-replica SLO-max at the requested SLO.
type capacity struct {
	rps            float64
	stageIndex     int
	isInterpolated bool
	limitingSLO    string
	// isAtLeast: no measured stage failed, so the real limit is higher.
	isAtLeast bool
}

// sloMaxPerReplica walks the usable stages in load order: the SLO-max is the
// last stage that meets every SLO before the first that fails one, moved
// linearly towards the failing stage up to where the limiting SLO is crossed.
// It never goes past a measured stage (guard rail 1). A stage whose curve
// lacks a percentile the SLO needs cannot be judged and is skipped.
func sloMaxPerReplica(model spec.RunCapacityModel, thresholds []threshold) (capacity, error) {
	var stages []spec.ModelStage
	for _, stage := range model.Stages {
		if stage.IsUsable && stage.RpsPerReplica > 0 {
			stages = append(stages, stage)
		}
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].RpsPerReplica < stages[j].RpsPerReplica })
	var passing *spec.ModelStage
	for i := range stages {
		stage := stages[i]
		meets, judged := meetsAll(stage, thresholds)
		if !judged {
			continue
		}
		if meets {
			passing = &stages[i]
			continue
		}
		if passing == nil {
			return capacity{}, fmt.Errorf("the SLO is stricter than the lightest measured stage (%s RPS per replica fails it): measure a lighter stage to plan for it",
				derive.FormatNumber(stage.RpsPerReplica))
		}
		return interpolate(*passing, stage, thresholds), nil
	}
	if passing == nil {
		return capacity{}, fmt.Errorf("no usable stage has the percentiles the SLO needs")
	}
	return capacity{rps: passing.RpsPerReplica, stageIndex: passing.StageIndex, isAtLeast: true}, nil
}

func meetsAll(stage spec.ModelStage, thresholds []threshold) (meets, judged bool) {
	meets = true
	for _, check := range thresholds {
		value, ok := check.value(stage)
		if !ok {
			return false, false
		}
		if value > check.limit {
			meets = false
		}
	}
	return meets, true
}

// interpolate finds, for each SLO the failing stage breaks, the load at which
// its curve crosses the limit between the two stages, and keeps the lowest.
func interpolate(passing, failing spec.ModelStage, thresholds []threshold) capacity {
	result := capacity{rps: failing.RpsPerReplica, stageIndex: passing.StageIndex, isInterpolated: true}
	for _, check := range thresholds {
		low, _ := check.value(passing)
		high, _ := check.value(failing)
		if high <= check.limit || high <= low {
			continue
		}
		crossing := passing.RpsPerReplica + (check.limit-low)*(failing.RpsPerReplica-passing.RpsPerReplica)/(high-low)
		if crossing < result.rps {
			result.rps, result.limitingSLO = crossing, check.name
		}
	}
	return result
}
