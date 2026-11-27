package validate

import (
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestABurstStageIsExemptFromTheLengthRulesButCannotStandAlone(t *testing.T) {
	burstStage := true
	interval := prometheus.ScrapeInterval{Interval: 15 * time.Second, Source: spec.ScrapeIntervalSourceAssumed}
	runSpec := spec.RunSpec{Load: spec.Load{Warmup: "60s", Stages: []spec.Stage{
		{Rate: 10, Duration: "150s"}, {Rate: 30, Duration: "20s", IsBurst: &burstStage},
	}}}
	warnings, errs := stageLengthProblems(runSpec, interval)
	if len(errs) != 0 {
		t.Fatalf("errors %v", errs)
	}
	for _, warning := range warnings {
		if warning.StageIndex != nil && *warning.StageIndex == 1 {
			t.Errorf("a 20s burst probe was warned about: %s", warning.Message)
		}
	}
	runSpec.Load.Stages = runSpec.Load.Stages[1:]
	if _, errs := stageLengthProblems(runSpec, interval); len(errs) != 1 || !strings.Contains(errs[0], "every stage is_burst") {
		t.Errorf("errors %v", errs)
	}
}

func TestAConversationNeedsChatAndLoadgen(t *testing.T) {
	runSpec := spec.RunSpec{
		Engine:   spec.Engine{Type: spec.EngineTypeInferencePerf},
		Workload: spec.Workload{Api: spec.WorkloadApiCompletion, Conversation: &spec.Conversation{Turns: 3}},
	}
	problems := conversationProblems(runSpec)
	if len(problems) != 2 || !strings.Contains(problems[0], "needs workload.api chat") || !strings.Contains(problems[1], "loadgen") {
		t.Errorf("problems %v", problems)
	}
	runSpec.Engine.Type, runSpec.Workload.Api = spec.EngineTypeLoadgen, spec.WorkloadApiChat
	if problems := conversationProblems(runSpec); len(problems) != 0 {
		t.Errorf("problems %v", problems)
	}
}
