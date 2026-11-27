package quality

import (
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestABurstProbeIsExcludedWithoutDegradingTheRun(t *testing.T) {
	quality := spec.RunQuality{Stages: []spec.StageQuality{{StageIndex: 0, IsValid: true}, {StageIndex: 1, IsValid: true}}}
	markBurstStages(&quality, map[int]bool{1: true})
	if !quality.Stages[1].IsExcluded || quality.Stages[1].Issues[0].Code != spec.QualityIssueCodeBurstStage || quality.Stages[0].IsExcluded {
		t.Fatalf("stages %+v", quality.Stages)
	}
	if got := Grade(quality); got != spec.QualityGradeGood {
		t.Errorf("grade %s: a deliberate probe is not a flaw", got)
	}
	// A 20s probe always has too few scrapes; that is not a flaw either.
	quality.Stages[1].Issues = append(quality.Stages[1].Issues, spec.QualityIssue{Code: spec.QualityIssueCodeLowResolution})
	if got := Grade(quality); got != spec.QualityGradeGood {
		t.Errorf("grade %s: low resolution on a burst probe", got)
	}
	quality.Stages[1].Issues = append(quality.Stages[1].Issues, spec.QualityIssue{Code: spec.QualityIssueCodeAchievedRpsLow})
	if got := Grade(quality); got != spec.QualityGradeDegraded {
		t.Errorf("grade %s: a burst stage with a real problem still degrades", got)
	}
	quality.Stages = quality.Stages[1:2]
	quality.Stages[0].Issues = quality.Stages[0].Issues[:2]
	if got := Grade(quality); got != spec.QualityGradeUnusable {
		t.Errorf("grade %s: only a burst probe measures nothing", got)
	}
}
