package analyze

import (
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/pricing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// applyInfraCost prices one replica of the workload under test at the
// recommended sizing and fills the capacity table's cost_per_hour.
// With source none it does nothing. Prices come from
// the bundle's pricing/infra.json; a static policy may be resolved from the
// RunSpec instead, since the table is in it, but aws prices are never fetched
// at analysis time.
func applyInfraCost(run input.Run, outputs *Outputs) error {
	var policy *spec.InfraPricingPolicy
	if run.Spec.Analysis != nil {
		policy = run.Spec.Analysis.InfraPricing
	}
	source := pricing.Source(policy)
	if source == spec.InfraPricingPolicySourceNone {
		return nil
	}
	table := &outputs.Capacity
	prices := run.InfraPrices
	if prices == nil && source == spec.InfraPricingPolicySourceStatic {
		resolved, err := pricing.FromStatic(*policy, runStart(run))
		if err != nil {
			return err
		}
		prices = &resolved
	}
	if prices == nil {
		reason := "the run has no pricing/infra.json: aws prices are resolved by servtuner run, never at analysis time"
		table.InfraCost = &spec.InfraCost{Source: spec.InfraCostSource(source), Reason: &reason}
		return nil
	}
	hardware, ok := targetHardware(run)
	declared := declaredHardware(run)
	if !ok && declared != nil {
		hardware, ok = pricing.Hardware{InstanceType: declared.Host, Accelerator: declared.Accelerator}, true
	}
	if !ok {
		reason := "no target snapshot with a hardware profile and no declared target.hardware, so the node type and GPU model are unknown"
		table.InfraCost = &spec.InfraCost{Source: spec.InfraCostSource(prices.Source), Reason: &reason}
		return nil
	}
	replica := pricing.Replica{}
	if per := table.PerReplica; per != nil {
		replica.CPUCores, replica.MemoryBytes = per.CpuRequestCores, per.MemoryRequestBytes
	}
	if table.GpusPerReplica != nil {
		replica.GPUs = *table.GpusPerReplica
	} else if declared != nil {
		replica.GPUs = declared.GpuCount // a host's vLLM: one replica holds the GPUs it declares
	}
	cost := pricing.ReplicaCost(*prices, hardware, replica)
	table.InfraCost = &cost
	if cost.IsPriced {
		for i := range table.Rows {
			perHour := float64(table.Rows[i].Replicas) * *cost.UsdPerReplicaHour
			table.Rows[i].CostPerHour = &perHour
		}
	}
	return nil
}

// applyUnitCosts adds a priced replica's cost per thousand requests and per
// million output tokens at the SLO-max, as compare's
// infrastructure cost objectives compute them, so one run shows them without a
// comparison. An unpriced replica gets neither.
func applyUnitCosts(analysis spec.AnalysisResult, outputs *Outputs) {
	cost := outputs.Capacity.InfraCost
	if cost == nil || !cost.IsPriced {
		return
	}
	per1k, per1m, reason := compare.InfraUnitCosts(analysis, cost)
	cost.UsdPer1KRequests, cost.UsdPer1MOutputTokens = per1k, per1m
	if reason != "" {
		cost.UnitCostReason = &reason
	}
}

// targetHardware is the target workload's hardware profile from the pre-run
// snapshot.
func targetHardware(run input.Run) (pricing.Hardware, bool) {
	snapshot := firstSnapshot(run)
	if snapshot == nil {
		return pricing.Hardware{}, false
	}
	for _, workload := range snapshot.Workloads {
		profile := workload.HardwareProfile
		if workload.Role != spec.WorkloadRoleTarget || profile == nil {
			continue
		}
		hardware := pricing.Hardware{Accelerator: profile.Accelerator}
		if profile.NodeInstanceType != nil {
			hardware.InstanceType = *profile.NodeInstanceType
		}
		return hardware, true
	}
	return pricing.Hardware{}, false
}

// declaredHardware is the RunSpec's target.hardware (a target outside Kubernetes), used
// only when the snapshot has no hardware profile for the target.
func declaredHardware(run input.Run) *spec.DeclaredHardware {
	if _, captured := targetHardware(run); captured {
		return nil
	}
	return run.Spec.Target.Hardware
}

// runStart is the first phase's start, so a static table resolved during
// analysis carries a stable time; the zero time when phases are empty.
func runStart(run input.Run) time.Time {
	if len(run.Phases.Phases) == 0 {
		return time.Time{}
	}
	start, err := run.Phases.Phases[0].Start.Time()
	if err != nil {
		return time.Time{}
	}
	return start
}
