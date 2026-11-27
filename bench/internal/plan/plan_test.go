package plan

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

const workload = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func float(value float64) *float64 { return &value }

// model has two replicas and three usable stages per replica: TTFT p95 rises
// 0.2s → 0.4s → 0.8s at 50 → 100 → 150 RPS per replica.
func model() spec.RunCapacityModel {
	stage := func(index int, rps, ttft float64) spec.ModelStage {
		return spec.ModelStage{StageIndex: index, RpsPerReplica: rps, IsUsable: true,
			TtftSeconds: &spec.LatencyPoint{P95: float(ttft), P99: float(ttft * 1.5)}, E2ESeconds: &spec.LatencyPoint{P95: float(ttft + 1)}}
	}
	return spec.RunCapacityModel{
		AnalyzerVersion: "test", RunId: "run-1", ReadyReplicas: 2,
		Identity:   spec.ModelIdentity{TargetType: spec.TargetTypeBifrost, WorkloadHash: workload, ArrivalProcess: spec.PacingPoisson, QualityGrade: spec.QualityGradeGood, Knobs: spec.ModelIdentityKnobs{}},
		Stages:     []spec.ModelStage{stage(0, 50, 0.2), stage(1, 100, 0.4), stage(2, 150, 0.8)},
		PerReplica: &spec.ModelPerReplica{CpuRequestCores: 0.5, MemoryRequestBytes: 268435456}, // 256 MiB
		Queueing:   &spec.ModelQueueing{WMeanSeconds: 1, WP99Seconds: 2, Cv: 0.3},
		ScaleOut:   spec.RunCapacityModelScaleOut{ReplicaCounts: []int{2}},
	}
}

func request(t *testing.T, raw string) spec.PlanRequest {
	t.Helper()
	parsed, err := spec.Parse[spec.PlanRequest](spec.SchemaPlanRequest, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func makePlan(t *testing.T, raw string, models ...spec.RunCapacityModel) spec.Plan {
	t.Helper()
	if len(models) == 0 {
		models = []spec.RunCapacityModel{model()}
	}
	result, err := Make(request(t, raw), models, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if err := spec.Validate(spec.SchemaPlan, encoded); err != nil {
		t.Fatalf("plan.json is invalid: %v\n%s", err, encoded)
	}
	return result
}

func TestTheSLOMaxIsInterpolatedBetweenMeasuredStages(t *testing.T) {
	// TTFT 0.5s lies a quarter of the way from 0.4s (100 RPS) to 0.8s (150 RPS).
	tier := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "500ms"}}`).Tiers[0]
	if math.Abs(*tier.RpsPerReplica-112.5) > 1e-9 || !*tier.IsInterpolated || *tier.LimitingSlo != "ttft_p95" {
		t.Errorf("tier = %+v", tier)
	}
	// 400 / (112.5 × 0.8) = 4.44, so 5 replicas.
	if *tier.ReplicasPeak != 5 || tier.Confidence != spec.PlanTierConfidenceLow {
		t.Errorf("replicas %d, confidence %s", *tier.ReplicasPeak, tier.Confidence)
	}
}

func TestADifferentSLOThanTheRunUsedIsPlannedFromTheCurves(t *testing.T) {
	// p99 is 1.5 × p95: 0.3s, 0.6s, 1.2s. A 0.6s p99 holds at 100 exactly.
	tier := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p99": "600ms"}}`).Tiers[0]
	if math.Abs(*tier.RpsPerReplica-100) > 1e-9 {
		t.Errorf("rps per replica = %v", *tier.RpsPerReplica)
	}
}

func TestAnSLOStricterThanTheLightestStageIsBlocked(t *testing.T) {
	plan := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "100ms"}}`)
	tier := plan.Tiers[0]
	if !tier.IsBlocked || !strings.Contains(*tier.BlockedReason, "lighter stage") || plan.Checks[0].Status != spec.PlanCheckStatusBlocked {
		t.Errorf("tier = %+v, checks %+v", tier, plan.Checks)
	}
}

func TestAnSLOLooserThanEveryStageIsAFloor(t *testing.T) {
	tier := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "5s"}}`).Tiers[0]
	if *tier.RpsPerReplica != 150 || *tier.IsInterpolated || len(tier.Warnings) != 1 || !strings.Contains(tier.Warnings[0], "is a floor") {
		t.Errorf("tier = %+v", tier)
	}
}

