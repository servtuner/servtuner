package analyze

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/advisors/litellm"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// litellmDefaultWorkers is LiteLLM's own default for --num_workers
// (DEFAULT_NUM_WORKERS_LITELLM_PROXY, verified on v1.108.3).
const litellmDefaultWorkers = 1

// adviseLiteLLM runs the LiteLLM advisor when the
// target is LiteLLM and the run has an operating point.
func adviseLiteLLM(run input.Run, analysis spec.AnalysisResult, stages []input.Stage, outputs *sizing.Outputs) error {
	if run.Spec.Target.Type != spec.TargetTypeLitellm || outputs.Recommendations.OperatingPoint.StageIndex == nil {
		return nil
	}
	in := litellm.Input{
		Workers: litellmDefaultWorkers, IsStreaming: run.Spec.Workload.Streaming,
		Confidence: spec.RecommendationConfidenceMedium,
	}
	found := false
	for _, one := range outputs.Usage {
		if one.Target.Role != spec.WorkloadRoleTarget {
			continue
		}
		in.Proxy = one.Target
		in.Proxy.Container = nil
		in.Container = litellm.Container{
			Pods: one.Pods, CPUP95: one.CPUP95, CPULimitCores: one.CPULimitCores,
			Throttle: one.Throttle, ThrottleStage: one.ThrottleStage,
		}
		found = true
		break
	}
	if !found {
		// Off-cluster: no sized container, but the proxy's metrics still speak.
		in.Proxy = spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeLitellm}
	}
	if workers := knobNumber(run.CapturedKnobs, target.KnobLiteLLMWorkers); workers > 0 {
		in.Workers, in.IsWorkersCaptured = workers, true
	}
	matrix := run.Matrix
	if matrix == nil {
		matrix = input.NoMatrix{}
	}
	operatingIndex := *outputs.Recommendations.OperatingPoint.StageIndex
	for i, stage := range stages {
		quality := analysis.Quality.Stages[i]
		if !quality.IsValid || quality.IsExcluded {
			continue
		}
		view, err := litellm.ViewStage(matrix, in.Container.Pods, stage, analysis.Stages[i])
		if err != nil {
			return fmt.Errorf("litellm advisor, stage %d: %w", stage.Index, err)
		}
		in.Stages = append(in.Stages, view)
		if stage.Index == operatingIndex {
			in.Operating = view
		}
	}
	outputs.Recommendations.Recommendations = append(outputs.Recommendations.Recommendations, litellm.Advise(in)...)
	return nil
}
