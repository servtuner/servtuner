// Package pricing resolves infrastructure prices and prices one replica.
// A run snapshots its resolved prices into
// pricing/infra.json; the analysis prices only from that copy.
package pricing

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// bytesPerGiB converts memory requests to the GiB that node prices are per.
const bytesPerGiB = 1073741824 // 1 GiB = 2^30 bytes

// wattsPerKilowatt converts board power to the kW that electricity is billed by.
const wattsPerKilowatt = 1000

// Source returns the policy's source, none when there is no policy.
func Source(policy *spec.InfraPricingPolicy) spec.InfraPricingPolicySource {
	if policy == nil || policy.Source == "" {
		return spec.InfraPricingPolicySourceNone
	}
	return policy.Source
}

// FromStatic resolves the static table: node prices as given, GPU prices as
// given or from the owned-hardware helper.
func FromStatic(policy spec.InfraPricingPolicy, now time.Time) (spec.InfraPricing, error) {
	if policy.Static == nil {
		return spec.InfraPricing{}, fmt.Errorf("infra_pricing.source is static but infra_pricing.static is missing")
	}
	prices := spec.InfraPricing{
		Source: spec.InfraPricingSourceStatic, ResolvedAt: spec.NewUTCTimestamp(now), Discount: discountOf(policy),
		Nodes: spec.InfraPricingNodes{}, Gpus: spec.InfraPricingGpus{},
	}
	for name, node := range policy.Static.Nodes {
		prices.Nodes[name] = spec.NodePrice{UsdPerHour: node.UsdPerHour, Cores: node.Cores, MemoryGib: node.MemoryGib, Gpus: node.Gpus}
	}
	for model, gpu := range policy.Static.Gpus {
		price, err := gpuPrice(gpu)
		if err != nil {
			return spec.InfraPricing{}, fmt.Errorf("infra_pricing.static.gpus[%q]: %w", model, err)
		}
		prices.Gpus[model] = price
	}
	return prices, nil
}

func discountOf(policy spec.InfraPricingPolicy) float64 {
	if policy.Discount <= 0 {
		return 1
	}
	return policy.Discount
}

// gpuPrice is the per-GPU-hour price: given, or purchase ÷ amortization hours
// plus board power at the electricity price.
func gpuPrice(gpu spec.StaticGpuPrice) (spec.GpuPrice, error) {
	if gpu.UsdPerHour != nil {
		return spec.GpuPrice{UsdPerHour: *gpu.UsdPerHour, Basis: "given"}, nil
	}
	if gpu.PurchaseUsd == nil || gpu.AmortizationHours == nil || gpu.Watts == nil || gpu.UsdPerKwh == nil {
		return spec.GpuPrice{}, fmt.Errorf("needs usd_per_hour, or purchase_usd, amortization_hours, watts and usd_per_kwh")
	}
	perHour := *gpu.PurchaseUsd / *gpu.AmortizationHours + *gpu.Watts/wattsPerKilowatt**gpu.UsdPerKwh
	return spec.GpuPrice{UsdPerHour: perHour, Basis: fmt.Sprintf("%s USD ÷ %s h + %s W ÷ 1000 × %s USD/kWh = %s USD/h",
		derive.FormatNumber(*gpu.PurchaseUsd), derive.FormatNumber(*gpu.AmortizationHours), derive.FormatNumber(*gpu.Watts),
		derive.FormatNumber(*gpu.UsdPerKwh), derive.FormatNumber(perHour))}, nil
}

// Hardware is where a replica runs, from the target snapshot's hardware profile.
type Hardware struct {
	// InstanceType is node.kubernetes.io/instance-type, or local.
	InstanceType string
	// Accelerator is the GPU model, or none.
	Accelerator string
}

// Replica is what one replica holds at the recommended sizing.
type Replica struct {
	CPUCores    float64
	MemoryBytes float64
	GPUs        int
}

