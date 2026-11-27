package analyze_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func stageHasIssue(result spec.AnalysisResult, stage int, code spec.QualityIssueCode) bool {
	for _, issue := range result.Quality.Stages[stage].Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func marshalable(t *testing.T, result spec.AnalysisResult) {
	t.Helper()
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("the result cannot be written as JSON: %v", err)
	}
}

func TestEngineWithoutASendLagScheduleIsNotJudgedOnIt(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("clean-ladder")
	for i := range scenario.Stages {
		scenario.Stages[i].UnknownLag = true
	}
	result := analyzeScenario(t, scenario)
	marshalable(t, result)
	if result.Quality.Grade == spec.QualityGradeUnusable {
		t.Fatalf("grade = %s", result.Quality.Grade)
	}
	for _, stage := range result.Stages {
		if stage.SendLagP50Seconds != nil || stage.SendLagP99Seconds != nil || stage.SendLagMaxSeconds != nil {
			t.Errorf("stage %d reports a send lag it does not know", stage.StageIndex)
		}
		if stageHasIssue(result, stage.StageIndex, spec.QualityIssueCodeSendLagHigh) || result.Quality.Stages[stage.StageIndex].IsExcluded && stage.StageIndex < 4 {
			t.Errorf("stage %d was judged on an unknown send lag: %+v", stage.StageIndex, result.Quality.Stages[stage.StageIndex])
		}
		if !stageHasIssue(result, stage.StageIndex, spec.QualityIssueCodeInsufficientSamples) {
			t.Errorf("stage %d does not note the unknown send lag", stage.StageIndex)
		}
	}
	if result.Capacity.SloMax == nil {
		t.Error("the capacity model still works from client latencies")
	}
}

func TestSomeUnknownSendLagsAreSkippedNotCountedAsNaN(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("send-lag-high")
	scenario.Stages[0].UnknownLagEvery = 3
	scenario.Stages[1].UnknownLagEvery = 2
	result := analyzeScenario(t, scenario)
	marshalable(t, result)
	if lag := result.Stages[0].SendLagP99Seconds; lag == nil || math.IsNaN(*lag) || *lag != 0 {
		t.Errorf("stage 0 send lag p99 = %v, want 0 from the known rows", lag)
	}
	// The known rows of stage 1 still show the 80ms lag.
	if !stageHasIssue(result, 1, spec.QualityIssueCodeSendLagHigh) {
		t.Error("known high lags must still be caught")
	}
}

func TestSelfCheckRejectsNaNAndInfinity(t *testing.T) {
	for name, poison := range map[string]float64{"NaN": math.NaN(), "Inf": math.Inf(1)} {
		result := validResult(t)
		result.Stages[0].AchievedRps = poison
		if err := analyze.SelfCheck(result); err == nil {
			t.Errorf("%s in achieved_rps was accepted", name)
		}
		result = validResult(t)
		*result.Stages[0].E2E.MeanSeconds = poison
		if err := analyze.SelfCheck(result); err == nil {
			t.Errorf("%s in a nested pointer was accepted", name)
		}
	}
}

func TestLinearLadderIsJudgedAgainstTheRampNotTheStageRate(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("achieved-rps-low")
	scenario.Pacing = spec.PacingLinear
	scenario.Stages = []analyzetest.Stage{
		{OfferedRPS: 4, DurationSeconds: 60, TTFT: 0.2, E2E: 2},
		{OfferedRPS: 8, DurationSeconds: 60, TTFT: 0.2, E2E: 2},
		{OfferedRPS: 12, DurationSeconds: 60, TTFT: 0.2, E2E: 2},
	}
	result := analyzeScenario(t, scenario)
	for _, stage := range result.Quality.Stages {
		if stage.IsExcluded {
			t.Errorf("stage %d was excluded: %+v", stage.StageIndex, stage.Issues)
		}
	}
	// Stage 0 holds 4 RPS; stage 1 ramps 4 to 8 (6 on average); stage 2 ramps 8 to 12 (10).
	for i, want := range []float64{4, 6, 10} {
		if got := result.Stages[i].OfferedRps; math.Abs(got-want) > 1e-9 {
			t.Errorf("stage %d offered_rps = %v, want %v", i, got, want)
		}
	}
	// A real shortfall is still caught on a ramp.
	scenario.Stages[1].AchievedRatio = 0.9
	if result := analyzeScenario(t, scenario); !stageHasIssue(result, 1, spec.QualityIssueCodeAchievedRpsLow) {
		t.Error("a 10% shortfall on a ramp must be caught")
	}
}

func TestSineStageThatIsNotAWholeNumberOfPeriods(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("achieved-rps-low")
	scenario.Pacing, scenario.SineAmplitude, scenario.SinePeriodSeconds = spec.PacingSine, 0.8, 40
	// 50s is 1.25 periods: the extra quarter period is the rising half, so the
	// stage offers more than rate x duration.
	scenario.Stages = []analyzetest.Stage{{OfferedRPS: 8, DurationSeconds: 50, TTFT: 0.2, E2E: 2}, {OfferedRPS: 8, DurationSeconds: 50, TTFT: 0.2, E2E: 2}}
	result := analyzeScenario(t, scenario)
	if result.Quality.Stages[0].IsExcluded {
		t.Errorf("a sine stage achieving its pacer's count was excluded: %+v", result.Quality.Stages[0].Issues)
	}
	if got := result.Stages[0].OfferedRps; math.Abs(got-8) < 0.1 {
		t.Errorf("offered_rps = %v; a 1.25 period sine stage does not average to its mean rate", got)
	}
	scenario.Stages[0].AchievedRatio = 0.9
	if result := analyzeScenario(t, scenario); !stageHasIssue(result, 0, spec.QualityIssueCodeAchievedRpsLow) {
		t.Error("a real shortfall must be caught")
	}
}

func TestPoissonStageToleratesChance(t *testing.T) {
	// 300 expected requests: the floor is min(291, 300 - 3 sqrt(300)) = 248.
	scenario := analyzetest.ScenarioNamed("achieved-rps-low")
	scenario.Pacing = spec.PacingPoisson
	scenario.Stages = []analyzetest.Stage{
		{OfferedRPS: 5, DurationSeconds: 60, TTFT: 0.2, E2E: 2, AchievedRatio: 0.93},
		{OfferedRPS: 5, DurationSeconds: 60, TTFT: 0.2, E2E: 2, AchievedRatio: 0.80},
	}
	result := analyzeScenario(t, scenario)
	if stageHasIssue(result, 0, spec.QualityIssueCodeAchievedRpsLow) {
		t.Error("93% of a poisson stage of 300 is within three deviations")
	}
	if !stageHasIssue(result, 1, spec.QualityIssueCodeAchievedRpsLow) {
		t.Error("80% is beyond three deviations")
	}
	// The same shortfall on a deterministic pacer is a failure.
	scenario.Pacing = spec.PacingConstant
	if result := analyzeScenario(t, scenario); !stageHasIssue(result, 0, spec.QualityIssueCodeAchievedRpsLow) {
		t.Error("deterministic pacers keep the 97% floor")
	}
}
