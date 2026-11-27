package sizing

import (
	"fmt"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Rounding steps: CPU to 50m, memory to 1 MiB.
const (
	cpuStepCores  = 0.05
	memoryStepMiB = 1048576
)

// Resource sizing thresholds.
const (
	// throttleEvidence: throttling above 5% in any usable stage shows the CPU
	// limit is too low.
	throttleEvidence = 0.05
	// oomFactor: after an OOM kill or restart the limit is at least 1.5 × the peak.
	oomFactor = 1.5
	// goMemoryLimitFraction: GOMEMLIMIT ≈ 90% of the memory limit (Bifrost's docs).
	goMemoryLimitFraction = 0.9
)

// Parameter node ids, shared by every container.
const (
	nodeRequestHeadroom = "h_req"
	nodeLimitHeadroom   = "h_lim"
	nodePodLifetime     = "pod_lifetime_seconds"
)

// Rule ids of the recommendations.
const (
	ruleCPURequest    = "sizing.cpu_request"
	ruleCPULimit      = "sizing.cpu_limit"
	ruleMemoryRequest = "sizing.memory_request"
	ruleMemoryLimit   = "sizing.memory_limit"
	ruleGoMemoryLimit = "sizing.gomemlimit"
	ruleGoMaxProcs    = "sizing.gomaxprocs"
)

// Settings are what the RunSpec's analysis block decides for sizing.
type Settings struct {
	CPULimit, MemoryLimit spec.LimitsPolicy
	IsGuaranteed          bool
	RequestHeadroom       float64
	LimitHeadroom         float64
	PodLifetime           time.Duration
}

// SettingsFrom reads the resolved analysis block (spec.ResolveAnalysis).
func SettingsFrom(analysis spec.Analysis) (Settings, error) {
	lifetime, err := analysis.ExpectedPodLifetime.Std()
	if err != nil {
		return Settings{}, fmt.Errorf("analysis.expected_pod_lifetime: %w", err)
	}
	return Settings{
		CPULimit: analysis.Limits.Cpu, MemoryLimit: analysis.Limits.Memory,
		IsGuaranteed:    analysis.Limits.Qos == spec.AnalysisLimitsQosGuaranteed,
		RequestHeadroom: analysis.Headroom.Request, LimitHeadroom: analysis.Headroom.Limit,
		PodLifetime: lifetime,
	}, nil
}

// addParameters adds the shared tunable inputs once.
func addParameters(graph *derive.Graph, settings Settings) {
	zero, two := 0.0, 2.0
	headroom := &derive.Bounds{Min: &zero, Max: &two}
	graph.AddParameter(nodeRequestHeadroom, "Request headroom over p95 usage", "", settings.RequestHeadroom, headroom, "sizing policy")
	graph.AddParameter(nodeLimitHeadroom, "Limit headroom over the peak", "", settings.LimitHeadroom, headroom, "sizing policy")
	graph.AddParameter(nodePodLifetime, "Expected pod lifetime", "s", settings.PodLifetime.Seconds(), &derive.Bounds{Min: &zero}, "sizing policy: memory")
}

// containerSizing is one container's nodes and recommendations.
type containerSizing struct {
	ref       containerRef
	use       usage
	settings  Settings
	stage     input.Stage
	stageRPS  float64
	isLowConf bool
	// runCaveats apply to every recommendation of the run.
	runCaveats []string
	graph      *derive.Graph
	// memoryLimitNode is set when a memory limit was recommended.
	memoryLimitNode string
	out             []spec.Recommendation
}

func (c *containerSizing) node(name string) string { return c.ref.key + "." + name }

// size adds the container's nodes to the graph and returns its
// recommendations, which point at those nodes. Values are filled in after
// the graph is evaluated (fillValues).
func (c *containerSizing) size() []spec.Recommendation {
	if c.use.hasCPU {
		c.sizeCPU()
	}
	if c.use.hasMemory {
		c.sizeMemory()
	}
	return c.out
}

func (c *containerSizing) sizeCPU() {
	source := c.source("prometheus/kubernetes/cpu_cores.json")
	c.graph.AddMeasured(c.node("cpu.p95"), "CPU p95 at the operating point (busiest pod)", "cores", c.use.cpuP95, source)
	c.graph.AddMeasured(c.node("cpu.peak"), "CPU peak, a 1-minute average (busiest pod)", "cores", c.use.cpuPeak, source)
	request := fmt.Sprintf("%s * (1 + %s)", c.node("cpu.p95"), nodeRequestHeadroom)
	limit := fmt.Sprintf("%s * (1 + %s)", c.node("cpu.peak"), nodeLimitHeadroom)
	if c.settings.IsGuaranteed {
		// QoS guaranteed: limits equal requests, and the request is raised to the
		// limit formula so the pod is never sized below its peak.
		request = fmt.Sprintf("max(%s, %s)", request, limit)
	}
	c.graph.AddDerived(c.node("cpu.request"), "CPU request", "cores", fmt.Sprintf("roundup(%s, %s)", request, derive.FormatNumber(cpuStepCores)))
	evidence := []spec.RecommendationEvidence{c.evidence(fmt.Sprintf("CPU p95 %s cores, peak %s cores (1-minute average) at %s RPS",
		derive.FormatNumber(c.use.cpuP95), derive.FormatNumber(c.use.cpuPeak), derive.FormatNumber(c.stageRPS)), "kubernetes/cpu_cores")}
	hasLimit := c.hasCurrent(c.ref.limits, "cpu")
	setsLimit := c.setsLimit(c.settings.CPULimit, hasLimit)

	requestCaveats := []string{"The CPU peak is a 1-minute average, so shorter bursts are not seen."}
	if !hasLimit {
		requestCaveats = append(requestCaveats,
			"The pod ran without a CPU limit, so it may have borrowed idle node CPU; the numbers may not hold on a busier node.",
			"Without a CPU limit there is no throttling evidence; CPU-bound detection uses usage against the request.")
	}
	if !setsLimit {
		requestCaveats = append(requestCaveats, fmt.Sprintf("Max observed CPU: %s cores, for a limit added later.", derive.FormatNumber(c.use.cpuPeak)))
	}
	c.recommend(ruleCPURequest, "resource", "cpu.request", "cores", c.current(c.ref.requests, "cpu"), c.node("cpu.request"), evidence, requestCaveats)

	if setsLimit {
		formula := fmt.Sprintf("roundup(%s, %s)", limit, derive.FormatNumber(cpuStepCores))
		if c.settings.IsGuaranteed {
			formula = c.node("cpu.request")
		}
		c.graph.AddDerived(c.node("cpu.limit"), "CPU limit", "cores", formula)
		limitEvidence := evidence
		var caveats []string
		if c.use.throttle > throttleEvidence {
			limitEvidence = append(limitEvidence, spec.RecommendationEvidence{
				Text:       fmt.Sprintf("CPU throttled %.1f%% of periods in stage %d: the current limit is too low by evidence", 100*c.use.throttle, c.use.throttleStage),
				StageIndex: intPointer(c.use.throttleStage), Series: stringPointer("kubernetes/cpu_throttle_ratio"),
			})
			caveats = append(caveats, "Raise the limit, or remove the CPU limit: that is a policy choice, not one servtuner makes.")
		}
		if c.isGo() {
			caveats = append(caveats, "GOMAXPROCS follows the CPU limit, so the limit also sets the Go runtime's parallelism.")
		}
		c.recommend(ruleCPULimit, "resource", "cpu.limit", "cores", c.current(c.ref.limits, "cpu"), c.node("cpu.limit"), limitEvidence, caveats)
	} else if c.isGo() {
		// No limit: the Go runtime sizes GOMAXPROCS to the node's cores.
		c.graph.AddDerived(c.node("gomaxprocs"), "GOMAXPROCS", "threads", fmt.Sprintf("max(1, ceil(%s))", c.node("cpu.request")))
		c.recommend(ruleGoMaxProcs, "env", "GOMAXPROCS", "threads", nil, c.node("gomaxprocs"), evidence,
			[]string{"A hypothesis to A/B test: without a CPU limit the Go runtime uses every node core, which can over-parallelise and thrash the GC."})
	}
}

func (c *containerSizing) sizeMemory() {
	source := c.source("prometheus/kubernetes/memory_working_set_bytes.json")
	c.graph.AddMeasured(c.node("memory.p95"), "Working set p95 at the operating point (busiest pod)", "bytes", c.use.memoryP95, source)
	c.graph.AddMeasured(c.node("memory.peak"), "Working set peak (busiest pod)", "bytes", c.use.memoryPeak, source)
	c.graph.AddMeasured(c.node("memory.growth"), "Significant working-set growth at constant load (0 when there is none)", "bytes/s", c.use.leakSlope, nil)
	request := fmt.Sprintf("%s * (1 + %s)", c.node("memory.p95"), nodeRequestHeadroom)
	limitTerms := fmt.Sprintf("%s * (1 + %s), %s + %s * %s", c.node("memory.peak"), nodeLimitHeadroom, c.node("memory.peak"), c.node("memory.growth"), nodePodLifetime)
	if c.ref.wasOOMKilled || c.ref.restarted {
		limitTerms += fmt.Sprintf(", %s * %s", c.node("memory.peak"), derive.FormatNumber(oomFactor))
	}
	killedLimit := c.killedMemoryLimit()
	if killedLimit > 0 {
		// The true peak passed the limit that was killed; the scraped peak, sampled
		// every few seconds, can miss the spike entirely. Never recommend less.
		c.graph.AddMeasured(c.node("memory.killed_limit"), "Memory limit the container was OOM-killed at", "bytes", killedLimit, nil)
		limitTerms += fmt.Sprintf(", %s * (1 + %s)", c.node("memory.killed_limit"), nodeLimitHeadroom)
	}
	limit := fmt.Sprintf("max(%s)", limitTerms)
	if c.settings.IsGuaranteed {
		request = fmt.Sprintf("max(%s, %s)", request, limit)
	}
	c.graph.AddDerived(c.node("memory.request"), "Memory request", "bytes", fmt.Sprintf("roundup(%s, %d)", request, memoryStepMiB))
	evidence := []spec.RecommendationEvidence{c.evidence(fmt.Sprintf("Working set p95 %s, peak %s at %s RPS",
		bytesText(c.use.memoryP95), bytesText(c.use.memoryPeak), derive.FormatNumber(c.stageRPS)), "kubernetes/memory_working_set_bytes")}
	c.recommend(ruleMemoryRequest, "resource", "memory.request", "bytes", c.current(c.ref.requests, "memory"), c.node("memory.request"), evidence, nil)

	if !c.setsLimit(c.settings.MemoryLimit, c.hasCurrent(c.ref.limits, "memory")) {
		return
	}
	formula := fmt.Sprintf("roundup(%s, %d)", limit, memoryStepMiB)
	if c.settings.IsGuaranteed {
		formula = c.node("memory.request")
	}
	c.graph.AddDerived(c.node("memory.limit"), "Memory limit", "bytes", formula)
	c.memoryLimitNode = c.node("memory.limit")
	caveats := []string{"A memory limit is never set below the observed peak."}
	if c.use.leakSlope > 0 {
		caveats = append(caveats, fmt.Sprintf("The working set grew %s/s at constant load in every usable stage: a possible leak. The limit allows for the expected pod lifetime.", bytesText(c.use.leakSlope)))
	}
	switch {
	case killedLimit > 0:
		caveats = append(caveats, fmt.Sprintf("The container was OOM-killed at its %s limit, so its true peak was above it and above the sampled peak: "+
			"the limit is at least the killed limit plus the limit headroom. Repeat the run with it.", bytesText(killedLimit)))
	case c.ref.wasOOMKilled || c.ref.restarted:
		caveats = append(caveats, "The container was OOM-killed or restarted during the run: the limit is at least 1.5 × the peak, and the run should be repeated with it.")
	}
	c.recommend(ruleMemoryLimit, "resource", "memory.limit", "bytes", c.current(c.ref.limits, "memory"), c.node("memory.limit"), evidence, caveats)

	if c.isGo() {
		c.graph.AddDerived(c.node("gomemlimit"), "GOMEMLIMIT", "bytes", fmt.Sprintf("floor(%s * %s)", c.node("memory.limit"), derive.FormatNumber(goMemoryLimitFraction)))
		c.recommend(ruleGoMemoryLimit, "env", "GOMEMLIMIT", "bytes", nil, c.node("gomemlimit"), evidence,
			[]string{"Vendor guidance (Bifrost's docs): GOMEMLIMIT about 90% of the memory limit; confirm with an A/B run."})
	}
}

// setsLimit applies the limits policy: follow recommends a limit only
// when the captured spec has one; QoS guaranteed always sets limits.
func (c *containerSizing) setsLimit(policy spec.LimitsPolicy, hasLimit bool) bool {
	switch {
	case c.settings.IsGuaranteed:
		return true
	case policy == spec.LimitsPolicySet:
		return true
	case policy == spec.LimitsPolicyFollow:
		return hasLimit
	default:
		return false
	}
}

// isGo: Bifrost is a Go target, whose runtime reads GOMAXPROCS and GOMEMLIMIT.
func (c *containerSizing) isGo() bool {
	return c.ref.target.TargetType == spec.TargetTypeBifrost
}

func (c *containerSizing) hasCurrent(quantities spec.ResourceQuantities, name string) bool {
	_, ok := quantities[name]
	return ok
}

// current is the captured quantity as the spec wrote it, or nil when unset.
func (c *containerSizing) current(quantities spec.ResourceQuantities, name string) spec.RecommendationValue {
	if value, ok := quantities[name]; ok {
		return value
	}
	return nil
}

func (c *containerSizing) source(file string) *derive.Source {
	return &derive.Source{StageIndex: intPointer(c.stage.Index), File: file}
}

func (c *containerSizing) evidence(text, series string) spec.RecommendationEvidence {
	return spec.RecommendationEvidence{Text: text, StageIndex: intPointer(c.stage.Index), Series: &series}
}

func (c *containerSizing) recommend(rule, kind, name, unit string, current spec.RecommendationValue, node string, evidence []spec.RecommendationEvidence, caveats []string) {
	confidence := spec.RecommendationConfidenceMedium
	if c.isLowConf || c.ref.wasOOMKilled || c.ref.restarted {
		confidence = spec.RecommendationConfidenceLow
	}
	caveats = append(append([]string{}, caveats...), c.runCaveats...)
	c.out = append(c.out, spec.Recommendation{
		Id: rule + "/" + c.ref.key, Rule: rule, Target: c.ref.target,
		Subject: spec.RecommendationSubject{Kind: spec.RecommendationSubjectKind(kind), Name: name},
		Unit:    &unit, Current: current, DerivationNode: &node,
		Tier: spec.RecommendationTierDiagnostic, Confidence: confidence,
		Evidence: evidence, Caveats: caveats,
	})
}

// bytesText writes a byte count in binary units for evidence sentences.
func bytesText(bytes float64) string {
	const (
		kibibyte = 1024       // bytes in one KiB
		mebibyte = 1048576    // bytes in one MiB (1024 × 1024)
		gibibyte = 1073741824 // bytes in one GiB (1024 × 1024 × 1024)
	)
	if bytes >= gibibyte {
		return derive.FormatNumber(float64(int(bytes/gibibyte*100))/100) + " GiB"
	}
	if bytes < mebibyte {
		return derive.FormatNumber(float64(int(bytes/kibibyte*10))/10) + " KiB"
	}
	return derive.FormatNumber(float64(int(bytes/mebibyte*10))/10) + " MiB"
}

func intPointer(value int) *int          { return &value }
func stringPointer(value string) *string { return &value }

// killedMemoryLimit is the memory limit in bytes of a container that was
// OOM-killed during the run, or 0 when it was not killed or had no limit.
func (c *containerSizing) killedMemoryLimit() float64 {
	if !c.ref.wasOOMKilled {
		return 0
	}
	return quantityOr(c.ref.limits, "memory")
}
