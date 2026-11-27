package analyze

import (
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/servtuner/servtuner/bench/internal/analyze/quality"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// relativeTolerance absorbs float rounding in the derived-value checks.
const relativeTolerance = 1e-9

// SelfCheck verifies what the schema cannot express (schema/CLAUDE.md) and the
// internal consistency of the result: confidence intervals contain their
// value, the failed stage lies above the valid one, and the capacity model
// refers only to stages that were not excluded. A result that fails is a bug
// in the analyzer and is never written. Every violation is listed.
func SelfCheck(result spec.AnalysisResult) error {
	var problems []error
	report := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	qualityByStage := map[int]spec.StageQuality{}
	for _, stage := range result.Quality.Stages {
		qualityByStage[stage.StageIndex] = stage
	}
	aggregateByStage := map[int]spec.StageAggregate{}
	for _, stage := range result.Stages {
		if _, duplicate := aggregateByStage[stage.StageIndex]; duplicate {
			report("stage %d is aggregated twice", stage.StageIndex)
		}
		aggregateByStage[stage.StageIndex] = stage
		if _, found := qualityByStage[stage.StageIndex]; !found {
			report("stage %d has an aggregate but no quality record", stage.StageIndex)
		}
		checkStageAggregate(stage, report)
	}
	if len(result.Quality.Stages) != len(result.Stages) {
		report("quality has %d stages but there are %d aggregates", len(result.Quality.Stages), len(result.Stages))
	}
	checkCapacity(result.Capacity, qualityByStage, aggregateByStage, report)
	checkFinite(reflect.ValueOf(result), "result", report)
	return errors.Join(problems...)
}

func checkStageAggregate(stage spec.StageAggregate, report func(string, ...any)) {
	for _, metric := range []struct {
		name    string
		summary *spec.LatencySummary
	}{{"ttft", stage.Ttft}, {"itl", stage.Itl}, {"tpot", stage.Tpot}, {"e2e", stage.E2E}} {
		if metric.summary == nil {
			continue
		}
		for _, entry := range []struct {
			name     string
			interval spec.BootstrapInterval
		}{{"p50", metric.summary.P50Seconds}, {"p90", metric.summary.P90Seconds}, {"p95", metric.summary.P95Seconds}, {"p99", metric.summary.P99Seconds}} {
			checkInterval(fmt.Sprintf("stage %d %s %s", stage.StageIndex, metric.name, entry.name), entry.interval, report)
		}
	}
	if stage.Errors > stage.Requests {
		report("stage %d has %d errors among %d requests", stage.StageIndex, stage.Errors, stage.Requests)
	}
}

// checkInterval asserts ci_low <= value <= ci_high, and that the three numbers
// are present or absent together (the schema enforces the latter too; this
// keeps the check local to the producer).
func checkInterval(where string, interval spec.BootstrapInterval, report func(string, ...any)) {
	hasAll := interval.Value != nil && interval.CiLow != nil && interval.CiHigh != nil
	hasNone := interval.Value == nil && interval.CiLow == nil && interval.CiHigh == nil
	switch {
	case hasNone:
		if interval.InsufficientSamplesReason == nil {
			report("%s has neither a value nor an insufficient_samples reason", where)
		}
	case !hasAll:
		report("%s has some but not all of value, ci_low and ci_high", where)
	case *interval.CiLow > *interval.Value || *interval.Value > *interval.CiHigh:
		report("%s violates ci_low <= value <= ci_high: %g, %g, %g", where, *interval.CiLow, *interval.Value, *interval.CiHigh)
	case interval.InsufficientSamplesReason != nil:
		report("%s has both a value and an insufficient_samples reason", where)
	}
}

func checkCapacity(model spec.CapacityModel, qualityByStage map[int]spec.StageQuality, aggregateByStage map[int]spec.StageAggregate, report func(string, ...any)) {
	requireUsable := func(what string, index int) {
		stage, found := qualityByStage[index]
		switch {
		case !found:
			report("%s refers to stage %d, which does not exist", what, index)
		case stage.IsExcluded:
			report("%s refers to stage %d, which was excluded", what, index)
		}
	}
	if slo := model.SloMax; slo != nil {
		requireUsable("slo_max", slo.ValidStageIndex)
		if slo.IsInterpolated && slo.FailedStageIndex == nil {
			report("slo_max is interpolated but has no failed_stage_index")
		}
		if slo.FailedStageIndex != nil {
			// A stage where the target fell behind is excluded, yet it is the
			// failing stage the SLO-max scan stops at (target_saturated).
			if failed, found := qualityByStage[*slo.FailedStageIndex]; !found || !quality.IsTargetSaturated(failed) {
				requireUsable("slo_max failed stage", *slo.FailedStageIndex)
			}
			if *slo.FailedStageIndex <= slo.ValidStageIndex {
				report("slo_max failed_stage_index %d is not above valid_stage_index %d", *slo.FailedStageIndex, slo.ValidStageIndex)
			}
			if slo.IsInterpolated {
				low, high := aggregateByStage[slo.ValidStageIndex].AchievedRps, aggregateByStage[*slo.FailedStageIndex].AchievedRps
				if slo.Rps < low-relativeTolerance*low || slo.Rps > high+relativeTolerance*high {
					report("interpolated slo_max rps %g lies outside the bracket [%g, %g]", slo.Rps, low, high)
				}
			}
		}
	}
	if knee := model.Knee; knee != nil {
		requireUsable("knee", knee.StageIndex)
		if knee.Reason == spec.KneeReasonLatencySlope && (knee.SlopeRatio == nil || *knee.SlopeRatio <= 2) {
			report("a latency_slope knee needs a slope_ratio above 2")
		}
		if bottleneck := knee.Bottleneck; bottleneck != nil && (bottleneck.Utilization < 0 || bottleneck.Utilization > 1) {
			report("bottleneck utilization %g is outside [0, 1]", bottleneck.Utilization)
		}
	}
	if replica := model.PerReplica; replica != nil {
		if model.SloMax == nil {
			report("per_replica without slo_max")
		} else if want := model.SloMax.Rps / float64(replica.ReadyReplicas); math.Abs(replica.RpsPerReplica-want) > relativeTolerance*max(want, 1) {
			report("rps_per_replica %g is not slo_max %g over %d replicas", replica.RpsPerReplica, model.SloMax.Rps, replica.ReadyReplicas)
		}
		if replica.RpsPerGpu != nil && replica.TotalGpus == nil {
			report("rps_per_gpu without total_gpus")
		}
	}
}

// checkFinite is the last line of defence: a NaN or an infinity anywhere in the
// result cannot be written as JSON and means a statistic was computed from bad
// input.
func checkFinite(value reflect.Value, path string, report func(string, ...any)) {
	switch value.Kind() {
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			report("%s is %v", path, number)
		}
	case reflect.Pointer, reflect.Interface:
		if !value.IsNil() {
			checkFinite(value.Elem(), path, report)
		}
	case reflect.Struct:
		for i := range value.NumField() {
			checkFinite(value.Field(i), path+"."+value.Type().Field(i).Name, report)
		}
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			checkFinite(value.Index(i), fmt.Sprintf("%s[%d]", path, i), report)
		}
	}
}