// ReplicaCost prices one replica: a GPU replica pays for its GPUs (the GPU
// model's price, else node price ÷ node GPUs); a CPU replica pays for the
// larger of its shares of the node's allocatable cores and memory, which is
// how much of a node it really takes. The discount applies last.
func ReplicaCost(prices spec.InfraPricing, hardware Hardware, replica Replica) spec.InfraCost {
	source := spec.InfraCostSource(prices.Source)
	cost := spec.InfraCost{Source: source}
	unpriced := func(reason string) spec.InfraCost {
		cost.Reason = &reason
		return cost
	}
	discount := prices.Discount
	if discount <= 0 {
		discount = 1
	}
	discountText := ""
	if discount != 1 {
		discountText = fmt.Sprintf(" × discount %s", derive.FormatNumber(discount))
		cost.Discount = &discount
	}
	if replica.GPUs > 0 {
		perGPU, basis, ok := perGPUPrice(prices, hardware)
		if !ok {
			return unpriced(fmt.Sprintf("no price for GPU %q or for node type %q with GPUs in the %s prices", hardware.Accelerator, hardware.InstanceType, prices.Source))
		}
		usd := float64(replica.GPUs) * perGPU * discount
		method := spec.InfraCostMethodGpu
		text := fmt.Sprintf("%d GPU × %s USD/GPU-h (%s)%s = %s USD/h", replica.GPUs, derive.FormatNumber(perGPU), basis, discountText, derive.FormatNumber(usd))
		cost.IsPriced, cost.UsdPerReplicaHour, cost.Method, cost.Basis = true, &usd, &method, &text
		return cost
	}
	node, ok := prices.Nodes[hardware.InstanceType]
	if !ok || hardware.InstanceType == "" {
		return unpriced(fmt.Sprintf("no price for node type %q in the %s prices", hardware.InstanceType, prices.Source))
	}
	if replica.CPUCores <= 0 && replica.MemoryBytes <= 0 {
		return unpriced("the replica has no recommended CPU or memory request to price")
	}
	gib := replica.MemoryBytes / bytesPerGiB
	share := math.Max(replica.CPUCores/node.Cores, gib/node.MemoryGib)
	usd := share * node.UsdPerHour * discount
	method := spec.InfraCostMethodNodeShare
	text := fmt.Sprintf("max(%s cores ÷ %s, %s GiB ÷ %s) = %s of a %s node × %s USD/h%s = %s USD/h",
		derive.FormatNumber(replica.CPUCores), derive.FormatNumber(node.Cores), derive.FormatNumber(gib), derive.FormatNumber(node.MemoryGib),
		derive.FormatNumber(share), hardware.InstanceType, derive.FormatNumber(node.UsdPerHour), discountText, derive.FormatNumber(usd))
	cost.IsPriced, cost.UsdPerReplicaHour, cost.Method, cost.Basis = true, &usd, &method, &text
	return cost
}

func perGPUPrice(prices spec.InfraPricing, hardware Hardware) (float64, string, bool) {
	if gpu, ok := prices.Gpus[hardware.Accelerator]; ok && hardware.Accelerator != "" {
		return gpu.UsdPerHour, hardware.Accelerator + ", " + gpu.Basis, true
	}
	if node, ok := prices.Nodes[hardware.InstanceType]; ok && node.Gpus > 0 {
		return node.UsdPerHour / float64(node.Gpus), fmt.Sprintf("%s at %s USD/h ÷ %d GPUs", hardware.InstanceType, derive.FormatNumber(node.UsdPerHour), node.Gpus), true
	}
	return 0, "", false
}

// InstanceTypes lists the node types a snapshot's workloads ran on, sorted.
func InstanceTypes(snapshot *spec.TargetSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, workload := range snapshot.Workloads {
		if profile := workload.HardwareProfile; profile != nil && profile.NodeInstanceType != nil && *profile.NodeInstanceType != "" {
			seen[*profile.NodeInstanceType] = true
		}
	}
	types := make([]string, 0, len(seen))
	for name := range seen {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}
