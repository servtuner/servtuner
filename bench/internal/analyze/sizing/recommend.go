package sizing

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/queueing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// minimumStagesForMediumConfidence: with a single usable stage a sizing has
// nothing to corroborate it (Confidence).
const minimumStagesForMediumConfidence = 2

// Input is what the sizing reads: the run's raw inputs and its analysis.
type Input struct {
	AnalyzerVersion string
	RunID           string
	RunSpec         spec.RunSpec
	Analysis        spec.AnalysisResult
	Stages          []input.Stage
	Matrix          input.MatrixSource
	// Interval is the effective scrape interval, which sets how much of each
	// stage's start is dropped from gauge statistics.
	Interval  time.Duration
	Pre, Post *spec.TargetSnapshot
	// Queue is the operating stage's queueing statistics; nil when
	// the stage has no completed requests.
	Queue *queueing.Input
}

// Outputs are the files the sizing writes.
type Outputs struct {
	Recommendations spec.Recommendations
	Capacity        spec.CapacityTable
	// ResourcesPatch is patches/resources.yaml.
	ResourcesPatch []byte
	// Usage is what each sized container used, for the target advisors.
	Usage []ContainerUsage
	// Queue is the queueing input as sizing completed it, with the router's
	// memory fit; nil without one.
	Queue *queueing.Input
}

// ContainerUsage is one container's measurements at the operating point and
// the limits its pod spec had, in cores and bytes (zero when unset).
type ContainerUsage struct {
	Target                          spec.RecommendationTarget
	Pods                            map[string]bool
	CPUP95, CPUPeak                 float64
	Throttle                        float64
	ThrottleStage                   int
	MemoryPeak                      float64
	CPULimitCores, MemoryLimitBytes float64
}

// Recommend sizes every container of the workloads under test at the
// operating point, plans replicas and the HPA, and builds the
// capacity table and the resources patch from the same evaluated graph.
func Recommend(in Input) (Outputs, error) {
	resolved, err := spec.ResolveAnalysis(in.RunSpec.Analysis)
	if err != nil {
		return Outputs{}, err
	}
	settings, err := SettingsFrom(resolved)
	if err != nil {
		return Outputs{}, err
	}
	point := OperatingPoint(in.Analysis, resolved.TargetRps)
	if point.Mode != spec.OperatingPointModeUnavailable {
		point.Caveats = operatingPointCaveats(in.Analysis, point)
	}
	document := spec.Recommendations{
		AnalyzerVersion: in.AnalyzerVersion, OperatingPoint: point, Policy: policyOf(resolved),
		Recommendations: []spec.Recommendation{}, Derivation: spec.RecommendationsDerivation{Nodes: []spec.DerivationNode{}},
	}
	graph := derive.NewGraph()
	plan := capacityPlan{headroom: resolved.Headroom.Replicas, targetRPS: resolved.TargetRps, ladder: resolved.CapacityLadderRps}
	var usage []ContainerUsage
	var queue *queueing.Input
	if point.Mode != spec.OperatingPointModeUnavailable {
		if usage, queue, err = sizeContainers(in, settings, point, graph, &document, &plan); err != nil {
			return Outputs{}, err
		}
	}
	result, err := graph.Evaluate()
	if err != nil {
		return Outputs{}, fmt.Errorf("evaluate the sizing: %w", err)
	}
	queueChecks := queueing.Checks(result, queue)
	for i := range document.Recommendations {
		recommendation := &document.Recommendations[i]
		value, _ := result.Value(*recommendation.DerivationNode)
		recommendation.Recommended = value
		if recommendation.Subject.Kind == spec.RecommendationSubjectKindKnob && strings.HasPrefix(recommendation.Rule, "queueing.") {
			recommendation.Caveats = append(recommendation.Caveats, queueChecks...)
			queueing.AnnotateQueue(result, recommendation, queue)
		}
	}
	document.Derivation.Nodes = result.SpecNodes()
	capacity, err := plan.table(result, in.AnalyzerVersion)
	if err != nil {
		return Outputs{}, err
	}
	patch, err := renderResourcesPatch(document, patchHeader(in, point))
	if err != nil {
		return Outputs{}, err
	}
	return Outputs{Recommendations: document, Capacity: capacity, ResourcesPatch: patch, Usage: usage, Queue: queue}, nil
}

