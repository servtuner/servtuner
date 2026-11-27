package analyze

import (
	"math"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func pricedRun(source spec.InfraPricingPolicySource, withSnapshot bool) input.Run {
	policy := &spec.InfraPricingPolicy{Source: source}
	if source == spec.InfraPricingPolicySourceStatic {
		policy.Static = &spec.StaticPriceTable{Nodes: spec.StaticPriceTableNodes{"m7i.xlarge": {UsdPerHour: 0.2, Cores: 4, MemoryGib: 16}}}
	}
	if source == spec.InfraPricingPolicySourceAws {
		policy.Aws = &spec.AwsPricing{Region: "us-east-1"}
	}
	run := input.Run{Spec: spec.RunSpec{Analysis: &spec.Analysis{InfraPricing: policy}}}
	if withSnapshot {
		instance := "m7i.xlarge"
		run.PreSnapshot = &spec.TargetSnapshot{Workloads: []spec.Workload_2{{
			Role: spec.WorkloadRoleTarget, HardwareProfile: &spec.HardwareProfile{Accelerator: "none", NodeInstanceType: &instance},
		}}}
	}
	return run
}

func sizedOutputs() Outputs {
	var outputs Outputs
	outputs.Capacity = spec.CapacityTable{
		PerReplica: &spec.CapacityTablePerReplica{CpuRequestCores: 1, MemoryRequestBytes: 2 * 1073741824},
		Rows:       []spec.CapacityRow{{Replicas: 2}, {Replicas: 5}},
	}
	return outputs
}

func TestAStaticPolicyPricesTheRowsAndTheModel(t *testing.T) {
	outputs := sizedOutputs()
	if err := applyInfraCost(pricedRun(spec.InfraPricingPolicySourceStatic, true), &outputs); err != nil {
		t.Fatal(err)
	}
	cost := outputs.Capacity.InfraCost
	// 1 core of 4 is 25% of the node, 2 GiB of 16 is 12.5%: 0.25 × 0.2 USD/h.
	if cost == nil || !cost.IsPriced || math.Abs(*cost.UsdPerReplicaHour-0.05) > 1e-12 {
		t.Fatalf("cost %+v", cost)
	}
	if got := *outputs.Capacity.Rows[1].CostPerHour; math.Abs(got-0.25) > 1e-12 {
		t.Errorf("5 replicas cost %v USD/h, want 0.25", got)
	}
}

func TestPricesAreNeverFetchedAtAnalysisTime(t *testing.T) {
	outputs := sizedOutputs()
	if err := applyInfraCost(pricedRun(spec.InfraPricingPolicySourceAws, true), &outputs); err != nil {
		t.Fatal(err)
	}
	cost := outputs.Capacity.InfraCost
	if cost == nil || cost.IsPriced || !strings.Contains(*cost.Reason, "never at analysis time") || outputs.Capacity.Rows[0].CostPerHour != nil {
		t.Errorf("aws without pricing/infra.json: %+v", cost)
	}
}

func TestNoSnapshotMeansUnknownHardwareAndNoPolicyMeansNoCost(t *testing.T) {
	outputs := sizedOutputs()
	if err := applyInfraCost(pricedRun(spec.InfraPricingPolicySourceStatic, false), &outputs); err != nil {
		t.Fatal(err)
	}
	if cost := outputs.Capacity.InfraCost; cost == nil || cost.IsPriced || !strings.Contains(*cost.Reason, "hardware profile") {
		t.Errorf("no snapshot: %+v", cost)
	}
	outputs = sizedOutputs()
	if err := applyInfraCost(input.Run{}, &outputs); err != nil || outputs.Capacity.InfraCost != nil {
		t.Errorf("no policy: %+v %v", outputs.Capacity.InfraCost, err)
	}
}

// A host target has no snapshot profile: its declared hardware prices it, a GPU replica
// paying for the GPUs it declares at the host's per-GPU price.
func TestDeclaredHardwarePricesAHostRun(t *testing.T) {
	purchase, hours, watts, kwh := 500.0, 8760.0, 220.0, 0.087
	run := input.Run{Spec: spec.RunSpec{
		Target: spec.Target{Hardware: &spec.DeclaredHardware{Host: "gpu-box", Accelerator: "NVIDIA GeForce RTX 3070", GpuCount: 1}},
		Analysis: &spec.Analysis{InfraPricing: &spec.InfraPricingPolicy{
			Source: spec.InfraPricingPolicySourceStatic,
			Static: &spec.StaticPriceTable{Gpus: spec.StaticPriceTableGpus{
				"NVIDIA GeForce RTX 3070": {PurchaseUsd: &purchase, AmortizationHours: &hours, Watts: &watts, UsdPerKwh: &kwh},
			}},
		}},
	}}
	var outputs Outputs
	outputs.Capacity = spec.CapacityTable{Rows: []spec.CapacityRow{{Replicas: 3}}}
	if err := applyInfraCost(run, &outputs); err != nil {
		t.Fatal(err)
	}
	cost := outputs.Capacity.InfraCost
	want := purchase/hours + watts/1000*kwh
	if cost == nil || !cost.IsPriced || math.Abs(*cost.UsdPerReplicaHour-want) > 1e-12 {
		t.Fatalf("cost %+v", cost)
	}
	if got := *outputs.Capacity.Rows[0].CostPerHour; math.Abs(got-3*want) > 1e-12 {
		t.Errorf("3 replicas cost %v USD/h, want %v", got, 3*want)
	}
	// A captured profile wins over the declaration.
	run.PreSnapshot = pricedRun(spec.InfraPricingPolicySourceStatic, true).PreSnapshot
	outputs.Capacity = spec.CapacityTable{Rows: []spec.CapacityRow{{Replicas: 1}}}
	if err := applyInfraCost(run, &outputs); err != nil {
		t.Fatal(err)
	}
	if cost := outputs.Capacity.InfraCost; cost.IsPriced {
		t.Errorf("the snapshot's m7i.xlarge has no price here, so the run is unpriced: %+v", cost)
	}
}

// A priced replica's cost per 1k requests and per 1M output tokens: a replica serves its
// share of the SLO-max for an hour.
func TestUnitCostsFollowTheSLOMaxPerReplica(t *testing.T) {
	outputs := sizedOutputs()
	if err := applyInfraCost(pricedRun(spec.InfraPricingPolicySourceStatic, true), &outputs); err != nil {
		t.Fatal(err)
	}
	tokens := 1000.0
	var analysis spec.AnalysisResult
	analysis.Capacity.SloMax = &spec.SloMaxRps{Rps: 10, ValidStageIndex: 1}
	analysis.Capacity.PerReplica = &spec.PerReplica{ReadyReplicas: 2, RpsPerReplica: 5}
	analysis.Stages = []spec.StageAggregate{{StageIndex: 1, OutputTokensPerSecond: &tokens}}
	applyUnitCosts(analysis, &outputs)
	cost := outputs.Capacity.InfraCost
	// 0.05 USD/h over 5 RPS × 3600 s, per 1000; and over 500 tokens/s × 3600 s, per 1e6.
	if cost.UsdPer1KRequests == nil || math.Abs(*cost.UsdPer1KRequests-0.05/18000*1000) > 1e-12 {
		t.Errorf("per 1k requests %v", cost.UsdPer1KRequests)
	}
	if cost.UsdPer1MOutputTokens == nil || math.Abs(*cost.UsdPer1MOutputTokens-0.05/1800000*1e6) > 1e-12 {
		t.Errorf("per 1M output tokens %v", cost.UsdPer1MOutputTokens)
	}
	if cost.UnitCostReason != nil {
		t.Errorf("reason %q", *cost.UnitCostReason)
	}

	// No SLO-max: priced per hour, but no unit costs, and it says why.
	outputs = sizedOutputs()
	if err := applyInfraCost(pricedRun(spec.InfraPricingPolicySourceStatic, true), &outputs); err != nil {
		t.Fatal(err)
	}
	applyUnitCosts(spec.AnalysisResult{}, &outputs)
	cost = outputs.Capacity.InfraCost
	if cost.UsdPer1KRequests != nil || cost.UnitCostReason == nil || !strings.Contains(*cost.UnitCostReason, "SLO-max") {
		t.Errorf("no SLO-max: %+v", cost)
	}
}