func TestAvailabilitySpareAndZones(t *testing.T) {
	// 400 / (150 × 0.8) = 3.33 → 4, + 1 spare = 5, rounded to 3 zones = 6.
	tier := makePlan(t, `{"peak_rps": 400, "baseline_rps": 30, "slos": {"ttft_p95": "5s"}, "availability": {"spare": true, "zones": 3}}`).Tiers[0]
	if *tier.ReplicasPeak != 6 {
		t.Errorf("peak replicas = %d", *tier.ReplicasPeak)
	}
	// Baseline: max(2 min, 1 + 1 spare) = 2 → 3 zones = 3, which is the HPA minimum.
	if *tier.ReplicasBaseline != 3 || tier.Hpa.MinReplicas != 3 || tier.Hpa.MaxReplicas != 6 {
		t.Errorf("baseline %d, hpa %+v", *tier.ReplicasBaseline, tier.Hpa)
	}
	if tier.Totals.CpuRequestCores != 3 || tier.Totals.MemoryRequestBytes != 6*(1<<28) {
		t.Errorf("totals = %+v", tier.Totals)
	}
	if len(tier.Sensitivity) != 3 || tier.Sensitivity[2].Rps != 800 || tier.Sensitivity[2].Replicas != 9 {
		t.Errorf("sensitivity = %+v", tier.Sensitivity)
	}
}

func TestPinningTooFewReplicasIsBlockedByGuardRailOne(t *testing.T) {
	plan := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "5s"}, "overrides": [{"node": "bifrost.replicas_peak", "kind": "pin", "value": 2}]}`)
	tier := plan.Tiers[0]
	if !tier.IsBlocked || *tier.ReplicasPeak != 2 {
		t.Fatalf("tier = %+v", tier)
	}
	var statuses []string
	for _, check := range plan.Checks {
		statuses = append(statuses, string(check.Status))
	}
	if joined := strings.Join(statuses, ","); !strings.Contains(joined, "blocked") || !strings.Contains(joined, "violates_measured") {
		t.Errorf("checks = %+v", plan.Checks)
	}
}

func TestAParameterOverrideFlowsThroughEveryNumber(t *testing.T) {
	base := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "5s"}}`).Tiers[0]
	tuned := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "5s"}, "overrides": [{"node": "bifrost.h_rep", "kind": "parameter", "value": 0}]}`).Tiers[0]
	// No headroom: 400 / 150 = 2.67 → 3 replicas instead of 4.
	if *base.ReplicasPeak != 4 || *tuned.ReplicasPeak != 3 || *tuned.UtilizationAtPeak <= *base.UtilizationAtPeak {
		t.Errorf("base %d, tuned %d", *base.ReplicasPeak, *tuned.ReplicasPeak)
	}
}

func TestAHypotheticalInputLowersConfidence(t *testing.T) {
	tier := makePlan(t, `{"peak_rps": 400, "slos": {"ttft_p95": "5s"}, "overrides": [{"node": "bifrost.w_mean", "kind": "hypothetical", "value": 3}]}`).Tiers[0]
	if tier.Confidence != spec.PlanTierConfidenceLow || tier.Queue.Concurrency <= 180 {
		t.Errorf("tier = %+v", tier)
	}
}

func TestModelsOfDifferentWorkloadsNeedAChoice(t *testing.T) {
	other := model()
	other.Identity.WorkloadHash = strings.Repeat("b", 64)
	if _, err := Make(request(t, `{"peak_rps": 1, "slos": {"ttft_p95": "5s"}}`), []spec.RunCapacityModel{model(), other}, nil, "test"); err == nil {
		t.Error("two workloads planned together")
	}
	if _, err := Make(request(t, `{"peak_rps": 1, "slos": {"ttft_p95": "5s"}, "workload_hash": "`+strings.Repeat("b", 64)+`"}`), []spec.RunCapacityModel{model()}, nil, "test"); err == nil {
		t.Error("a workload the model did not measure was planned")
	}
}

func TestUnusableAndReplicaLessModelsAreBlocked(t *testing.T) {
	unusable := model()
	unusable.Identity.QualityGrade = spec.QualityGradeUnusable
	unknown := model()
	unknown.ReadyReplicas = 0
	plan := makePlan(t, `{"peak_rps": 10, "slos": {"ttft_p95": "5s"}}`, unusable, unknown)
	if !plan.Tiers[0].IsBlocked || !plan.Tiers[1].IsBlocked {
		t.Errorf("tiers = %+v", plan.Tiers)
	}
}

func TestRenderingsReevaluate(t *testing.T) {
	plan := makePlan(t, `{"peak_rps": 400, "baseline_rps": 30, "slos": {"ttft_p95": "500ms"}, "availability": {"zones": 2}}`)
	for _, node := range plan.Derivation.Nodes {
		if node.Kind == spec.DerivationNodeKindDerived && (node.Rendered == nil || !strings.Contains(*node.Rendered, " = ")) {
			t.Errorf("%s has no rendering", node.Id)
		}
	}
}
