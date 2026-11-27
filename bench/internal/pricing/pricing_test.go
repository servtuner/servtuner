package pricing

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func pointer[T any](value T) *T { return &value }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

var now = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func staticPolicy() spec.InfraPricingPolicy {
	return spec.InfraPricingPolicy{Source: spec.InfraPricingPolicySourceStatic, Static: &spec.StaticPriceTable{
		Nodes: spec.StaticPriceTableNodes{
			"m7i.xlarge": {UsdPerHour: 0.2, Cores: 4, MemoryGib: 16},
			"g5.xlarge":  {UsdPerHour: 1.0, Cores: 4, MemoryGib: 16, Gpus: 1},
			"p4d":        {UsdPerHour: 32, Cores: 96, MemoryGib: 1152, Gpus: 8},
		},
		Gpus: spec.StaticPriceTableGpus{
			"RTX 3070": {PurchaseUsd: pointer(500.0), AmortizationHours: pointer(8760.0), Watts: pointer(220.0), UsdPerKwh: pointer(0.087)},
		},
	}}
}

func TestStaticResolvesTheOwnedHardwareHelper(t *testing.T) {
	prices, err := FromStatic(staticPolicy(), now)
	if err != nil {
		t.Fatal(err)
	}
	gpu := prices.Gpus["RTX 3070"]
	if want := 500.0/8760 + 220.0/1000*0.087; !near(gpu.UsdPerHour, want) {
		t.Errorf("GPU price %v, want %v", gpu.UsdPerHour, want)
	}
	if !strings.Contains(gpu.Basis, "500 USD ÷ 8760 h + 220 W ÷ 1000 × 0.087 USD/kWh") {
		t.Errorf("basis %q", gpu.Basis)
	}
	if prices.Discount != 1 || prices.Nodes["p4d"].Gpus != 8 {
		t.Errorf("prices %+v", prices)
	}
	if _, err := FromStatic(spec.InfraPricingPolicy{Source: spec.InfraPricingPolicySourceStatic}, now); err == nil {
		t.Error("static without a table resolved")
	}
}

func TestACPUReplicaPaysForItsLargerShareOfTheNode(t *testing.T) {
	prices, _ := FromStatic(staticPolicy(), now)
	// 1 core of 4 is 25%; 8 GiB of 16 is 50%: memory decides.
	cost := ReplicaCost(prices, Hardware{InstanceType: "m7i.xlarge", Accelerator: "none"}, Replica{CPUCores: 1, MemoryBytes: 8 * bytesPerGiB})
	if !cost.IsPriced || !near(*cost.UsdPerReplicaHour, 0.1) || *cost.Method != spec.InfraCostMethodNodeShare {
		t.Fatalf("cost %+v", cost)
	}
	if !strings.Contains(*cost.Basis, "max(1 cores ÷ 4, 8 GiB ÷ 16) = 0.5 of a m7i.xlarge node") {
		t.Errorf("basis %q", *cost.Basis)
	}
}

func TestAGPUReplicaPaysForItsGPUs(t *testing.T) {
	prices, _ := FromStatic(staticPolicy(), now)
	byNode := ReplicaCost(prices, Hardware{InstanceType: "p4d", Accelerator: "A100"}, Replica{CPUCores: 8, MemoryBytes: 64 * bytesPerGiB, GPUs: 2})
	if !byNode.IsPriced || !near(*byNode.UsdPerReplicaHour, 8) || *byNode.Method != spec.InfraCostMethodGpu {
		t.Errorf("2 of 8 GPUs of a 32 USD/h node: %+v", byNode)
	}
	owned := ReplicaCost(prices, Hardware{InstanceType: "local", Accelerator: "RTX 3070"}, Replica{GPUs: 1})
	if !owned.IsPriced || !near(*owned.UsdPerReplicaHour, prices.Gpus["RTX 3070"].UsdPerHour) {
		t.Errorf("the GPU model's own price wins: %+v", owned)
	}
}

func TestTheDiscountAppliesLastAndIsRecorded(t *testing.T) {
	policy := staticPolicy()
	policy.Discount = 0.7
	prices, _ := FromStatic(policy, now)
	cost := ReplicaCost(prices, Hardware{InstanceType: "g5.xlarge"}, Replica{GPUs: 1})
	if !near(*cost.UsdPerReplicaHour, 0.7) || cost.Discount == nil || !strings.Contains(*cost.Basis, "× discount 0.7") {
		t.Errorf("cost %+v %q", cost, *cost.Basis)
	}
}

func TestUnpricedHardwareSaysWhyAndIsNeverFree(t *testing.T) {
	prices, _ := FromStatic(staticPolicy(), now)
	for name, cost := range map[string]spec.InfraCost{
		"unknown node": ReplicaCost(prices, Hardware{InstanceType: "c7g.large"}, Replica{CPUCores: 1, MemoryBytes: bytesPerGiB}),
		"unknown GPU":  ReplicaCost(prices, Hardware{InstanceType: "m7i.xlarge", Accelerator: "H100"}, Replica{GPUs: 1}),
		"no sizing":    ReplicaCost(prices, Hardware{InstanceType: "m7i.xlarge"}, Replica{}),
		"no node type": ReplicaCost(prices, Hardware{}, Replica{CPUCores: 1}),
	} {
		if cost.IsPriced || cost.UsdPerReplicaHour != nil || cost.Reason == nil {
			t.Errorf("%s: %+v", name, cost)
		}
	}
}
