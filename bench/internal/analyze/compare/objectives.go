package compare

import (
	"fmt"
	"math"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// objectiveOrder is every objective, the primary one first, so rankings list
// the one the recommendation uses first.
func objectiveOrder(primary spec.ObjectiveName) []spec.ObjectiveName {
	order := []spec.ObjectiveName{primary}
	for _, name := range []spec.ObjectiveName{
		spec.ObjectiveNameSloMaxRps, spec.ObjectiveNameSloRpsPerReplica, spec.ObjectiveNameSloRpsPerCore,
		spec.ObjectiveNameSloRpsPerGpu, spec.ObjectiveNameTokensPerSPerGpu, spec.ObjectiveNameP95TtftAtTargetRps,
		spec.ObjectiveNameInfraCostPer1KRequests, spec.ObjectiveNameInfraCostPer1MOutputTokens,
		spec.ObjectiveNameCostPer1KRequests, spec.ObjectiveNameCostPer1MOutputTokens,
	} {
		if name != primary {
			order = append(order, name)
		}
	}
	return order
}

// direction is whether more of the objective is better.
func direction(name spec.ObjectiveName) spec.ObjectiveDirection {
	switch name {
	case spec.ObjectiveNameP95TtftAtTargetRps, spec.ObjectiveNameInfraCostPer1KRequests, spec.ObjectiveNameInfraCostPer1MOutputTokens,
		spec.ObjectiveNameCostPer1KRequests, spec.ObjectiveNameCostPer1MOutputTokens:
		return spec.ObjectiveDirectionMinimize
	}
	return spec.ObjectiveDirectionMaximize
}

// estimates are one configuration's objective estimates and its p95 TTFT at
// the reference stage.
type estimates struct {
	objectives    []spec.ObjectiveEstimate
	referenceTTFT *spec.ObjectiveInterval
}

func estimateAll(configurations []configuration, reference *int) ([]estimates, error) {
	result := make([]estimates, len(configurations))
	for i, config := range configurations {
		for _, name := range objectiveOrder(spec.ObjectiveNameSloMaxRps) {
			var interval spec.ObjectiveInterval
			var err error
			if name == spec.ObjectiveNameP95TtftAtTargetRps {
				interval, err = ttftAtTargetRPS(config)
			} else {
				interval = acrossRepeats(config.runs, perRun[name], direction(name), !perRequest[name])
			}
			if err != nil {
				return nil, fmt.Errorf("configuration %s, %s: %w", config.id, name, err)
			}
			result[i].objectives = append(result[i].objectives, spec.ObjectiveEstimate{Objective: name, Estimate: interval})
		}
		if reference != nil {
			interval, err := pooledTTFTP95(config, *reference)
			if err != nil {
				return nil, fmt.Errorf("configuration %s, p95 TTFT at stage %d: %w", config.id, *reference, err)
			}
			result[i].referenceTTFT = &interval
		}
	}
	return result, nil
}

// perRunValue is an objective's value for one run, or why it has none.
type perRunValue func(Run) (float64, string)

var perRun = map[spec.ObjectiveName]perRunValue{
	spec.ObjectiveNameSloMaxRps:                  sloMax,
	spec.ObjectiveNameSloRpsPerReplica:           perReplica,
	spec.ObjectiveNameSloRpsPerCore:              perCore,
	spec.ObjectiveNameSloRpsPerGpu:               perGPU,
	spec.ObjectiveNameTokensPerSPerGpu:           tokensPerGPU,
	spec.ObjectiveNameInfraCostPer1KRequests:     infraCostPer1KRequests,
	spec.ObjectiveNameInfraCostPer1MOutputTokens: infraCostPer1MOutputTokens,
	spec.ObjectiveNameCostPer1KRequests:          tokenCostPer1KRequests,
	spec.ObjectiveNameCostPer1MOutputTokens:      tokenCostPer1MOutputTokens,
}

// perRequest are the objectives priced per request rather than read off the
// SLO-max rate.
var perRequest = map[spec.ObjectiveName]bool{
	spec.ObjectiveNameCostPer1KRequests:     true,
	spec.ObjectiveNameCostPer1MOutputTokens: true,
}

// tokenCost is the capacity model's priced requests.
func tokenCost(run Run) (*spec.TokenCost, string) {
	if run.Model == nil || run.Model.TokenCost == nil {
		return nil, "the run has no token prices (analysis.token_pricing)"
	}
	cost := run.Model.TokenCost
	if !cost.IsPriced {
		reason := "the requests are unpriced"
		if cost.Reason != nil {
			reason += ": " + *cost.Reason
		}
		return nil, reason
	}
	return cost, ""
}

func tokenCostPer1KRequests(run Run) (float64, string) {
	cost, reason := tokenCost(run)
	if reason != "" {
		return 0, reason
	}
	if cost.UsdPer1KRequests == nil {
		return 0, "no cost per request was computed"
	}
	return *cost.UsdPer1KRequests, ""
}

func tokenCostPer1MOutputTokens(run Run) (float64, string) {
	cost, reason := tokenCost(run)
	if reason != "" {
		return 0, reason
	}
	if cost.UsdPer1MOutputTokens == nil {
		return 0, "the requests produced no output tokens"
	}
	return *cost.UsdPer1MOutputTokens, ""
}

// secondsPerHour turns a per-replica hourly price into a per-request cost.
const secondsPerHour = 3600

// replicaHourlyCost is the capacity model's priced replica.
func replicaHourlyCost(run Run) (float64, string) {
	if run.Model == nil || run.Model.InfraCost == nil {
		return 0, "the run has no infrastructure prices (analysis.infra_pricing)"
	}
	cost := run.Model.InfraCost
	if !cost.IsPriced || cost.UsdPerReplicaHour == nil {
		reason := "the replica is unpriced"
		if cost.Reason != nil {
			reason += ": " + *cost.Reason
		}
		return 0, reason
	}
	return *cost.UsdPerReplicaHour, ""
}

// infraCostPer1KRequests is a replica's hourly cost over the requests it
// serves in an hour at its SLO-max, per thousand: a replica runs at SLO-max
// RPS, so this is the cost of the capacity a request uses.
func infraCostPer1KRequests(run Run) (float64, string) {
	perHour, reason := replicaHourlyCost(run)
	if reason != "" {
		return 0, reason
	}
	rps, reason := perReplica(run)
	if reason != "" {
		return 0, reason
	}
	return perHour / (rps * secondsPerHour) * 1000, ""
}

// InfraUnitCosts is a priced replica's cost per thousand requests and per
// million output tokens at the run's SLO-max: the same numbers the
// infrastructure cost objectives rank by, so servtuner analyze writes them into the
// run's own capacity.json. reason says why either is missing.
func InfraUnitCosts(analysis spec.AnalysisResult, cost *spec.InfraCost) (per1k, per1m *float64, reason string) {
	run := Run{Analysis: analysis, Model: &spec.RunCapacityModel{InfraCost: cost}}
	if value, why := infraCostPer1KRequests(run); why == "" {
		per1k = &value
	} else {
		reason = why
	}
	if value, why := infraCostPer1MOutputTokens(run); why == "" {
		per1m = &value
	} else if reason == "" {
		reason = why
	}
	return per1k, per1m, reason
}

// infraCostPer1MOutputTokens is the same per million output tokens, from the
// SLO-max stage's output token rate per replica.
func infraCostPer1MOutputTokens(run Run) (float64, string) {
	perHour, reason := replicaHourlyCost(run)
	if reason != "" {
		return 0, reason
	}
	if run.Analysis.Capacity.SloMax == nil {
		return 0, "no stage is valid and meets the SLOs, so there is no SLO-max stage"
	}
	index := run.Analysis.Capacity.SloMax.ValidStageIndex
	stage, ok := stageAggregate(run, index)
	if !ok || stage.OutputTokensPerSecond == nil || *stage.OutputTokensPerSecond <= 0 {
		return 0, fmt.Sprintf("stage %d has no output token rate", index)
	}
	count, reason := replicas(run)
	if reason != "" {
		return 0, reason
	}
	return perHour / (*stage.OutputTokensPerSecond / float64(count) * secondsPerHour) * 1e6, ""
}

func sloMax(run Run) (float64, string) {
	if run.Analysis.Capacity.SloMax == nil {
		return 0, "no stage is valid and meets the SLOs, so there is no SLO-max"
	}
	return run.Analysis.Capacity.SloMax.Rps, ""
}

func replicas(run Run) (int, string) {
	if run.Analysis.Capacity.PerReplica == nil || run.Analysis.Capacity.PerReplica.ReadyReplicas <= 0 {
		return 0, "the ready replica count is unknown"
	}
	return run.Analysis.Capacity.PerReplica.ReadyReplicas, ""
}

func perReplica(run Run) (float64, string) {
	rps, reason := sloMax(run)
	if reason != "" {
		return 0, reason
	}
	count, reason := replicas(run)
	if reason != "" {
		return 0, reason
	}
	return rps / float64(count), ""
}

// perCore divides by the cores the configuration needs: the sized CPU request
// per replica times the replicas, so a configuration that
// needs fewer cores for the same SLO-max scores higher.
func perCore(run Run) (float64, string) {
	rps, reason := perReplica(run)
	if reason != "" {
		return 0, reason
	}
	if run.Model == nil || run.Model.PerReplica == nil || run.Model.PerReplica.CpuRequestCores <= 0 {
		return 0, "the capacity model has no sized CPU request"
	}
	return rps / run.Model.PerReplica.CpuRequestCores, ""
}

func gpus(run Run) (int, string) {
	perReplica := run.Analysis.Capacity.PerReplica
	if perReplica == nil || perReplica.TotalGpus == nil || *perReplica.TotalGpus <= 0 {
		return 0, "the run has no GPU count"
	}
	return *perReplica.TotalGpus, ""
}

func perGPU(run Run) (float64, string) {
	rps, reason := sloMax(run)
	if reason != "" {
		return 0, reason
	}
	count, reason := gpus(run)
	if reason != "" {
		return 0, reason
	}
	return rps / float64(count), ""
}

// tokensPerGPU is the output token rate at the SLO-max's valid stage.
func tokensPerGPU(run Run) (float64, string) {
	if run.Analysis.Capacity.SloMax == nil {
		return 0, "no stage is valid and meets the SLOs, so there is no SLO-max stage"
	}
	index := run.Analysis.Capacity.SloMax.ValidStageIndex
	stage, ok := stageAggregate(run, index)
	if !ok || stage.OutputTokensPerSecond == nil {
		return 0, fmt.Sprintf("stage %d has no output token rate", index)
	}
	count, reason := gpus(run)
	if reason != "" {
		return 0, reason
	}
	return *stage.OutputTokensPerSecond / float64(count), ""
}

func stageAggregate(run Run, index int) (spec.StageAggregate, bool) {
	for _, stage := range run.Analysis.Stages {
		if stage.StageIndex == index {
			return stage, true
		}
	}
	return spec.StageAggregate{}, false
}

// acrossRepeats is the mean of the runs' values with a Student t 95%
// interval, a single sample with no interval, or unavailable when any run has
// no value: a mean over the runs that happen to have one would hide a failure.
//
// A run that never failed its SLOs is censored: its true value is better
// than measured, a lower bound when more is better and an upper bound when
// less is (a cost per request divides by the SLO-max).
//
// readsSLOMax is false for an objective measured per request at the SLO-max
// stage (token cost): censoring does not bound it.
func acrossRepeats(runs []Run, value perRunValue, dir spec.ObjectiveDirection, readsSLOMax bool) spec.ObjectiveInterval {
	values := make([]float64, 0, len(runs))
	for _, run := range runs {
		v, reason := value(run)
		if reason != "" {
			return unavailable(len(runs), fmt.Sprintf("run %s: %s", run.ID, reason))
		}
		values = append(values, v)
	}
	mean := aggregate.Mean(values)
	censored := readsSLOMax && anyCensored(runs)
	interval := spec.ObjectiveInterval{Value: &mean, Samples: len(values)}
	interval.IsLowerBound = censored && dir == spec.ObjectiveDirectionMaximize
	interval.IsUpperBound = censored && dir == spec.ObjectiveDirectionMinimize
	if len(values) == 1 {
		method := spec.IntervalMethodSingleSample
		interval.Method = &method
		return interval
	}
	half := studentT975(len(values)-1) * standardDeviation(values, mean) / math.Sqrt(float64(len(values)))
	low, high := math.Max(mean-half, 0), mean+half
	method := spec.IntervalMethodRepeats
	interval.CiLow, interval.CiHigh, interval.Method = &low, &high, &method
	return interval
}

// anyCensored reports whether some run never failed its SLOs within its
// ladder: its SLO-max is then the top stage's rate, a lower bound. Every
// per-run objective is read at the SLO-max, so all of them inherit it.
func anyCensored(runs []Run) bool {
	for _, run := range runs {
		if sloMax := run.Analysis.Capacity.SloMax; sloMax != nil && sloMax.FailedStageIndex == nil && !sloMax.IsInterpolated {
			return true
		}
	}
	return false
}

func unavailable(samples int, reason string) spec.ObjectiveInterval {
	return spec.ObjectiveInterval{Samples: samples, UnavailableReason: &reason}
}

func standardDeviation(values []float64, mean float64) float64 {
	var sum float64
	for _, v := range values {
		sum += (v - mean) * (v - mean)
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

// tTable holds the 97.5th percentile of Student's t for 1 to 30 degrees of
// freedom; beyond that the normal 1.96 is within 2%.
var tTable = []float64{
	12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
	2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
	2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042,
}

func studentT975(degreesOfFreedom int) float64 {
	if degreesOfFreedom <= len(tTable) {
		return tTable[degreesOfFreedom-1]
	}
	return 1.96
}

// ttftAtTargetRPS is the pooled p95 TTFT at the stage offering
// analysis.target_rps, which must be valid in every run of the configuration.
func ttftAtTargetRPS(config configuration) (spec.ObjectiveInterval, error) {
	target := config.runs[0].Spec.Analysis
	if target == nil || target.TargetRps == nil {
		return unavailable(0, "the RunSpec sets no analysis.target_rps"), nil
	}
	index, reason := stageOffering(config.runs, *target.TargetRps)
	if reason != "" {
		return unavailable(0, reason), nil
	}
	return pooledTTFTP95(config, index)
}

// stageOffering finds the stage index offering rps in every run, valid in all.
func stageOffering(runs []Run, rps float64) (int, string) {
	index := -1
	for _, stage := range runs[0].Analysis.Stages {
		if math.Abs(stage.OfferedRps-rps) <= ladderRelativeTolerance*rps {
			index = stage.StageIndex
		}
	}
	if index < 0 {
		return 0, fmt.Sprintf("no stage offers the target %g RPS", rps)
	}
	for _, run := range runs {
		if !stageIsUsable(run, index) {
			return 0, fmt.Sprintf("stage %d (the target %g RPS) is not valid in run %s", index, rps, run.ID)
		}
	}
	return index, ""
}

func stageIsUsable(run Run, index int) bool {
	for _, stage := range run.Analysis.Quality.Stages {
		if stage.StageIndex == index {
			return stage.IsValid && !stage.IsExcluded
		}
	}
	return false
}

// pooledTTFTP95 bootstraps the p95 of the TTFTs of every run of the
// configuration at the stage, with the per-run analysis's own Rule A sample
// requirement and a seed from the run ids, so it is reproducible.
func pooledTTFTP95(config configuration, stageIndex int) (spec.ObjectiveInterval, error) {
	var pooled []float64
	for _, run := range config.runs {
		if run.TTFT == nil {
			return unavailable(0, fmt.Sprintf("run %s has no request records", run.ID)), nil
		}
		values, err := run.TTFT(stageIndex)
		if err != nil {
			return spec.ObjectiveInterval{}, fmt.Errorf("run %s: %w", run.ID, err)
		}
		pooled = append(pooled, values...)
	}
	label := fmt.Sprintf("stage-%d/ttft", stageIndex)
	summary := aggregate.Summarize(strings.Join(runIDs(config.runs), ","), label, pooled)
	if summary == nil {
		return unavailable(0, fmt.Sprintf("no request at stage %d has a TTFT", stageIndex)), nil
	}
	p95 := summary.P95Seconds
	if p95.Value == nil {
		return unavailable(p95.Samples, *p95.InsufficientSamplesReason), nil
	}
	method := spec.IntervalMethodBootstrap
	return spec.ObjectiveInterval{Value: p95.Value, CiLow: p95.CiLow, CiHigh: p95.CiHigh, Method: &method, Samples: p95.Samples}, nil
}
