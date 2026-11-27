package analyze

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/capacity"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/quality"
	"github.com/servtuner/servtuner/bench/internal/analyze/signals"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/loadgen/pacer"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// AnalyzerVersion is written into analysis.json and recommendations.json. Change it whenever a result
// can differ for the same bundle, so a stale analysis.json is recognisable.
const AnalyzerVersion = "1.24.0" // 1.24.0: token cost says when and where its datasheet came from (datasheet_fetched_at, datasheet_source)

// Analyze turns the raw data of one run into analysis.json's content: the
// quality gate, the per-stage client aggregates
// and the capacity model. It reads only the run's raw inputs, never
// in-memory state of the run, and it is deterministic: the bootstrap is seeded
// from the run id. The result has passed SelfCheck.
func Analyze(run input.Run) (spec.AnalysisResult, error) {
	stages, err := input.StagesFromPhases(run.Phases)
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	if err := applyOfferedLoad(stages, run.Spec); err != nil {
		return spec.AnalysisResult{}, err
	}
	validity, err := validityOf(run.Spec)
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	slos, err := aggregate.SLOThresholdsFromSpec(run.Spec.Slos)
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	matrix := run.Matrix
	if matrix == nil {
		matrix = input.NoMatrix{}
	}
	series := signals.New(signals.Config{
		Matrix: matrix, BenchPod: run.BenchPod, ShardPods: run.ShardPods, TargetPods: signals.PodsOf(run.PreSnapshot, run.PostSnapshot),
		ReplicaPods:        signals.ReplicaPodsOf(run.PreSnapshot, run.PostSnapshot),
		BifrostConcurrency: knobNumber(run.CapturedKnobs, bifrostConcurrencyKnob),
	})
	interval, err := aggregate.ResolveInterval(run.Targets, run.Queries, prometheus.InKubernetes(run.Spec.Target))
	if err != nil {
		return spec.AnalysisResult{}, err
	}

	aggregates := make([]aggregate.StageResult, len(stages))
	stageInputs := make([]quality.StageInput, len(stages))
	// One stage's rows at a time: a 10k RPS ladder has about a million rows,
	// and holding them all OOM-killed the pod that runs bench on kind.
	for i, stage := range stages {
		rows, err := rowsOfStage(run.Records, stage.Index)
		if err != nil {
			return spec.AnalysisResult{}, err
		}
		aggregates[i] = aggregate.AggregateStage(aggregate.StageInput{RunID: run.ID, Stage: stage, Rows: rows, SLOs: slos})
		trend := quality.SendLagGrowth(rows)
		peak := quality.PeakInFlight(rows)
		stageInputs[i] = quality.StageInput{
			Stage: stage, SendLagGrowth: &trend, Aggregate: aggregates[i],
			Validity: validity, Series: series, Interval: interval, Warnings: warningsFor(run.Phases.Warnings, stage.Index),
			PeakInFlight: &peak, MaxInFlight: run.EngineMaxInFlight,
		}
	}
	gate, err := quality.Check(quality.RunInput{
		Stages: stageInputs, Warnings: run.Phases.Warnings,
		Declared: run.Spec.Knobs, Captured: run.CapturedKnobs,
		Pre: run.PreSnapshot, Post: run.PostSnapshot, Series: series, Matrix: matrix, Queries: run.Queries,
		BurstStages: burstStages(run.Spec.Load),
	})
	if err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("quality gate: %w", err)
	}

	capacityStages := make([]capacity.Stage, len(stages))
	result := spec.AnalysisResult{AnalyzerVersion: AnalyzerVersion, Quality: gate.Quality, Stages: make([]spec.StageAggregate, len(stages))}
	for i, stage := range stages {
		result.Stages[i] = aggregates[i].Aggregate
		capacityStages[i] = capacity.Stage{
			Window: stage, Aggregate: aggregates[i].Aggregate, MeanE2ESeconds: aggregates[i].MeanE2ESeconds,
			IsExcluded: gate.Quality.Stages[i].IsExcluded, IsLowResolution: gate.LowResolution[stage.Index],
			IsTargetSaturated: quality.IsTargetSaturated(gate.Quality.Stages[i]),
		}
	}
	var usableWindows []input.Stage
	for i, stage := range stages {
		if !gate.Quality.Stages[i].IsExcluded {
			usableWindows = append(usableWindows, stage)
		}
	}
	readyReplicas, err := capacity.ReadyReplicas(series, usableWindows, run.PreSnapshot, run.PostSnapshot)
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	gpusPerReplica := capacity.GPUsPerReplica(firstSnapshot(run))
	if declared := run.Spec.Target.Hardware; readyReplicas == 0 && declared != nil {
		// A target outside Kubernetes declares its machine: servtuner launcher runs at
		// most one vLLM there, so it is one replica holding the declared GPUs.
		readyReplicas, gpusPerReplica = 1, declared.GpuCount
	}
	result.Capacity, err = capacity.Build(capacity.Input{
		Stages: capacityStages, Series: series, Interval: interval,
		ReadyReplicas: readyReplicas, GPUsPerReplica: gpusPerReplica,
	})
	if err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("capacity model: %w", err)
	}
	result.HardwareProfileId = hardwareProfileID(firstSnapshot(run))

	if err := SelfCheck(result); err != nil {
		return spec.AnalysisResult{}, err
	}
	return result, nil
}

