package analyze

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/queueing"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// Recommend turns a run and its analysis into recommendations.json, the
// capacity table and the resources patch: per-container sizing at the
// operating point and the replica plan, each
// value with its derivation. It reads the same raw inputs as
// Analyze, so it is recomputable from the bundle.
func Recommend(run input.Run, analysis spec.AnalysisResult) (Outputs, error) {
	stages, err := input.StagesFromPhases(run.Phases)
	if err != nil {
		return Outputs{}, err
	}
	interval, err := aggregate.ResolveInterval(run.Targets, run.Queries, prometheus.InKubernetes(run.Spec.Target))
	if err != nil {
		return Outputs{}, err
	}
	matrix := run.Matrix
	if matrix == nil {
		matrix = input.NoMatrix{}
	}
	queue, err := queueInput(run, analysis, stages)
	if err != nil {
		return Outputs{}, err
	}
	outputs, err := sizing.Recommend(sizing.Input{
		AnalyzerVersion: AnalyzerVersion, RunID: run.ID, RunSpec: run.Spec, Analysis: analysis, Stages: stages,
		Matrix: matrix, Interval: interval.Duration, Pre: run.PreSnapshot, Post: run.PostSnapshot, Queue: queue,
	})
	if err != nil {
		return Outputs{}, err
	}
	targetPatch, err := adviseBifrost(run, analysis, stages, &outputs)
	if err != nil {
		return Outputs{}, err
	}
	if err := adviseVLLM(run, analysis, stages, interval, &outputs); err != nil {
		return Outputs{}, err
	}
	if err := adviseLiteLLM(run, analysis, stages, &outputs); err != nil {
		return Outputs{}, err
	}
	if err := checkBursts(run, analysis, stages, &outputs); err != nil {
		return Outputs{}, err
	}
	return Outputs{Outputs: outputs, TargetPatch: targetPatch}, nil
}

// Outputs are the sizing's files plus the target advisor's patch.
type Outputs struct {
	sizing.Outputs
	// TargetPatch is patches/<target>.yaml; nil when the advisor has none.
	TargetPatch []byte
}

// queueInput measures the operating-point stage's records for the queueing
// model. It is nil when there is no operating point
// or the stage has no completed requests.
func queueInput(run input.Run, analysis spec.AnalysisResult, stages []input.Stage) (*queueing.Input, error) {
	resolved, err := spec.ResolveAnalysis(run.Spec.Analysis)
	if err != nil {
		return nil, err
	}
	point := sizing.OperatingPoint(analysis, resolved.TargetRps)
	if point.StageIndex == nil {
		return nil, nil
	}
	var operating *input.Stage
	for i := range stages {
		if stages[i].Index == *point.StageIndex {
			operating = &stages[i]
		}
	}
	if operating == nil {
		return nil, fmt.Errorf("operating-point stage %d is not in phases.json", *point.StageIndex)
	}
	rows, err := rowsOfStage(run.Records, operating.Index)
	if err != nil {
		return nil, err
	}
	// Streaming through Bifrost, a provider slot is held until the upstream's
	// first token ("Streaming through a router").
	slot, firstToken := queueing.WholeRequest, false
	if run.Spec.Target.Type == spec.TargetTypeBifrost && run.Spec.Workload.Streaming {
		slot, firstToken = queueing.UntilFirstToken, true
	}
	measured, ok := queueing.MeasureSlots(*operating, rows, slot)
	if !ok {
		return nil, nil
	}
	slos, err := aggregate.SLOThresholdsFromSpec(run.Spec.Slos)
	if err != nil {
		return nil, err
	}
	queue := &queueing.Input{
		SlotIsFirstToken: firstToken,
		Operating:        measured, BurstFactor: resolved.BurstFactor, IsPoisson: run.Spec.Load.Pacing == spec.PacingPoisson,
		CurrentConcurrency: knobValue(run.CapturedKnobs, bifrostConcurrencyKnob),
		CurrentBufferSize:  knobValue(run.CapturedKnobs, target.KnobBifrostBufferSize),
	}
	if queue.InFlightByStage, queue.RequestBytesMean, err = inFlightByStage(run, analysis, stages); err != nil {
		return nil, err
	}
	switch {
	case firstToken && slos.TTFTP95Seconds != nil:
		// A request's wait for a slot shows in its TTFT, so the TTFT SLO bounds the queue.
		queue.SLOSeconds, queue.SLOPercentile, queue.SLOWSeconds, queue.SLOName = *slos.TTFTP95Seconds, 0.95, measured.P95Seconds, "TTFT"
	case firstToken:
		// No TTFT SLO: the wait still adds to every E2E, measured on the whole request.
		if slos.E2EP95Seconds != nil {
			if whole, ok := queueing.Measure(*operating, rows); ok {
				queue.SLOSeconds, queue.SLOPercentile, queue.SLOWSeconds, queue.SLOName = *slos.E2EP95Seconds, 0.95, whole.P95Seconds, "E2E"
			}
		}
	case slos.E2EP95Seconds != nil:
		queue.SLOSeconds, queue.SLOPercentile, queue.SLOWSeconds, queue.SLOName = *slos.E2EP95Seconds, 0.95, measured.P95Seconds, "E2E"
	}
	return queue, nil
}

// inFlightByStage is the mean in flight of every usable, steady stage, for
// the memory per in-flight request, and the mean request body over those
// stages, the payload size s it was measured at.
func inFlightByStage(run input.Run, analysis spec.AnalysisResult, stages []input.Stage) (map[int]float64, float64, error) {
	inFlight := map[int]float64{}
	var bytes, sized float64
	for i, stage := range stages {
		quality := analysis.Quality.Stages[i]
		if !quality.IsValid || quality.IsExcluded {
			continue
		}
		rows, err := rowsOfStage(run.Records, stage.Index)
		if err != nil {
			return nil, 0, err
		}
		if measured, ok := queueing.Measure(stage, rows); ok && measured.IsSteady() {
			inFlight[stage.Index] = measured.InFlightMean
			for _, row := range rows {
				if row.RequestBytes != nil {
					bytes += float64(*row.RequestBytes)
					sized++
				}
			}
		}
	}
	if sized == 0 {
		return inFlight, 0, nil
	}
	return inFlight, bytes / sized, nil
}

// knobValue is a captured knob as a recommendation's current value, or nil.
func knobValue(knobs map[string]spec.KnobValue, name string) spec.RecommendationValue {
	if value, ok := knobs[name]; ok {
		return value
	}
	return nil
}
