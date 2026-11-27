package analyze

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/queueing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// BuildCapacityModel writes capacity_model.json's content: the run's identity, its per-replica curves, the per-replica sizing
// and the queueing inputs, so the planner never reads raw data.
func BuildCapacityModel(run spec.RunSpec, runID string, analysis spec.AnalysisResult, outputs Outputs, knobs map[string]spec.KnobValue) (spec.RunCapacityModel, error) {
	hash, err := WorkloadHash(run.Workload)
	if err != nil {
		return spec.RunCapacityModel{}, err
	}
	arrival := run.Load.Pacing
	if arrival == "" {
		arrival = spec.PacingConstant
	}
	model := spec.RunCapacityModel{
		AnalyzerVersion: AnalyzerVersion, RunId: runID,
		Identity: spec.ModelIdentity{
			TargetType: run.Target.Type, Knobs: spec.ModelIdentityKnobs{}, WorkloadHash: hash, WorkloadPreset: run.WorkloadPreset,
			ArrivalProcess: arrival, QualityGrade: analysis.Quality.Grade, HardwareProfileId: analysis.HardwareProfileId,
		},
		Stages:   []spec.ModelStage{},
		ScaleOut: spec.RunCapacityModelScaleOut{ReplicaCounts: []int{}},
	}
	for name, value := range knobs {
		model.Identity.Knobs[name] = value
	}
	if perReplica := analysis.Capacity.PerReplica; perReplica != nil {
		model.ReadyReplicas = perReplica.ReadyReplicas
		model.ScaleOut.ReplicaCounts = []int{perReplica.ReadyReplicas}
	}
	for i, stage := range analysis.Stages {
		model.Stages = append(model.Stages, modelStage(stage, analysis.Quality.Stages[i], model.ReadyReplicas))
	}
	model.PerReplica = modelPerReplica(outputs)
	model.InfraCost = outputs.Capacity.InfraCost
	model.TokenCost = outputs.Capacity.TokenCost
	if queue := outputs.Queue; queue != nil {
		model.Queueing = &spec.ModelQueueing{
			WMeanSeconds: queue.Operating.MeanSeconds, WP99Seconds: queue.Operating.P99Seconds, Cv: queue.Operating.CoefficientOfVariation,
		}
		model.Memory = modelMemory(queue, outputs.Recommendations)
	}
	return model, nil
}

// modelMemory is the router's memory line for the planner's m(s) and memory
// check, or nil without a fit.
func modelMemory(queue *queueing.Input, recommendations spec.Recommendations) *spec.ModelMemory {
	fit := queue.Memory
	if fit == nil {
		return nil
	}
	headroom := recommendations.Policy.Headroom.Limit
	memory := &spec.ModelMemory{BaselineBytes: fit.BaselineBytes, BytesPerInFlight: fit.BytesPerInFlight, Stages: fit.Stages, LimitHeadroom: &headroom}
	if queue.RequestBytesMean > 0 {
		mean := queue.RequestBytesMean
		memory.RequestBytesMean = &mean
	}
	for _, recommendation := range recommendations.Recommendations {
		if recommendation.DerivationNode == nil || *recommendation.DerivationNode != queueing.NodeQueue {
			continue
		}
		if size, ok := recommendation.Recommended.(float64); ok {
			queueSize := int(size)
			memory.QueueSize = &queueSize
		}
	}
	return memory
}

// modelStage is one stage per replica. With no known replica count the run's
// totals are kept, and ready_replicas 0 says they cannot be scaled.
func modelStage(stage spec.StageAggregate, quality spec.StageQuality, replicas int) spec.ModelStage {
	perReplica := stage.AchievedRps
	if replicas > 0 {
		perReplica /= float64(replicas)
	}
	errorRate := 0.0
	if stage.Requests > 0 {
		errorRate = float64(stage.Errors) / float64(stage.Requests)
	}
	return spec.ModelStage{
		StageIndex: stage.StageIndex, RpsPerReplica: perReplica, IsUsable: quality.IsValid && !quality.IsExcluded, ErrorRate: errorRate,
		TtftSeconds: latencyPoint(stage.Ttft), E2ESeconds: latencyPoint(stage.E2E), ItlSeconds: latencyPoint(stage.Itl),
	}
}

func latencyPoint(summary *spec.LatencySummary) *spec.LatencyPoint {
	if summary == nil {
		return nil
	}
	point := &spec.LatencyPoint{P50: summary.P50Seconds.Value, P95: summary.P95Seconds.Value, P99: summary.P99Seconds.Value}
	if point.P50 == nil && point.P95 == nil && point.P99 == nil {
		return nil
	}
	return point
}

// modelPerReplica sums the target workload's recommended requests and limits.
func modelPerReplica(outputs Outputs) *spec.ModelPerReplica {
	table := outputs.Capacity
	if table.PerReplica == nil {
		return nil
	}
	perReplica := &spec.ModelPerReplica{
		CpuRequestCores: table.PerReplica.CpuRequestCores, MemoryRequestBytes: table.PerReplica.MemoryRequestBytes, Gpus: table.GpusPerReplica,
	}
	var cpuLimit, memoryLimit float64
	var hasCPULimit, hasMemoryLimit bool
	for _, recommendation := range outputs.Recommendations.Recommendations {
		value, ok := recommendation.Recommended.(float64)
		if !ok || recommendation.Target.Role != spec.WorkloadRoleTarget {
			continue
		}
		switch recommendation.Subject.Name {
		case "cpu.limit":
			cpuLimit, hasCPULimit = cpuLimit+value, true
		case "memory.limit":
			memoryLimit, hasMemoryLimit = memoryLimit+value, true
		case "cpu_utilization":
			perReplica.HpaCpuUtilizationPercent = &value
		}
	}
	if hasCPULimit {
		perReplica.CpuLimitCores = &cpuLimit
	}
	if hasMemoryLimit {
		perReplica.MemoryLimitBytes = &memoryLimit
	}
	return perReplica
}

// WorkloadHash identifies a request mix: SHA-256 of the RunSpec's workload
// block as canonical JSON (sorted keys, no spaces), without the seed, which
// changes the sample but not the distribution.
func WorkloadHash(workload spec.Workload) (string, error) {
	raw, err := json.Marshal(workload)
	if err != nil {
		return "", fmt.Errorf("workload hash: %w", err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return "", fmt.Errorf("workload hash: %w", err)
	}
	delete(canonical, "seed")
	sorted, err := json.Marshal(canonical) // Go writes map keys in sorted order
	if err != nil {
		return "", fmt.Errorf("workload hash: %w", err)
	}
	sum := sha256.Sum256(sorted)
	return hex.EncodeToString(sum[:]), nil
}
