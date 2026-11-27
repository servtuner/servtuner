package plan

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// planValues is patches/plan-values.yaml:
// replicas, HPA and per-replica resources for each tier.
type planValues struct {
	Tiers []tierValues `yaml:"tiers"`
}

type tierValues struct {
	Key        string            `yaml:"key"`
	RunID      string            `yaml:"run_id"`
	TargetType string            `yaml:"target_type"`
	Replicas   int               `yaml:"replicas"`
	HPA        hpaValues         `yaml:"hpa"`
	Requests   map[string]string `yaml:"requests,omitempty"`
	Limits     map[string]string `yaml:"limits,omitempty"`
}

type hpaValues struct {
	MinReplicas                 int  `yaml:"minReplicas"`
	MaxReplicas                 int  `yaml:"maxReplicas"`
	TargetCPUUtilizationPercent *int `yaml:"targetCPUUtilizationPercent,omitempty"`
}

// header lists every override and every unresolved warning, so an exported
// patch never hides that a person changed the plan (Scenario outputs).
func header(title string, request spec.PlanRequest, result spec.Plan) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("# servtuner %s, analyzer %s, for %s RPS at peak.", title, result.AnalyzerVersion, derive.FormatNumber(request.PeakRps)))
	for _, override := range request.Overrides {
		lines = append(lines, fmt.Sprintf("# override: %s %s = %s", override.Kind, override.Node, derive.FormatNumber(override.Value)))
	}
	for _, check := range result.Checks {
		if check.Status != spec.PlanCheckStatusOk {
			lines = append(lines, fmt.Sprintf("# %s: %s", check.Status, check.Text))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// RenderValues writes patches/plan-values.yaml; blocked tiers are left out.
func RenderValues(request spec.PlanRequest, result spec.Plan) ([]byte, error) {
	var values planValues
	for _, tier := range result.Tiers {
		if tier.IsBlocked || tier.ReplicasPeak == nil || tier.Hpa == nil {
			continue
		}
		entry := tierValues{
			Key: tier.Key, RunID: tier.RunId, TargetType: string(tier.TargetType), Replicas: *tier.ReplicasPeak,
			HPA: hpaValues{MinReplicas: tier.Hpa.MinReplicas, MaxReplicas: tier.Hpa.MaxReplicas},
		}
		if utilization := tier.Hpa.CpuUtilizationPercent; utilization != nil {
			percent := int(*utilization)
			entry.HPA.TargetCPUUtilizationPercent = &percent
		}
		if perReplica := tier.PerReplica; perReplica != nil {
			entry.Requests = map[string]string{"cpu": sizing.CPUQuantity(perReplica.CpuRequestCores), "memory": sizing.MemoryQuantity(perReplica.MemoryRequestBytes)}
			entry.Limits = map[string]string{}
			if perReplica.CpuLimitCores != nil {
				entry.Limits["cpu"] = sizing.CPUQuantity(*perReplica.CpuLimitCores)
			}
			if perReplica.MemoryLimitBytes != nil {
				entry.Limits["memory"] = sizing.MemoryQuantity(*perReplica.MemoryLimitBytes)
			}
		}
		values.Tiers = append(values.Tiers, entry)
	}
	body, err := yaml.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("render patches/plan-values.yaml: %w", err)
	}
	return append([]byte(header("plan values", request, result)), body...), nil
}

// RenderMarkdown writes plan.md: a summary per tier for people.
func RenderMarkdown(request spec.PlanRequest, result spec.Plan) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "# Capacity plan for %s RPS\n\n", derive.FormatNumber(request.PeakRps))
	for _, tier := range result.Tiers {
		fmt.Fprintf(&out, "## %s (%s, run %s)\n\n", tier.Key, tier.TargetType, tier.RunId)
		if tier.IsBlocked {
			fmt.Fprintf(&out, "**Blocked:** %s\n\n", *tier.BlockedReason)
			continue
		}
		fmt.Fprintf(&out, "| | |\n|---|---|\n")
		fmt.Fprintf(&out, "| SLO-max per replica | %s RPS%s |\n", derive.FormatNumber(*tier.RpsPerReplica), interpolatedNote(tier))
		fmt.Fprintf(&out, "| Replicas at peak | %d (%.0f%% of the SLO-max each) |\n", *tier.ReplicasPeak, 100**tier.UtilizationAtPeak)
		if tier.ReplicasBaseline != nil {
			fmt.Fprintf(&out, "| Replicas at baseline | %d |\n", *tier.ReplicasBaseline)
		}
		fmt.Fprintf(&out, "| HPA | min %d, max %d |\n", tier.Hpa.MinReplicas, tier.Hpa.MaxReplicas)
		if totals := tier.Totals; totals != nil {
			fmt.Fprintf(&out, "| Requests in total | %s CPU, %s memory |\n", sizing.CPUQuantity(totals.CpuRequestCores), readableBytes(totals.MemoryRequestBytes))
		}
		if memory := tier.Memory; memory != nil {
			fmt.Fprintf(&out, "| Memory at C + B, per replica | %s (%s) |\n", memoryText(*memory), memory.Basis)
		}
		for _, row := range tier.Sensitivity {
			fmt.Fprintf(&out, "| At %s RPS (×%s) | %d replicas |\n", derive.FormatNumber(row.Rps), derive.FormatNumber(row.TrafficFactor), row.Replicas)
		}
		fmt.Fprintf(&out, "\nConfidence: %s. Scope: %s.\n\n", tier.Confidence, strings.Join(tier.Scope, "; "))
		for _, warning := range tier.Warnings {
			fmt.Fprintf(&out, "- Warning: %s\n", warning)
		}
		if len(tier.Warnings) > 0 {
			out.WriteString("\n")
		}
	}
	if len(request.Overrides) > 0 {
		out.WriteString("## Overrides\n\n")
		for _, override := range request.Overrides {
			fmt.Fprintf(&out, "- %s `%s` = %s\n", override.Kind, override.Node, derive.FormatNumber(override.Value))
		}
	}
	return []byte(out.String())
}

// memoryText is the working set at C + B, as a range when k was assumed.
func memoryText(memory spec.PlanTierMemory) string {
	high := readableBytes(memory.WorkingSetBytes)
	if memory.WorkingSetBytesLow != nil {
		return readableBytes(*memory.WorkingSetBytesLow) + " to " + high
	}
	return high
}

// readableBytes is a size for people: GiB or MiB with one decimal.
func readableBytes(bytes float64) string {
	if bytes >= 1073741824 { // 1 GiB
		return fmt.Sprintf("%.1f GiB", bytes/(1<<30))
	}
	return fmt.Sprintf("%.1f MiB", bytes/(1<<20))
}

func interpolatedNote(tier spec.PlanTier) string {
	if tier.IsInterpolated != nil && *tier.IsInterpolated && tier.LimitingSlo != nil {
		return fmt.Sprintf(", interpolated where %s crosses its limit", *tier.LimitingSlo)
	}
	return ""
}