// sizeContainers adds every container's sizing and the replica plan to the
// graph and the document.
// It returns the queueing input as completed for the run, which the checks
// read after evaluation.
func sizeContainers(in Input, settings Settings, point spec.OperatingPoint, graph *derive.Graph, document *spec.Recommendations, plan *capacityPlan) ([]ContainerUsage, *queueing.Input, error) {
	operating, usable, err := stagesOf(in, *point.StageIndex)
	if err != nil {
		return nil, nil, err
	}
	var usage []ContainerUsage
	var memoryFit *queueing.MemoryFit
	var memoryLimitNode string
	addParameters(graph, settings)
	measurer := measurer{matrix: in.Matrix, interval: in.Interval, operating: operating, usable: usable}
	isLowConfidence := isLowConfidence(in.Analysis, len(usable), operating.Index)
	runCaveats := point.Caveats
	for _, container := range containersOf(in.RunSpec, in.Pre, in.Post) {
		use, err := measurer.measure(container)
		if err != nil {
			return nil, nil, err
		}
		usage = append(usage, ContainerUsage{
			Target: container.target, Pods: container.pods, CPUP95: use.cpuP95, CPUPeak: use.cpuPeak,
			Throttle: use.throttle, ThrottleStage: use.throttleStage, MemoryPeak: use.memoryPeak,
			CPULimitCores: quantityOr(container.limits, "cpu"), MemoryLimitBytes: quantityOr(container.limits, "memory"),
		})
		sizing := containerSizing{ref: container, use: use, settings: settings, stage: operating, stageRPS: *point.Rps, isLowConf: isLowConfidence, runCaveats: runCaveats, graph: graph}
		document.Recommendations = append(document.Recommendations, sizing.size()...)
		if container.target.Role != spec.WorkloadRoleTarget {
			continue
		}
		if fit, ok := fitMemory(in.Queue, use); ok && memoryFit == nil && sizing.memoryLimitNode != "" {
			memoryFit, memoryLimitNode = &fit, sizing.memoryLimitNode
		}
		if plan.target == nil {
			workload := container.target
			workload.Container = nil
			plan.target = &workload
		}
		if use.hasCPU && use.hasMemory {
			plan.keys = append(plan.keys, container.key)
		}
	}
	plan.perReplica, plan.runCaveats, plan.isLowConf, plan.stageIndex = in.Analysis.Capacity.PerReplica, runCaveats, isLowConfidence, operating.Index
	plan.hasSloMax = in.Analysis.Capacity.SloMax != nil
	if perReplica := plan.perReplica; perReplica != nil && perReplica.TotalGpus != nil && perReplica.ReadyReplicas > 0 {
		plan.gpusPerUnit = *perReplica.TotalGpus / perReplica.ReadyReplicas
	}
	document.Recommendations = append(document.Recommendations, plan.add(graph)...)
	if in.Queue == nil {
		return usage, nil, nil
	}
	queue := *in.Queue
	queue.Confidence, queue.Caveats = confidenceOf(isLowConfidence), runCaveats
	queue.Memory, queue.MemoryLimitNode = memoryFit, memoryLimitNode
	if plan.target != nil && plan.target.TargetType == spec.TargetTypeBifrost {
		queue.Target = plan.target // the knobs for C and B exist only on Bifrost so far
	}
	document.Recommendations = append(document.Recommendations, queueing.Add(graph, queue)...)
	return usage, &queue, nil
}

// quantityOr reads a captured Kubernetes quantity ("1", "250m", "1Gi") as
// cores or bytes; 0 when absent or unreadable.
func quantityOr(quantities spec.ResourceQuantities, name string) float64 {
	text, ok := quantities[name]
	if !ok {
		return 0
	}
	quantity, err := resource.ParseQuantity(text)
	if err != nil {
		return 0
	}
	return quantity.AsApproximateFloat64()
}