// validityOf returns the stage validity thresholds of the RunSpec, or the
// schema's defaults when it declares none.
func validityOf(runSpec spec.RunSpec) (quality.Validity, error) {
	thresholds, err := spec.DefaultStageValidity()
	if err != nil {
		return quality.Validity{}, err
	}
	if runSpec.Analysis != nil && runSpec.Analysis.StageValidity != nil {
		declared := *runSpec.Analysis.StageValidity
		if declared.MinAchievedRpsRatio != 0 {
			thresholds.MinAchievedRpsRatio = declared.MinAchievedRpsRatio
		}
		if declared.MaxSendLagP99 != "" {
			thresholds.MaxSendLagP99 = declared.MaxSendLagP99
		}
	}
	maxSendLag, err := thresholds.MaxSendLagP99.Std()
	if err != nil {
		return quality.Validity{}, fmt.Errorf("analysis.stage_validity.max_send_lag_p99: %w", err)
	}
	return quality.Validity{
		MinAchievedRPSRatio: thresholds.MinAchievedRpsRatio, MaxSendLagP99: maxSendLag,
		IsRandomArrivals: runSpec.Load.Pacing == spec.PacingPoisson,
	}, nil
}

// rowsOfStage reads the records and keeps those the stage scheduled. Records of
// no stage (warmup, cooldown) belong to no stage window and are skipped. It is
// one pass over the records per stage, which trades re-reading the file for
// never holding more than one stage's rows.
func rowsOfStage(records input.RecordSource, stageIndex int) ([]engine.RequestRow, error) {
	var rows []engine.RequestRow
	if records == nil {
		return rows, nil
	}
	for row, err := range records.Records() {
		if err != nil {
			return nil, fmt.Errorf("read request records: %w", err)
		}
		if row.StageIndex == nil || aggregate.IsWarmupRow(row) || int(*row.StageIndex) != stageIndex {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func warningsFor(warnings []spec.StageLengthWarning, stageIndex int) []spec.StageLengthWarning {
	var matching []spec.StageLengthWarning
	for _, warning := range warnings {
		if warning.StageIndex != nil && *warning.StageIndex == stageIndex {
			matching = append(matching, warning)
		}
	}
	return matching
}

// firstSnapshot is the pre-run snapshot, or the post-run one when there is no
// pre-run snapshot: the target as it was when the load began.
func firstSnapshot(run input.Run) *spec.TargetSnapshot {
	if run.PreSnapshot != nil {
		return run.PreSnapshot
	}
	return run.PostSnapshot
}

func hardwareProfileID(snapshot *spec.TargetSnapshot) *string {
	if snapshot == nil {
		return nil
	}
	for _, workload := range snapshot.Workloads {
		if workload.Role == spec.WorkloadRoleTarget && workload.HardwareProfile != nil {
			return workload.HardwareProfile.HardwareProfileId
		}
	}
	return nil
}

// bifrostConcurrencyKnob is the knob that sets the router's concurrency ceiling.
const bifrostConcurrencyKnob = target.KnobBifrostConcurrency

// knobNumber returns a captured numeric knob, or 0 when it is absent or not a number.
func knobNumber(knobs map[string]spec.KnobValue, name string) float64 {
	number, _ := knobs[name].(float64)
	return number
}

// applyOfferedLoad sets each stage's offered rate to what its pacer really
// offers: the integral of the pacing rate over the stage, divided by its
// length. For constant pacing that is the stage rate, but a linear stage ramps
// from the previous stage's rate and a sine stage that is not a whole number
// of periods averages to something else, and comparing the achieved count with
// rate x duration would fail those stages for nothing. The pacers are
// the loadgen engine's own (pacer.ForLoad), so the two cannot disagree.
func applyOfferedLoad(stages []input.Stage, runSpec spec.RunSpec) error {
	pacers, err := pacer.ForLoad(runSpec.Load, runSpec.Workload.Seed)
	if err != nil {
		return fmt.Errorf("build the stage pacers from the RunSpec: %w", err)
	}
	for i := range stages {
		if stages[i].Index < 0 || stages[i].Index >= len(pacers) {
			return fmt.Errorf("phases.json has stage %d, but the RunSpec load has %d stages", stages[i].Index, len(pacers))
		}
		stages[i].OfferedRPS = pacer.OfferedHits(pacers[stages[i].Index]) / stages[i].Duration().Seconds()
	}
	return nil
}

// burstStages are the indexes of the load's is_burst stages.
func burstStages(load spec.Load) map[int]bool {
	burst := map[int]bool{}
	for i, stage := range load.Stages {
		if stage.Burst() {
			burst[i] = true
		}
	}
	return burst
}
