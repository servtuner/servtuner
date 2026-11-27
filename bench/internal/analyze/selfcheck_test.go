package analyze_test

import (
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func validResult(t *testing.T) spec.AnalysisResult {
	t.Helper()
	result, _ := analyzeGolden(t, "clean-ladder")
	return result
}

func TestSelfCheckRejectsEachViolation(t *testing.T) {
	high := 99.0
	for name, test := range map[string]struct {
		mutate func(*spec.AnalysisResult)
		want   string
	}{
		"ci_low above value":          {func(r *spec.AnalysisResult) { r.Stages[0].E2E.P95Seconds.CiLow = &high }, "ci_low <= value <= ci_high"},
		"ci_high below value":         {func(r *spec.AnalysisResult) { low := 0.0; r.Stages[0].E2E.P50Seconds.CiHigh = &low }, "ci_low <= value <= ci_high"},
		"partial interval":            {func(r *spec.AnalysisResult) { r.Stages[0].E2E.P50Seconds.CiLow = nil }, "some but not all"},
		"failed not above valid":      {func(r *spec.AnalysisResult) { zero := 0; r.Capacity.SloMax.FailedStageIndex = &zero }, "not above valid_stage_index"},
		"interpolated without failed": {func(r *spec.AnalysisResult) { r.Capacity.SloMax.FailedStageIndex = nil }, "no failed_stage_index"},
		"rps outside bracket":         {func(r *spec.AnalysisResult) { r.Capacity.SloMax.Rps = 100 }, "outside the bracket"},
		"knee on excluded stage":      {func(r *spec.AnalysisResult) { r.Quality.Stages[4].IsExcluded = true }, "was excluded"},
		"bottleneck out of range":     {func(r *spec.AnalysisResult) { r.Capacity.Knee.Bottleneck.Utilization = 1.5 }, "outside [0, 1]"},
		"per replica mismatch":        {func(r *spec.AnalysisResult) { r.Capacity.PerReplica.RpsPerReplica = 1 }, "rps_per_replica"},
		"stage count mismatch":        {func(r *spec.AnalysisResult) { r.Quality.Stages = r.Quality.Stages[:2] }, "quality has"},
		"slope ratio too small":       {func(r *spec.AnalysisResult) { one := 1.0; r.Capacity.Knee.SlopeRatio = &one }, "slope_ratio"},
	} {
		t.Run(name, func(t *testing.T) {
			result := validResult(t)
			if err := analyze.SelfCheck(result); err != nil {
				t.Fatalf("baseline must pass: %v", err)
			}
			test.mutate(&result)
			err := analyze.SelfCheck(result)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestAnalyzeNeverReturnsAnInvalidResult(t *testing.T) {
	for _, scenario := range analyzetest.Scenarios() {
		result := analyzeScenario(t, scenario)
		if err := analyze.SelfCheck(result); err != nil {
			t.Errorf("%s: %v", scenario.Name, err)
		}
	}
}

func TestConfigDriftUsesTheReplicasOfTheUsableStages(t *testing.T) {
	result, _ := analyzeGolden(t, "config-drift-restart")
	if got := result.Capacity.PerReplica.ReadyReplicas; got != 2 {
		t.Errorf("ready replicas = %d, want the 2 the usable stages ran on", got)
	}
}

func TestAFailedStageMayBeExcludedOnlyWhenTheTargetSaturated(t *testing.T) {
	result := validResult(t)
	result.Capacity.Knee = nil // in this golden the failed stage is also the knee, which must stay usable
	failed := *result.Capacity.SloMax.FailedStageIndex
	result.Quality.Stages[failed].IsExcluded = true
	if err := analyze.SelfCheck(result); err == nil || !strings.Contains(err.Error(), "slo_max failed stage refers to stage") {
		t.Fatalf("an excluded failed stage that did not saturate is an error, got %v", err)
	}
	result.Quality.Stages[failed].Issues = append(result.Quality.Stages[failed].Issues,
		spec.QualityIssue{Code: spec.QualityIssueCodeTargetSaturated, Message: "the target fell behind"})
	if err := analyze.SelfCheck(result); err != nil {
		t.Errorf("a target_saturated failed stage is the SLO-max's bracket: %v", err)
	}
}
