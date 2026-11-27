package sizing

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Node ids and formulas of the replica count (Replicas and HPA).
const (
	nodeRPSPerReplica   = "rps_per_replica"
	nodeReplicaHeadroom = "h_rep"
	nodeMinimumReplicas = "min_replicas"
	nodeTargetRPS       = "target_rps"
	nodeReplicas        = "replicas"
	// replicasFormula plans each replica at its SLO-max less headroom. The
	// capacity table evaluates the same formula at every traffic level.
	replicasFormula = "max(1, ceil(target_rps / (rps_per_replica * (1 - h_rep))))"
)

// Rule ids of the replica and HPA recommendations.
const (
	ruleReplicas       = "sizing.replicas"
	ruleHPAMinimum     = "sizing.hpa_min_replicas"
	ruleHPAMaximum     = "sizing.hpa_max_replicas"
	ruleHPAUtilization = "sizing.hpa_cpu_utilization"
)

// availabilityMinimumReplicas survives a pod or node loss.
const availabilityMinimumReplicas = 2

// defaultLadder is the default list of traffic levels.
var defaultLadder = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000}

// maximumTableReplicas trims the ladder: levels past the first one that needs
// more replicas than this are beyond what one run can speak for.
const maximumTableReplicas = 100

// capacityPlan adds the replica and HPA nodes and recommendations for the
// workload under test, and later fills the capacity table from the evaluated
// graph.
type capacityPlan struct {
	perReplica *spec.PerReplica
	headroom   float64
	targetRPS  *float64
	ladder     []float64
	target     *spec.RecommendationTarget
	// keys of the target workload's sized containers.
	keys        []string
	runCaveats  []string
	isLowConf   bool
	stageIndex  int
	gpusPerUnit int
	// hasSloMax: the run found an SLO-max, so a missing perReplica means unknown replicas.
	hasSloMax bool
}

// add puts the replica nodes into the graph and returns the recommendations.
func (p capacityPlan) add(graph *derive.Graph) []spec.Recommendation {
	if p.perReplica == nil || p.target == nil {
		return nil
	}
	zero, almostOne, one := 0.0, 0.95, 1.0
	graph.AddMeasured(nodeRPSPerReplica, "SLO-max RPS per replica", "requests/s", p.perReplica.RpsPerReplica, &derive.Source{StageIndex: &p.stageIndex, File: "analysis.json"})
	graph.AddParameter(nodeReplicaHeadroom, "Replica headroom under the SLO-max", "", p.headroom, &derive.Bounds{Min: &zero, Max: &almostOne}, "sizing policy: replicas and HPA")
	graph.AddParameter(nodeMinimumReplicas, "Minimum replicas for availability", "replicas", availabilityMinimumReplicas, &derive.Bounds{Min: &one}, "sizing policy: survive a pod or node loss")
	var recommendations []spec.Recommendation
	recommend := func(rule, kind, name, unit, node string, evidence, caveats []string) {
		recommendations = append(recommendations, p.recommendation(rule, kind, name, unit, node, evidence, caveats))
	}
	recommend(ruleHPAMinimum, "hpa", "min_replicas", "replicas", nodeMinimumReplicas, nil,
		[]string{"For anything that must survive a pod or node loss; 1 is enough for a non-critical target."})
	if p.targetRPS != nil {
		graph.AddParameter(nodeTargetRPS, "Target traffic", "requests/s", *p.targetRPS, &derive.Bounds{Min: &zero}, "RunSpec analysis.target_rps")
		graph.AddDerived(nodeReplicas, "Replicas at the target traffic", "replicas", strings.ReplaceAll(replicasFormula, "target_rps", nodeTargetRPS))
		evidence := []string{fmt.Sprintf("One replica met the SLOs at %s RPS", derive.FormatNumber(p.perReplica.RpsPerReplica))}
		recommend(ruleReplicas, "replicas", "replicas", "replicas", nodeReplicas, evidence, nil)
		recommend(ruleHPAMaximum, "hpa", "max_replicas", "replicas", nodeReplicas, evidence,
			[]string{"The replicas the target traffic needs; raise it once the real peak is known."})
	}
	if utilization := p.cpuUtilizationFormula(); utilization != "" {
		node := string(p.target.Role) + ".hpa.cpu_utilization"
		graph.AddDerived(node, "HPA CPU target, % of the request", "%", utilization)
		recommend(ruleHPAUtilization, "hpa", "cpu_utilization", "%", node,
			[]string{"The pod's CPU p95 over its request at the operating point, less the replica headroom"},
			[]string{"Scale on CPU for routers. The HPA max is set from the capacity table once peak traffic is known."})
	}
	return recommendations
}

// cpuUtilizationFormula is the HPA target: the pod's CPU at the operating
// point as a share of its requests (the HPA's own definition of utilization),
// less the replica headroom. Empty for vLLM, where CPU is a poor scaling
// signal.
func (p capacityPlan) cpuUtilizationFormula() string {
	if p.target.TargetType == spec.TargetTypeVllm || len(p.keys) == 0 {
		return ""
	}
	usage, requests := make([]string, len(p.keys)), make([]string, len(p.keys))
	for i, key := range p.keys {
		usage[i], requests[i] = key+".cpu.p95", key+".cpu.request"
	}
	return fmt.Sprintf("floor(100 * (%s) / (%s) * (1 - %s))", strings.Join(usage, " + "), strings.Join(requests, " + "), nodeReplicaHeadroom)
}