// fitMemory relates a router container's working set to the requests in
// flight per replica across the usable stages.
func fitMemory(queue *queueing.Input, use usage) (queueing.MemoryFit, bool) {
	if queue == nil || use.pods == 0 {
		return queueing.MemoryFit{}, false
	}
	var points []queueing.MemoryPoint
	// In stage order: map order would change the fit's last digits run to run.
	for _, stage := range slices.Sorted(maps.Keys(use.stageMemory)) {
		if inFlight, ok := queue.InFlightByStage[stage]; ok {
			points = append(points, queueing.MemoryPoint{InFlight: inFlight / float64(use.pods), WorkingSetBytes: use.stageMemory[stage]})
		}
	}
	return queueing.FitMemory(points)
}

func confidenceOf(isLow bool) spec.RecommendationConfidence {
	if isLow {
		return spec.RecommendationConfidenceLow
	}
	return spec.RecommendationConfidenceMedium
}

// patchHeader names where the patch came from, so an applied patch can be
// traced back to its run.
func patchHeader(in Input, point spec.OperatingPoint) string {
	at := "no operating point: nothing is sized"
	if point.Mode != spec.OperatingPointModeUnavailable {
		at = fmt.Sprintf("operating point %s, stage %d at %s RPS", point.Mode, *point.StageIndex, derive.FormatNumber(*point.Rps))
	}
	return fmt.Sprintf("# servtuner resources patch: run %s, analyzer %s, %s.\n"+
		"# Requests and limits are per replica and hold at any traffic level; replicas and the HPA max\n"+
		"# appear only with analysis.target_rps. Each value's derivation is in recommendations.json.\n",
		in.RunID, in.AnalyzerVersion, at)
}

// stagesOf returns the operating-point stage and every usable stage (valid and
// not excluded), which the leak and throttle checks look across.
func stagesOf(in Input, operatingIndex int) (operating input.Stage, usable []input.Stage, err error) {
	found := false
	for i, stage := range in.Stages {
		if stage.Index == operatingIndex {
			operating, found = stage, true
		}
		quality := in.Analysis.Quality.Stages[i]
		if quality.IsValid && !quality.IsExcluded {
			usable = append(usable, stage)
		}
	}
	if !found {
		return input.Stage{}, nil, fmt.Errorf("operating-point stage %d is not in phases.json", operatingIndex)
	}
	return operating, usable, nil
}

// isLowConfidence applies the low-confidence conditions that hold for the
// whole run: one usable stage, a low-resolution operating stage, or no
// kubernetes catalog.
func isLowConfidence(analysis spec.AnalysisResult, usableStages, operatingIndex int) bool {
	if usableStages < minimumStagesForMediumConfidence {
		return true
	}
	for _, stage := range analysis.Quality.Stages {
		if stage.StageIndex != operatingIndex {
			continue
		}
		for _, issue := range stage.Issues {
			if issue.Code == spec.QualityIssueCodeLowResolution {
				return true
			}
		}
	}
	if analysis.Quality.Coverage == nil {
		return true // no prometheus/queries.json: nothing says the series are complete
	}
	for _, catalog := range analysis.Quality.Coverage.Catalogs {
		if catalog.Catalog == spec.CatalogNameKubernetes && !catalog.IsCollected {
			return true
		}
	}
	return false
}

// operatingPointCaveats warns when the SLO-max stage is only the highest stage
// tested: no stage failed the SLOs, so the replica may serve more, and sizes
// taken there fit that load, not the replica's capacity.
func operatingPointCaveats(analysis spec.AnalysisResult, point spec.OperatingPoint) []string {
	sloMax := analysis.Capacity.SloMax
	if point.Mode != spec.OperatingPointModeSloMax || sloMax == nil || sloMax.FailedStageIndex != nil {
		return nil
	}
	return []string{fmt.Sprintf("No stage failed the SLOs, so %s RPS is the highest load tested, not the replica's limit: these sizes fit that load. Extend the ladder until a stage fails to size for full capacity.", derive.FormatNumber(*point.Rps))}
}

func policyOf(resolved spec.Analysis) spec.SizingPolicy {
	return spec.SizingPolicy{
		CpuLimit: resolved.Limits.Cpu, MemoryLimit: resolved.Limits.Memory, Qos: spec.SizingPolicyQos(resolved.Limits.Qos),
		Headroom: spec.SizingPolicyHeadroom{
			Request: resolved.Headroom.Request, Limit: resolved.Headroom.Limit,
			Replicas: resolved.Headroom.Replicas, Gpu: resolved.Headroom.Gpu,
		},
	}
}
