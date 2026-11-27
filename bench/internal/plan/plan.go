package plan

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Availability defaults when the request has no availability block.
const (
	defaultMinimumReplicas = 2
	defaultZones           = 1
	queueSafety            = 1.1
)

// sensitivityFactors are +25%, +50% and +100% traffic.
var sensitivityFactors = []float64{1.25, 1.5, 2}

// Node names inside a tier (the tier key prefixes them).
const (
	nodeRPSPerReplica = "rps_per_replica"
	nodeHeadroom      = "h_rep"
	nodePeak          = "peak_rps"
	nodeBaseline      = "baseline_rps"
	nodeMinimum       = "min_replicas"
	nodeSpare         = "spare"
	nodeZones         = "zones"
	nodeNeeded        = "replicas_needed"
	nodeReplicasPeak  = "replicas_peak"
	nodeReplicasBase  = "replicas_baseline"
	nodeUtilization   = "utilization_at_peak"
	nodeWMean         = "w_mean"
	nodeConcurrency   = "concurrency"
)

// replicasAt is the replica formula at a traffic node: enough replicas at the
// per-replica SLO-max less headroom, at least the availability floor, plus a
// spare, rounded up to whole zones.
func replicasAt(key, traffic string) string {
	n := func(name string) string { return key + "." + name }
	needed := fmt.Sprintf("max(1, ceil(%s / (%s * (1 - %s))))", traffic, n(nodeRPSPerReplica), n(nodeHeadroom))
	return fmt.Sprintf("ceil(max(%s, %s + %s) / %s) * %s", n(nodeMinimum), needed, n(nodeSpare), n(nodeZones), n(nodeZones))
}

// Make plans every model against the request.
// series are payload-series models: runs of a tier's configuration at other
// request sizes, used only to fit its m(s).
func Make(request spec.PlanRequest, models, series []spec.RunCapacityModel, analyzerVersion string) (spec.Plan, error) {
	result := spec.Plan{AnalyzerVersion: analyzerVersion, Tiers: []spec.PlanTier{}, Derivation: spec.PlanDerivation{Nodes: []spec.DerivationNode{}}, Checks: []spec.PlanCheck{}}
	if len(models) == 0 {
		return spec.Plan{}, errors.New("plan needs at least one capacity model")
	}
	if err := checkWorkloads(request, models); err != nil {
		return spec.Plan{}, err
	}
	thresholds, err := thresholdsOf(request.Slos)
	if err != nil {
		return spec.Plan{}, err
	}
	models, scaleOutChecks := mergeScaleOut(models, thresholds)
	graph := derive.NewGraph()
	var tiers []*tierPlan
	keys := tierKeys(models)
	for i, model := range models {
		tiers = append(tiers, &tierPlan{key: keys[i], model: model, request: request})
	}
	if err := payloadSeries(tiers, series); err != nil {
		return spec.Plan{}, err
	}
	for _, tier := range tiers {
		tier.add(graph, thresholds)
	}
	evaluated, err := graph.Evaluate(overridesOf(request)...)
	if err != nil {
		return spec.Plan{}, fmt.Errorf("evaluate the plan: %w", err)
	}
	for _, tier := range tiers {
		planned, checks, err := tier.result(evaluated)
		if err != nil {
			return spec.Plan{}, err
		}
		result.Tiers = append(result.Tiers, planned)
		if check, ok := scaleOutChecks[tier.model.RunId]; ok {
			key := tier.key
			check.Tier = &key
			result.Checks = append(result.Checks, check)
		}
		result.Checks = append(result.Checks, checks...)
	}
	result.Derivation.Nodes = evaluated.SpecNodes()
	return result, nil
}

// tierKeys names each tier after its target type, which overrides then use
// ("bifrost.h_rep"); a second tier of the same type gets a suffix ("vllm_2").
func tierKeys(models []spec.RunCapacityModel) []string {
	seen := map[string]int{}
	keys := make([]string, len(models))
	for i, model := range models {
		base := nodeIDUnsafe.ReplaceAllString(string(model.Identity.TargetType), "_")
		seen[base]++
		keys[i] = base
		if seen[base] > 1 {
			keys[i] = fmt.Sprintf("%s_%d", base, seen[base])
		}
	}
	return keys
}