func (p capacityPlan) recommendation(rule, kind, name, unit, node string, evidence, caveats []string) spec.Recommendation {
	confidence := spec.RecommendationConfidenceMedium
	if p.isLowConf {
		confidence = spec.RecommendationConfidenceLow
	}
	items := make([]spec.RecommendationEvidence, len(evidence))
	for i, text := range evidence {
		items[i] = spec.RecommendationEvidence{Text: text, StageIndex: intPointer(p.stageIndex)}
	}
	return spec.Recommendation{
		Id: rule + "/" + string(p.target.Role), Rule: rule, Target: *p.target,
		Subject: spec.RecommendationSubject{Kind: spec.RecommendationSubjectKind(kind), Name: name},
		Unit:    &unit, DerivationNode: &node, Tier: spec.RecommendationTierDiagnostic, Confidence: confidence,
		Evidence: items, Caveats: append(append([]string{}, caveats...), p.runCaveats...),
	}
}

// noTableReason says which input the capacity table lacks. An SLO-max with no per-replica capacity
// means the target's replicas are unknown: a target outside Kubernetes (a host, an external
// endpoint) has no workload to count them from.
func (p capacityPlan) noTableReason() string {
	switch {
	case !p.hasSloMax:
		return "the run has no SLO-max, so there is no per-replica capacity to scale"
	case p.perReplica == nil:
		return "the target's ready replicas are unknown (no Kubernetes workload in the snapshot, as for a host target), so the SLO-max cannot be divided per replica"
	default:
		return "no container of the target workload was sized, so there are no per-replica requests to scale"
	}
}

// table evaluates the replica formula at every traffic level (the
// capacity table), with the per-replica requests summed over the workload's
// containers.
func (p capacityPlan) table(result *derive.Result, analyzerVersion string) (spec.CapacityTable, error) {
	document := spec.CapacityTable{AnalyzerVersion: analyzerVersion, Rows: []spec.CapacityRow{}, Caveats: []string{}}
	if p.perReplica == nil || p.target == nil {
		reason := p.noTableReason()
		document.Reason = &reason
		return document, nil
	}
	cpu, memory := p.perReplicaRequests(result)
	verified := false
	document.RpsPerReplica, document.ReplicaHeadroom, document.IsScaleOutVerified = &p.perReplica.RpsPerReplica, &p.headroom, &verified
	document.PerReplica = &spec.CapacityTablePerReplica{CpuRequestCores: cpu, MemoryRequestBytes: memory}
	if p.gpusPerUnit > 0 {
		document.GpusPerReplica = &p.gpusPerUnit
	}
	document.Caveats = append([]string{"Linear scale-out is assumed: per-replica RPS is unverified until runs at two or more replica counts agree within 10%."}, p.runCaveats...)
	expr, err := derive.Parse(replicasFormula)
	if err != nil {
		return spec.CapacityTable{}, err
	}
	isPastTheTable := false
	for _, rps := range p.levels() {
		isTarget := p.targetRPS != nil && rps == *p.targetRPS
		if isPastTheTable && !isTarget {
			continue
		}
		env := func(id string) (float64, bool) {
			if id == nodeTargetRPS {
				return rps, true
			}
			return result.Value(id)
		}
		value, err := derive.Eval(expr, env)
		if err != nil {
			return spec.CapacityTable{}, fmt.Errorf("replicas at %s RPS: %w", derive.FormatNumber(rps), err)
		}
		replicas := int(value)
		row := spec.CapacityRow{
			Rps: rps, Replicas: replicas, CpuRequestCores: snapQuantity(float64(replicas) * cpu), MemoryRequestBytes: float64(replicas) * memory,
			IsTargetRps: isTarget,
		}
		if p.gpusPerUnit > 0 {
			gpus := replicas * p.gpusPerUnit
			row.Gpus = &gpus
		}
		document.Rows = append(document.Rows, row)
		isPastTheTable = isPastTheTable || replicas > maximumTableReplicas

	}
	return document, nil
}

// levels is the ladder (the RunSpec's or the default) plus target_rps, in order.
func (p capacityPlan) levels() []float64 {
	levels := append([]float64{}, p.ladder...)
	if len(levels) == 0 {
		levels = append(levels, defaultLadder...)
	}
	if p.targetRPS != nil && !slices.Contains(levels, *p.targetRPS) {
		levels = append(levels, *p.targetRPS)
	}
	slices.Sort(levels)
	return levels
}

func (p capacityPlan) perReplicaRequests(result *derive.Result) (cpu, memory float64) {
	for _, key := range p.keys {
		if value, ok := result.Value(key + ".cpu.request"); ok {
			cpu += value
		}
		if value, ok := result.Value(key + ".memory.request"); ok {
			memory += value
		}
	}
	return snapQuantity(cpu), memory
}

// snapQuantity drops binary noise from a sum of rounded requests.
func snapQuantity(value float64) float64 { return math.Round(value*1e9) / 1e9 }

// CapacityCSV writes the capacity table's rows for spreadsheets
// (capacity.csv). Columns follow capacity.json; absent values are empty.
func CapacityCSV(table spec.CapacityTable) []byte {
	var out strings.Builder
	out.WriteString("rps,replicas,gpus,cpu_request_cores,memory_request_bytes,cost_per_hour,is_target_rps\n")
	optionalInt := func(value *int) string {
		if value == nil {
			return ""
		}
		return fmt.Sprint(*value)
	}
	optionalFloat := func(value *float64) string {
		if value == nil {
			return ""
		}
		return derive.FormatNumber(*value)
	}
	for _, row := range table.Rows {
		fmt.Fprintf(&out, "%s,%d,%s,%s,%s,%s,%t\n", derive.FormatNumber(row.Rps), row.Replicas, optionalInt(row.Gpus),
			derive.FormatNumber(row.CpuRequestCores), derive.FormatNumber(row.MemoryRequestBytes), optionalFloat(row.CostPerHour), row.IsTargetRps)
	}
	return []byte(out.String())
}
