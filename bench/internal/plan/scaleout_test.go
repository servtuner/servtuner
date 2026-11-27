package plan

import (
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// atReplicas is model() measured at another replica count, with its
// per-replica curve scaled by factor (1 means perfectly linear).
func atReplicas(runID string, replicas int, factor float64) spec.RunCapacityModel {
	measured := model()
	measured.RunId, measured.ReadyReplicas = runID, replicas
	measured.ScaleOut.ReplicaCounts = []int{replicas}
	stages := make([]spec.ModelStage, len(measured.Stages))
	for i, stage := range measured.Stages {
		stage.RpsPerReplica *= factor
		stages[i] = stage
	}
	measured.Stages = stages
	return measured
}

func checkTexts(plan spec.Plan) string {
	var texts []string
	for _, check := range plan.Checks {
		texts = append(texts, string(check.Status)+": "+check.Text)
	}
	return strings.Join(texts, "\n")
}

const scaleOutRequest = `{"peak_rps": 400, "slos": {"ttft_p95": "500ms"}}`

func TestRunsAtTwoReplicaCountsWithin10PercentVerifyScaleOut(t *testing.T) {
	plan := makePlan(t, scaleOutRequest, atReplicas("one", 1, 1), atReplicas("two", 2, 0.95))
	if len(plan.Tiers) != 1 {
		t.Fatalf("one configuration is one tier: %d tiers", len(plan.Tiers))
	}
	if plan.Tiers[0].RunId != "two" {
		t.Errorf("planned from %s, want the lower per-replica capacity (two)", plan.Tiers[0].RunId)
	}
	if strings.Contains(strings.Join(plan.Tiers[0].Scope, " "), "unverified") {
		t.Errorf("scope still says unverified: %v", plan.Tiers[0].Scope)
	}
	if text := checkTexts(plan); !strings.Contains(text, "ok: Linear scale-out verified") || !strings.Contains(text, "[1 2] replicas") {
		t.Errorf("checks:\n%s", text)
	}
}

func TestAPerReplicaDropBeyond10PercentViolatesTheMeasurements(t *testing.T) {
	plan := makePlan(t, scaleOutRequest, atReplicas("one", 1, 1), atReplicas("two", 2, 0.7))
	text := checkTexts(plan)
	if !strings.Contains(text, "violates_measured: Linear scale-out does not hold") || !strings.Contains(text, "differs by 30%") {
		t.Errorf("checks:\n%s", text)
	}
	if !strings.Contains(strings.Join(plan.Tiers[0].Scope, " "), "unverified") {
		t.Errorf("scope: %v", plan.Tiers[0].Scope)
	}
}

func TestRepeatsAtOneCountOrAFloorDoNotVerify(t *testing.T) {
	repeats := makePlan(t, scaleOutRequest, atReplicas("a", 2, 1), atReplicas("b", 2, 0.98))
	if text := checkTexts(repeats); !strings.Contains(text, "all at 2 replica(s)") {
		t.Errorf("checks:\n%s", text)
	}
	// A lenient SLO no stage fails: each per-replica SLO-max is only a floor.
	floors := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "5s"}}`, atReplicas("one", 1, 1), atReplicas("two", 2, 1))
	if text := checkTexts(floors); !strings.Contains(text, "cannot be verified") {
		t.Errorf("checks:\n%s", text)
	}
}

func TestDifferentConfigurationsStaySeparateTiers(t *testing.T) {
	other := atReplicas("other", 1, 1)
	other.Identity.Knobs = spec.ModelIdentityKnobs{"bifrost.concurrency": 128.0}
	plan := makePlan(t, scaleOutRequest, atReplicas("one", 1, 1), other)
	if len(plan.Tiers) != 2 {
		t.Errorf("tiers = %d, want 2: knobs differ", len(plan.Tiers))
	}
}

// TestThePresetNamesThePlansWorkload: a model measured with a preset says so
// in the tier's scope, beside the hash.
func TestThePresetNamesThePlansWorkload(t *testing.T) {
	plain := makePlan(t, scaleOutRequest, model())
	if scope := strings.Join(plain.Tiers[0].Scope, "; "); strings.Contains(scope, "preset") {
		t.Errorf("no preset, no preset in the scope: %s", scope)
	}
	measured := model()
	measured.Identity.WorkloadPreset = &spec.PresetRef{Name: "rag-prefill", Version: 1}
	named := makePlan(t, scaleOutRequest, measured)
	if scope := strings.Join(named.Tiers[0].Scope, "; "); !strings.Contains(scope, "for workload preset rag-prefill@1 (") {
		t.Errorf("scope: %s", scope)
	}
}
