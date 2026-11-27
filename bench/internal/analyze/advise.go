package analyze

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/advisors/bifrost"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/signals"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// adviseBifrost runs the Bifrost advisor when the
// target is Bifrost, adding its recommendations to the sizing's and naming
// the queueing model's C and B rules B1 and B2. It returns the Bifrost patch,
// or nil for another target.
func adviseBifrost(run input.Run, analysis spec.AnalysisResult, stages []input.Stage, outputs *sizing.Outputs) ([]byte, error) {
	if run.Spec.Target.Type != spec.TargetTypeBifrost || outputs.Recommendations.OperatingPoint.StageIndex == nil {
		return nil, nil
	}
	router, upstream, container := bifrostWorkloads(outputs.Usage)
	if router == nil {
		return nil, nil
	}
	config, err := bifrost.ParseConfig(run.ConfigDocuments)
	if err != nil {
		return nil, err
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
	active, _, err := series.Signal(input.RouterConcurrency)
	if err != nil {
		return nil, err
	}
	in := bifrost.Input{Router: *router, Upstream: upstream, Config: config, Container: container, Confidence: spec.RecommendationConfidenceMedium}
	operatingIndex := *outputs.Recommendations.OperatingPoint.StageIndex
	for i, stage := range stages {
		quality := analysis.Quality.Stages[i]
		if !quality.IsValid || quality.IsExcluded {
			continue
		}
		view, err := bifrost.ViewStage(matrix, container.Pods, stage, analysis.Stages[i], peakIn(active, stage))
		if err != nil {
			return nil, fmt.Errorf("bifrost advisor, stage %d: %w", stage.Index, err)
		}
		in.Stages = append(in.Stages, view)
		if stage.Index == operatingIndex {
			in.Operating = view
		}
	}
	added := bifrost.Advise(in, outputs.Recommendations.Recommendations)
	outputs.Recommendations.Recommendations = append(outputs.Recommendations.Recommendations, added...)
	return bifrost.RenderPatch(bifrost.KnobsOf(outputs.Recommendations.Recommendations), config, run.Spec, run.ID)
}

// bifrostWorkloads finds the router (the target workload) and the first
// upstream among the sized containers, and the router container's usage.
func bifrostWorkloads(usage []sizing.ContainerUsage) (router, upstream *spec.RecommendationTarget, container bifrost.Container) {
	for _, one := range usage {
		workload := one.Target
		workload.Container = nil
		switch {
		case workload.Role == spec.WorkloadRoleTarget && router == nil:
			router = &workload
			container = bifrost.Container{
				CPUP95: one.CPUP95, CPULimitCores: one.CPULimitCores, Throttle: one.Throttle, ThrottleStage: one.ThrottleStage,
				MemoryPeak: one.MemoryPeak, MemoryLimitBytes: one.MemoryLimitBytes, Pods: one.Pods,
			}
		case workload.Role == spec.WorkloadRoleUpstream && upstream == nil:
			upstream = &workload
		}
	}
	return router, upstream, container
}

// peakIn is the highest sample of a signal inside a stage, or nil.
func peakIn(samples []input.Sample, stage input.Stage) *float64 {
	var peak *float64
	for _, sample := range samples {
		if sample.At.Before(stage.Start) || !sample.At.Before(stage.End) {
			continue
		}
		if peak == nil || sample.Value > *peak {
			value := sample.Value
			peak = &value
		}
	}
	return peak
}
