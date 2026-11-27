package queueing

import (
	"fmt"
	"math"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// Node ids. The prefix keeps them apart from the sizing's.
const (
	nodeLambda          = "queue.lambda"
	nodeMeanW           = "queue.W_mean"
	nodeP99W            = "queue.W_p99"
	nodeSLOW            = "queue.W_slo_percentile"
	nodeEmpirical       = "queue.C_emp"
	nodePoisson         = "queue.C_pois"
	nodeConservative    = "queue.C_cons"
	nodeSafety          = "queue.safety"
	nodeConcurrency     = "queue.C"
	nodeUtilization     = "queue.rho"
	nodeWaitProbability = "queue.P_wait"
	nodeSLO             = "queue.slo_e2e"
	nodeWaitBudget      = "queue.W_q_max"
	nodeDrainRate       = "queue.mu"
	nodeQueueMaximum    = "queue.B_max"
	nodeArrivalsP99     = "queue.arrivals_p99"
	nodeBurstFactor     = "queue.burst_factor"
	nodeQueueMinimum    = "queue.B_min"
	nodeQueue           = NodeQueue
	nodeBytesPerRequest = "queue.m"
	nodeBaselineMemory  = "queue.WS0"
	nodeMemoryAtLimit   = "queue.memory_at_C_B"
	nodeMemoryAtMaximum = "queue.memory_at_C_B_max"
	mebibyte            = 1048576 // bytes in one MiB (1024 × 1024)
	// limitHeadroom is the sizing's h_lim parameter, shared in the graph.
	limitHeadroom = "h_lim"
)

// Queueing model defaults.
const (
	defaultSafety      = 1.1
	utilizationCeiling = 0.8
	// variableService: below this coefficient of variation, service time is
	// near-constant, and with evenly paced arrivals nothing queues even at a
	// high ρ.
	variableService = 0.5
)

// Rule ids.
const (
	ruleConcurrency = "queueing.concurrency"
	ruleQueue       = "queueing.queue"
)

// Input is the operating-point stage's queueing statistics and what the RunSpec
// says about the SLO and bursts.
type Input struct {
	Operating StageStats
	// SLOSeconds and SLOPercentile are the E2E SLO (e2e_p95 → 0.95); zero
	// SLOSeconds when the RunSpec sets none.
	SLOSeconds    float64
	SLOPercentile float64
	SLOWSeconds   float64 // W at the SLO's percentile
	// SLOName names the SLO the queue's wait budget comes from ("E2E", or
	// "TTFT" when a slot is held until the first token).
	SLOName string
	// SlotIsFirstToken: a slot is held until the first token (streaming
	// through Bifrost), so W is the TTFT, not the whole request.
	SlotIsFirstToken bool
	BurstFactor      float64
	// IsPoisson: the run's arrivals were Poisson, so sizes are for
	// Poisson-like traffic.
	IsPoisson bool
	// Target is the router the C and B are for; nil when the target has no
	// knob for them (only bifrost so far).
	Target *spec.RecommendationTarget
	// Current knob values captured from the target, when known.
	CurrentConcurrency, CurrentBufferSize spec.RecommendationValue
	Confidence                            spec.RecommendationConfidence
	Caveats                               []string
	// InFlightByStage is each usable, steady stage's mean in flight (all
	// replicas), for the memory fit.
	InFlightByStage map[int]float64
	// RequestBytesMean is the mean request body over those stages, the s of
	// m(s); 0 when the engine reported no sizes.
	RequestBytesMean float64
	// Memory is the router's bytes per in-flight request, and MemoryLimitNode
	// the derivation node of its recommended memory limit; the memory check
	// runs only with both.
	Memory          *MemoryFit
	MemoryLimitNode string
}

// Add puts the queueing nodes into the graph and returns the C and B
// recommendations (only for targets that have the knobs). Nothing is added
// when the operating stage fails Little's law: it is not in steady state.
func Add(graph *derive.Graph, in Input) []spec.Recommendation {
	stage := in.Operating
	if !stage.IsSteady() {
		return nil
	}
	source := &derive.Source{StageIndex: &stage.Stage.Index, File: "requests.parquet"}
	graph.AddMeasured(nodeLambda, "Arrival rate λ", "requests/s", stage.ArrivalRate, source)
	held := "the whole E2E"
	if in.SlotIsFirstToken {
		held = "until the first token, TTFT"
	}
	graph.AddMeasured(nodeMeanW, fmt.Sprintf("Mean time a request holds a slot W (%s)", held), "s", stage.MeanSeconds, source)
	graph.AddMeasured(nodeP99W, "p99 time a request holds a slot", "s", stage.P99Seconds, source)
	graph.AddMeasured(nodeEmpirical, "p99 of in-flight L(t)", "requests", stage.InFlightP99, source)
	graph.AddMeasured(nodeArrivalsP99, "p99 arrivals in one second", "requests", stage.ArrivalsP99, source)
	one, two, ten := 1.0, 2.0, 10.0
	graph.AddParameter(nodeSafety, "Safety factor on C", "", defaultSafety, &derive.Bounds{Min: &one, Max: &two}, "queueing model")
	graph.AddParameter(nodeBurstFactor, "Burst factor on B_min", "", in.BurstFactor, &derive.Bounds{Min: &one, Max: &ten}, "queueing model assumption; RunSpec analysis.burst_factor")
	graph.AddDerived(nodePoisson, "Poisson p99 in flight", "requests", fmt.Sprintf("poisson_q(0.99, %s * %s)", nodeLambda, nodeMeanW))
	// Counts of requests are whole: the bound is rounded up, and C is at least 1.
	graph.AddDerived(nodeConservative, "Conservative bound ⌈λ·p99(W)⌉", "requests", fmt.Sprintf("ceil(%s * %s)", nodeLambda, nodeP99W))
	graph.AddDerived(nodeConcurrency, "Recommended concurrency C", "requests",
		fmt.Sprintf("max(1, clamp(ceil(max(%s, %s) * %s), %s, max(%s, %s)))", nodeEmpirical, nodePoisson, nodeSafety, nodeEmpirical, nodeEmpirical, nodeConservative))
	graph.AddDerived(nodeUtilization, "Utilization ρ = λ·mean(W) / C", "", fmt.Sprintf("%s * %s / %s", nodeLambda, nodeMeanW, nodeConcurrency))
	graph.AddDerived(nodeWaitProbability, "P(wait), Erlang-C (approximate)", "", fmt.Sprintf("erlang_c(%s, %s * %s)", nodeConcurrency, nodeLambda, nodeMeanW))
	graph.AddDerived(nodeDrainRate, "Drain rate μ = C / mean(W)", "requests/s", fmt.Sprintf("%s / %s", nodeConcurrency, nodeMeanW))
	graph.AddDerived(nodeQueueMinimum, "B_min: p99 one-second burst over μ", "requests",
		fmt.Sprintf("ceil(%s * max(0, %s - %s))", nodeBurstFactor, nodeArrivalsP99, nodeDrainRate))
	hasSLO := in.SLOSeconds > 0
	if hasSLO {
		percentile := fmt.Sprintf("p%g", 100*in.SLOPercentile)
		name := in.SLOName
		if name == "" {
			name = "E2E"
		}
		graph.AddParameter(nodeSLO, fmt.Sprintf("%s SLO (%s)", name, percentile), "s", in.SLOSeconds, &derive.Bounds{Min: new(float64)}, "RunSpec slos")
		graph.AddMeasured(nodeSLOW, fmt.Sprintf("%s %s", percentile, name), "s", in.SLOWSeconds, source)
		graph.AddDerived(nodeWaitBudget, "Wait budget W_q_max = SLO − W", "s", fmt.Sprintf("max(0, %s - %s)", nodeSLO, nodeSLOW))
		graph.AddDerived(nodeQueueMaximum, "B_max = μ × W_q_max", "requests", fmt.Sprintf("floor(%s * %s)", nodeDrainRate, nodeWaitBudget))
		graph.AddDerived(nodeQueue, "Recommended queue size B", "requests",
			fmt.Sprintf("max(%s, min(%s, 2 * %s))", nodeQueueMinimum, nodeQueueMaximum, nodeQueueMinimum))
	}
	addMemoryCheck(graph, in, hasSLO, source)
	if in.Target == nil {
		return nil
	}
	evidence := []spec.RecommendationEvidence{{
		Text:       fmt.Sprintf("Little's law holds within %.1f%%: mean in flight %.1f = λ %.2f × mean W %.3fs", 100*stage.LittleError, stage.InFlightMean, stage.ArrivalRate, stage.MeanSeconds),
		StageIndex: &stage.Stage.Index,
	}}
	caveats := append([]string{}, in.Caveats...)
	if in.SlotIsFirstToken {
		caveats = append(caveats, "Streaming through Bifrost: a provider slot is held until the upstream sends its first token, not for the whole response, so C is sized from TTFT (measured on kind: 32 slots completed ~160 RPS at TTFT 0.2 s).")
	}
	if in.IsPoisson {
		caveats = append(caveats, "Sized from Poisson arrivals: for Poisson-like traffic. Burstier production traffic needs a burst stage or analysis.burst_factor.")
	} else {
		caveats = append(caveats, "Sized from evenly paced arrivals: C and B fit traffic at least this smooth. Real traffic arrives at random and queues more; size from a Poisson stage (load.pacing: poisson).")
	}
	recommendations := []spec.Recommendation{
		recommendation(in, ruleConcurrency, target.KnobBifrostConcurrency, in.CurrentConcurrency, nodeConcurrency, evidence,
			append(append([]string{}, caveats...), fmt.Sprintf("Utilization ρ must stay at or below %.1f; above it, tail waiting grows sharply.", utilizationCeiling))),
	}
	if hasSLO {
		recommendations = append(recommendations, recommendation(in, ruleQueue, target.KnobBifrostBufferSize, in.CurrentBufferSize, nodeQueue, evidence,
			append(append([]string{}, caveats...), "A request past position B_max cannot meet the SLO: a larger queue only turns fast failures into slow ones and holds memory. When B_max is small against the bursts, reject when full (drop_excess_requests) with client retries.")))
	}
	return recommendations
}

// addMemoryCheck adds the working set the recommended C and B could reach,
// with the limit's headroom: WS0 + m × (C + B), times 1 + h_lim.
func addMemoryCheck(graph *derive.Graph, in Input, hasSLO bool, source *derive.Source) {
	if in.Memory == nil || in.MemoryLimitNode == "" {
		return
	}
	graph.AddMeasured(nodeBytesPerRequest, fmt.Sprintf("Bytes per in-flight request, fitted over %d stages", in.Memory.Stages), "bytes", in.Memory.BytesPerInFlight, source)
	graph.AddMeasured(nodeBaselineMemory, "Working set with nothing in flight", "bytes", in.Memory.BaselineBytes, source)
	held := nodeConcurrency
	if hasSLO {
		held = fmt.Sprintf("(%s + %s)", nodeConcurrency, nodeQueue)
	}
	graph.AddDerived(nodeMemoryAtLimit, "Working set at C + B, with the limit's headroom", "bytes",
		fmt.Sprintf("(%s + %s * %s) * (1 + %s)", nodeBaselineMemory, nodeBytesPerRequest, held, limitHeadroom))
	if hasSLO {
		// The other end of the buffer trade-off: a queue as deep as the SLO
		// allows, for bursts, and the memory it would hold.
		graph.AddDerived(nodeMemoryAtMaximum, "Working set at C + B_max, with the limit's headroom", "bytes",
			fmt.Sprintf("(%s + %s * (%s + %s)) * (1 + %s)", nodeBaselineMemory, nodeBytesPerRequest, nodeConcurrency, nodeQueueMaximum, limitHeadroom))
	}
}

// BufferTradeOff (added to the queue recommendation by AnnotateQueue) states what keeping a queue as deep as the SLO allows would
// cost in memory, for the buffer recommendation: B_max and the working set at
// C + B_max against the recommended limit. Empty without an SLO or a fitted
// memory line.
func BufferTradeOff(result *derive.Result, in *Input) string {
	if in == nil || in.MemoryLimitNode == "" {
		return ""
	}
	maximum, hasMaximum := result.Value(nodeQueueMaximum)
	needed, hasNeeded := result.Value(nodeMemoryAtMaximum)
	limit, hasLimit := result.Value(in.MemoryLimitNode)
	if !hasMaximum || !hasNeeded || !hasLimit || maximum < 1 {
		return ""
	}
	return fmt.Sprintf("To absorb bursts, any buffer up to B_max %s also meets the SLO; at B_max the working set could reach %s MiB with headroom, "+
		"so raise the memory limit to that (the recommended limit is %s MiB). A buffer past B_max holds only requests that will miss the SLO.",
		derive.FormatNumber(maximum), derive.FormatNumber(math.Ceil(needed/mebibyte)), derive.FormatNumber(math.Ceil(limit/mebibyte)))
}

func recommendation(in Input, rule, knob string, current spec.RecommendationValue, node string, evidence []spec.RecommendationEvidence, caveats []string) spec.Recommendation {
	unit := "requests"
	return spec.Recommendation{
		Id: rule + "/" + string(in.Target.Role), Rule: rule, Target: *in.Target,
		Subject: spec.RecommendationSubject{Kind: spec.RecommendationSubjectKindKnob, Name: knob},
		Unit:    &unit, Current: current, DerivationNode: &node,
		Tier: spec.RecommendationTierDiagnostic, Confidence: in.Confidence, Evidence: evidence, Caveats: caveats,
	}
}

// Checks are what the evaluated graph says about the recommended C and B,
// as caveats for the report: utilization over the ceiling, a burst the SLO
// cannot absorb, and a current queue far past the SLO. The ceiling assumes
// random arrivals or variable service; without either nothing queues even at
// a high ρ, so the check says that instead of asking for more C.
func Checks(result *derive.Result, in *Input) []string {
	var checks []string
	if rho, ok := result.Value(nodeUtilization); ok && rho > utilizationCeiling && in != nil {
		if in.IsPoisson || in.Operating.CoefficientOfVariation >= variableService {
			checks = append(checks, fmt.Sprintf("ρ = %s at the recommended C is above %.1f: the component needs more C or more replicas.", derive.FormatNumber(rho), utilizationCeiling))
		} else {
			checks = append(checks, fmt.Sprintf("ρ = %s is above %.1f, but the run's arrivals were evenly paced and its service near-constant (cv %s), so no queue formed. Random arrivals would queue at this C.",
				derive.FormatNumber(rho), utilizationCeiling, derive.FormatNumber(in.Operating.CoefficientOfVariation)))
		}
	}
	if in != nil && in.MemoryLimitNode != "" {
		needed, hasNeeded := result.Value(nodeMemoryAtLimit)
		limit, hasLimit := result.Value(in.MemoryLimitNode)
		if hasNeeded && hasLimit && needed > limit {
			remedy := "lower C and add replicas, or raise the memory limit"
			if queue, ok := result.Value(nodeQueue); ok && queue > 0 {
				remedy = "lower B first (an oversized queue is the usual cause), then raise memory"
			}
			checks = append(checks, fmt.Sprintf("At the recommended C and B the working set could reach %s bytes with headroom, past the %s-byte memory limit: %s.",
				derive.FormatNumber(needed), derive.FormatNumber(limit), remedy))
		}
	}
	var currentBuffer *float64
	if in != nil {
		if value, ok := in.CurrentBufferSize.(float64); ok {
			currentBuffer = &value
		}
	}
	minimum, hasMinimum := result.Value(nodeQueueMinimum)
	maximum, hasMaximum := result.Value(nodeQueueMaximum)
	if hasMinimum && hasMaximum && minimum > maximum {
		checks = append(checks, fmt.Sprintf("B_min %s exceeds B_max %s: the component cannot absorb its own bursts within the SLO. Add C or replicas, not queue.", derive.FormatNumber(minimum), derive.FormatNumber(maximum)))
	}
	drain, hasDrain := result.Value(nodeDrainRate)
	budget, hasBudget := result.Value(nodeWaitBudget)
	if currentBuffer != nil && hasDrain && hasBudget && drain > 0 && *currentBuffer/drain > budget {
		checks = append(checks, fmt.Sprintf("The current queue of %s at μ = %s/s holds about %ss of waiting, past the %ss the SLO leaves.",
			derive.FormatNumber(*currentBuffer), derive.FormatNumber(drain), derive.FormatNumber(*currentBuffer/drain), derive.FormatNumber(budget)))
	}
	return checks
}

// NodeQueue is the recommended B, which the capacity model carries for the
// planner's memory check.
const NodeQueue = "queue.B"

// AnnotateQueue gives the queue recommendation B_max as its max, and marks it
// urgent when the current queue is past B_max: every request past
// that position fails the SLO while it holds memory.
func AnnotateQueue(result *derive.Result, recommendation *spec.Recommendation, in *Input) {
	if recommendation.DerivationNode == nil || *recommendation.DerivationNode != nodeQueue {
		return
	}
	maximum, ok := result.Value(nodeQueueMaximum)
	if !ok {
		return
	}
	recommendation.Max = &maximum
	if in == nil {
		return
	}
	if tradeOff := BufferTradeOff(result, in); tradeOff != "" {
		recommendation.Caveats = append(recommendation.Caveats, tradeOff)
	}
	if current, isNumber := in.CurrentBufferSize.(float64); isNumber && current > maximum {
		urgent := true
		recommendation.IsUrgent = &urgent
		recommendation.Evidence = append(recommendation.Evidence, spec.RecommendationEvidence{
			Text: fmt.Sprintf("Urgent: the current queue of %s is past B_max %s, the deepest a request can wait and still meet the SLO",
				derive.FormatNumber(current), derive.FormatNumber(maximum)),
		})
	}
}
