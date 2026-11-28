package analyze

import (
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/advisors/sglang"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// adviseSGLang runs the SGLang advisor (S1 to S4) when the target is SGLang
// and the run has an operating point.
func adviseSGLang(run input.Run, analysis spec.AnalysisResult, stages []input.Stage, outputs *sizing.Outputs) error {
	if run.Spec.Target.Type != spec.TargetTypeSglang || outputs.Recommendations.OperatingPoint.StageIndex == nil {
		return nil
	}
	in := sglang.Input{
		Target:     spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeSglang},
		Knobs:      sglangKnobs(run.CapturedKnobs),
		Confidence: spec.RecommendationConfidenceMedium,
	}
	if dataset := run.Spec.Workload.Dataset; dataset != nil && dataset.SharedPrefix != nil {
		in.IsSharedPrefixWorkload = true
	}
	var pods map[string]bool
	for _, one := range outputs.Usage {
		if one.Target.Role == spec.WorkloadRoleTarget {
			in.Target = one.Target
			in.Target.Container = nil
			pods = one.Pods
			break
		}
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
		view, err := sglang.ViewStage(matrix, pods, stage, analysis.Stages[i])
		if err != nil {
			return fmt.Errorf("sglang advisor, stage %d: %w", stage.Index, err)
		}
		in.Stages = append(in.Stages, view)
		if stage.Index == operatingIndex {
			in.Operating = view
		}
	}
	outputs.Recommendations.Recommendations = append(outputs.Recommendations.Recommendations, sglang.Advise(in)...)
	return nil
}

// sglangKnobs reads the captured SGLang flags the advisor uses.
func sglangKnobs(captured map[string]spec.KnobValue) sglang.Knobs {
	knobs := sglang.Knobs{
		MemFractionStatic:  knobNumber(captured, target.KnobSGLangMemFractionStatic),
		MaxRunningRequests: knobNumber(captured, target.KnobSGLangMaxRunningRequests),
	}
	knobs.SchedulePolicy, _ = captured[target.KnobSGLangSchedulePolicy].(string)
	knobs.IsRadixCacheDisabled, knobs.IsRadixCacheCaptured = captured[target.KnobSGLangDisableRadixCache].(bool)
	return knobs
}