// nodeIDUnsafe matches what a derivation node id cannot hold.
var nodeIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_]`)

// checkWorkloads refuses to plan models of different request mixes together
// unless the request names the one to plan (Workload).
func checkWorkloads(request spec.PlanRequest, models []spec.RunCapacityModel) error {
	for _, model := range models {
		if request.WorkloadHash != nil && model.Identity.WorkloadHash != *request.WorkloadHash {
			return fmt.Errorf("model of run %s measured workload %s, not the requested %s: a workload that was not measured cannot be planned",
				model.RunId, model.Identity.WorkloadHash[:12], (*request.WorkloadHash)[:12])
		}
		if model.Identity.WorkloadHash != models[0].Identity.WorkloadHash && request.WorkloadHash == nil {
			return errors.New("the models measured different workloads: name the one to plan in workload_hash")
		}
	}
	return nil
}

func overridesOf(request spec.PlanRequest) []derive.Override {
	overrides := make([]derive.Override, 0, len(request.Overrides))
	for _, override := range request.Overrides {
		overrides = append(overrides, derive.Override{Node: override.Node, Kind: derive.OverrideKind(override.Kind), Value: override.Value})
	}
	return overrides
}

// tierPlan is one model's part of the plan.
type tierPlan struct {
	key      string
	model    spec.RunCapacityModel
	request  spec.PlanRequest
	capacity capacity
	// blocked is why the tier cannot be planned, or empty.
	blocked string
	// series are the tier's payload-series models; memory is its m(s).
	series []spec.RunCapacityModel
	memory *memoryPlan
}

func (t *tierPlan) node(name string) string { return t.key + "." + name }

func (t *tierPlan) add(graph *derive.Graph, thresholds []threshold) {
	if t.model.Identity.QualityGrade == spec.QualityGradeUnusable {
		t.blocked = "the model's run is graded unusable (Guard rail 4)"
		return
	}
	if t.model.ReadyReplicas < 1 {
		t.blocked = "the model's run has no known replica count, so its load cannot be divided per replica"
		return
	}
	capacity, err := sloMaxPerReplica(t.model, thresholds)
	if err != nil {
		t.blocked = err.Error()
		return
	}
	t.capacity = capacity
	availability := availabilityOf(t.request)
	zero, one, almostOne := 0.0, 1.0, 0.95
	stage := capacity.stageIndex
	graph.AddMeasured(t.node(nodeRPSPerReplica), "SLO-max RPS per replica at the requested SLO", "requests/s", capacity.rps, &derive.Source{StageIndex: &stage, File: "capacity_model.json"})
	graph.AddParameter(t.node(nodeHeadroom), "Replica headroom", "", t.request.ReplicaHeadroom, &derive.Bounds{Min: &zero, Max: &almostOne}, "sizing policy: replicas and HPA")
	graph.AddParameter(t.node(nodePeak), "Peak traffic", "requests/s", t.request.PeakRps, &derive.Bounds{Min: &zero}, "plan request peak_rps")
	graph.AddParameter(t.node(nodeMinimum), "Minimum replicas for availability", "replicas", float64(availability.MinReplicas), &derive.Bounds{Min: &one}, "availability policy")
	graph.AddParameter(t.node(nodeSpare), "Spare replicas (N+1)", "replicas", boolNumber(availability.Spare), &derive.Bounds{Min: &zero}, "availability policy")
	graph.AddParameter(t.node(nodeZones), "Zones to spread across", "zones", float64(availability.Zones), &derive.Bounds{Min: &one}, "availability policy")
	graph.AddDerived(t.node(nodeNeeded), "Replicas the peak needs", "replicas",
		fmt.Sprintf("max(1, ceil(%s / (%s * (1 - %s))))", t.node(nodePeak), t.node(nodeRPSPerReplica), t.node(nodeHeadroom)))
	graph.AddDerived(t.node(nodeReplicasPeak), "Replicas at peak", "replicas", replicasAt(t.key, t.node(nodePeak)))
	graph.AddDerived(t.node(nodeUtilization), "Per-replica load at peak over the SLO-max", "",
		fmt.Sprintf("%s / (%s * %s)", t.node(nodePeak), t.node(nodeReplicasPeak), t.node(nodeRPSPerReplica)))
	if t.request.BaselineRps != nil {
		graph.AddParameter(t.node(nodeBaseline), "Baseline traffic", "requests/s", *t.request.BaselineRps, &derive.Bounds{Min: &zero}, "plan request baseline_rps")
		graph.AddDerived(t.node(nodeReplicasBase), "Replicas at baseline", "replicas", replicasAt(t.key, t.node(nodeBaseline)))
	}
	if queue := t.model.Queueing; queue != nil {
		graph.AddMeasured(t.node(nodeWMean), "Mean time in system", "s", queue.WMeanSeconds, &derive.Source{StageIndex: &stage, File: "capacity_model.json"})
		graph.AddDerived(t.node(nodeConcurrency), "Concurrency per replica at the planned load (Poisson)", "requests",
			fmt.Sprintf("max(1, ceil(poisson_q(0.99, %s / %s * %s) * %s))", t.node(nodePeak), t.node(nodeReplicasPeak), t.node(nodeWMean), derive.FormatNumber(queueSafety)))
		t.addMemory(graph)
	}
}

func availabilityOf(request spec.PlanRequest) spec.PlanRequestAvailability {
	if request.Availability == nil {
		return spec.PlanRequestAvailability{MinReplicas: defaultMinimumReplicas, Zones: defaultZones}
	}
	return *request.Availability
}

func boolNumber(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// result reads the tier's values from the evaluated graph and runs its checks.
func (t *tierPlan) result(evaluated *derive.Result) (spec.PlanTier, []spec.PlanCheck, error) {
	tier := spec.PlanTier{
		Key: t.key, RunId: t.model.RunId, TargetType: t.model.Identity.TargetType,
		Scope: t.scope(), Confidence: spec.PlanTierConfidenceMedium, Warnings: []string{},
	}
	if t.blocked != "" {
		tier.IsBlocked, tier.BlockedReason, tier.Confidence = true, &t.blocked, spec.PlanTierConfidenceLow
		return tier, []spec.PlanCheck{{Status: spec.PlanCheckStatusBlocked, Tier: &t.key, Text: t.blocked}}, nil
	}
	value := func(name string) float64 {
		v, _ := evaluated.Value(t.node(name))
		return v
	}
	perReplicaRPS, peakReplicas := value(nodeRPSPerReplica), int(value(nodeReplicasPeak))
	utilization := value(nodeUtilization)
	tier.RpsPerReplica, tier.IsInterpolated, tier.ReplicasPeak, tier.UtilizationAtPeak = &perReplicaRPS, &t.capacity.isInterpolated, &peakReplicas, &utilization
	if t.capacity.limitingSLO != "" {
		tier.LimitingSlo = &t.capacity.limitingSLO
	}
	hpa := &spec.PlanTierHpa{MinReplicas: int(value(nodeMinimum)), MaxReplicas: peakReplicas}
	if t.request.BaselineRps != nil {
		baseline := int(value(nodeReplicasBase))
		tier.ReplicasBaseline, hpa.MinReplicas = &baseline, baseline
	}
	if perReplica := t.model.PerReplica; perReplica != nil {
		tier.PerReplica = perReplica
		hpa.CpuUtilizationPercent = perReplica.HpaCpuUtilizationPercent
		totals := &spec.PlanTierTotals{
			CpuRequestCores: math.Round(float64(peakReplicas)*perReplica.CpuRequestCores*1e9) / 1e9, MemoryRequestBytes: float64(peakReplicas) * perReplica.MemoryRequestBytes,
		}
		if perReplica.Gpus != nil {
			gpus := peakReplicas * *perReplica.Gpus
			totals.Gpus = &gpus
		}
		tier.Totals = totals
	}
	tier.Hpa = hpa
	sensitivity, err := t.sensitivity(evaluated)
	if err != nil {
		return spec.PlanTier{}, nil, err
	}
	tier.Sensitivity = sensitivity
	if concurrency, ok := evaluated.Value(t.node(nodeConcurrency)); ok {
		tier.Queue = &spec.PlanTierQueue{Concurrency: int(concurrency)}
	}
	checks := t.checks(evaluated, utilization, &tier)
	tier.Confidence = t.confidence(evaluated)
	return tier, checks, nil
}

// sensitivity evaluates the peak replica formula at more traffic.
func (t *tierPlan) sensitivity(evaluated *derive.Result) ([]spec.PlanTierSensitivityElem, error) {
	expr, err := derive.Parse(replicasAt(t.key, t.node(nodePeak)))
	if err != nil {
		return nil, err
	}
	peak, _ := evaluated.Value(t.node(nodePeak))
	var rows []spec.PlanTierSensitivityElem
	for _, factor := range sensitivityFactors {
		rps := peak * factor
		replicas, err := derive.Eval(expr, func(id string) (float64, bool) {
			if id == t.node(nodePeak) {
				return rps, true
			}
			return evaluated.Value(id)
		})
		if err != nil {
			return nil, fmt.Errorf("sensitivity at %s×: %w", derive.FormatNumber(factor), err)
		}
		rows = append(rows, spec.PlanTierSensitivityElem{TrafficFactor: factor, Rps: rps, Replicas: int(replicas)})
	}
	return rows, nil
}

// checks are the guard rails and consequence checks.
func (t *tierPlan) checks(evaluated *derive.Result, utilization float64, tier *spec.PlanTier) []spec.PlanCheck {
	var checks []spec.PlanCheck
	add := func(status spec.PlanCheckStatus, text string) {
		checks = append(checks, spec.PlanCheck{Status: status, Tier: &t.key, Text: text})
		if status == spec.PlanCheckStatusWarning {
			tier.Warnings = append(tier.Warnings, text)
		}
	}
	if utilization > 1 {
		// Guard rail 1: no replica busier than it was ever measured.
		add(spec.PlanCheckStatusBlocked, fmt.Sprintf("Each replica would run at %.0f%% of its measured SLO-max: more traffic needs more replicas, never busier ones.", 100*utilization))
		tier.IsBlocked = true
		reason := "a replica would run past its measured SLO-max (Guard rail 1)"
		tier.BlockedReason = &reason
	} else {
		add(spec.PlanCheckStatusOk, fmt.Sprintf("Each replica runs at %.0f%% of its measured SLO-max at peak.", 100*utilization))
	}
	if node, ok := evaluated.Node(t.node(nodeReplicasPeak)); ok && node.IsPinned && node.Derivable != nil && node.Value < *node.Derivable {
		add(spec.PlanCheckStatusViolatesMeasured, fmt.Sprintf("%s replicas pinned, below the %s the measurements call for.", derive.FormatNumber(node.Value), derive.FormatNumber(*node.Derivable)))
	}
	if t.capacity.isAtLeast {
		add(spec.PlanCheckStatusWarning, fmt.Sprintf("No measured stage failed the SLO: %s RPS per replica is a floor, so this plan may hold more replicas than needed. A heavier stage would show the real limit.", derive.FormatNumber(t.capacity.rps)))
	}
	if t.model.Identity.QualityGrade == spec.QualityGradeDegraded {
		add(spec.PlanCheckStatusWarning, "The model's run is graded degraded (Guard rail 4).")
	}
	tier.Memory = t.memoryResult(evaluated, add)
	return checks
}

// scope are the labels every number of the tier carries (Guard rail 3).
func (t *tierPlan) scope() []string {
	scope := []string{}
	if t.model.Identity.ArrivalProcess == spec.PacingPoisson {
		scope = append(scope, "for Poisson-like traffic")
	} else {
		scope = append(scope, "for traffic as smooth as "+string(t.model.Identity.ArrivalProcess)+" pacing")
	}
	if !t.model.ScaleOut.IsVerified {
		scope = append(scope, "linear scale-out, unverified: run at two replica counts to verify it")
	}
	if t.model.Identity.HardwareProfileId != nil {
		scope = append(scope, "on hardware profile "+*t.model.Identity.HardwareProfileId)
	}
	workload := "for workload " + t.model.Identity.WorkloadHash[:12]
	if preset := t.model.Identity.WorkloadPreset; preset != nil {
		// The preset name is what a reader recognises; the hash stays the identity.
		workload = fmt.Sprintf("for workload preset %s (%s)", spec.FormatPresetReference(preset.Name, preset.Version), t.model.Identity.WorkloadHash[:12])
	}
	scope = append(scope, workload)
	return scope
}

// confidence is medium, lowered by interpolation, a degraded model or a
// hypothetical input (Confidence).
func (t *tierPlan) confidence(evaluated *derive.Result) spec.PlanTierConfidence {
	if t.capacity.isInterpolated || t.model.Identity.QualityGrade == spec.QualityGradeDegraded {
		return spec.PlanTierConfidenceLow
	}
	for _, node := range evaluated.Nodes {
		if node.IsHypothetical && strings.HasPrefix(node.ID, t.key+".") {
			return spec.PlanTierConfidenceLow
		}
	}
	return spec.PlanTierConfidenceMedium
}
