package queueing

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var start = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// poissonRows sends Poisson arrivals at rate for the stage, each lasting a
// duration drawn by duration().
func poissonRows(stage input.Stage, rate float64, duration func(*rand.Rand) float64) []engine.RequestRow {
	r := rand.New(rand.NewPCG(7, 11))
	var rows []engine.RequestRow
	for at := stage.Start; at.Before(stage.End); {
		at = at.Add(time.Duration(r.ExpFloat64() / rate * float64(time.Second)))
		if !at.Before(stage.End) {
			break
		}
		rows = append(rows, engine.RequestRow{SentAt: at, E2ESeconds: duration(r)})
	}
	return rows
}

func TestPoissonArrivalsSatisfyLittlesLaw(t *testing.T) {
	stage := input.Stage{Index: 1, Start: start, End: start.Add(10 * time.Minute)}
	rows := poissonRows(stage, 20, func(r *rand.Rand) float64 { return 0.5 + r.Float64() }) // mean 1s
	stats, ok := Measure(stage, rows)
	if !ok {
		t.Fatal("no statistics")
	}
	if math.Abs(stats.ArrivalRate-20) > 1 || math.Abs(stats.MeanSeconds-1) > 0.02 {
		t.Errorf("λ = %v, mean W = %v", stats.ArrivalRate, stats.MeanSeconds)
	}
	if !stats.IsSteady() {
		t.Errorf("Little's law fails on a steady Poisson stage: error %.3f (mean L %.2f)", stats.LittleError, stats.InFlightMean)
	}
	// M/G/∞: L ~ Poisson(λW = 20), whose p99 is 31.
	if stats.InFlightP99 < 28 || stats.InFlightP99 > 34 {
		t.Errorf("p99 in flight = %v, want about 31", stats.InFlightP99)
	}
	if stats.ArrivalsP99 < 28 || stats.ArrivalsP99 > 35 {
		t.Errorf("p99 arrivals per second = %v, want about 31", stats.ArrivalsP99)
	}
	if stats.CoefficientOfVariation < 0.25 || stats.CoefficientOfVariation > 0.33 {
		t.Errorf("cv = %v, want about 0.29 (uniform on [0.5, 1.5])", stats.CoefficientOfVariation)
	}
}

// Little's law is a time average, so a slow ramp still satisfies it. What it
// catches is a stage whose work spills past its end: here every request is
// sent in the last 10s and runs a minute, so most of it is never inside the
// stage and λ·W says nothing about the in-flight count.
func TestAStageThatSpillsPastItsEndFailsLittlesLaw(t *testing.T) {
	stage := input.Stage{Index: 0, Start: start, End: start.Add(5 * time.Minute)}
	var rows []engine.RequestRow
	for i := range 1000 {
		at := stage.End.Add(-10 * time.Second).Add(time.Duration(i) * 10 * time.Millisecond)
		rows = append(rows, engine.RequestRow{SentAt: at, E2ESeconds: 60})
	}
	stats, ok := Measure(stage, rows)
	if !ok || stats.IsSteady() {
		t.Errorf("a spilling stage passes Little's law: %+v", stats)
	}
	if Add(derive.NewGraph(), Input{Operating: stats}) != nil {
		t.Error("an unsteady stage must not be sized")
	}
}

func TestNoCompletedRequestsMeansNoStatistics(t *testing.T) {
	stage := input.Stage{Start: start, End: start.Add(time.Minute)}
	if _, ok := Measure(stage, []engine.RequestRow{{SentAt: start, E2ESeconds: 0}}); ok {
		t.Error("statistics from no completed request")
	}
}

// workedExample is the queueing model's worked example: 20 RPS, mean E2E 8s, p99 12s,
// p99 in flight 185, an E2E SLO of 20s at p99, bursts of 31 per second.
func workedExample() Input {
	target := spec.RecommendationTarget{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeBifrost, Namespace: "llm"}
	return Input{
		Operating: StageStats{
			Stage: input.Stage{Index: 3}, Requests: 12000, ArrivalRate: 20, MeanSeconds: 8, P95Seconds: 11, P99Seconds: 12,
			InFlightMean: 160, InFlightP99: 185, ArrivalsP99: 31,
		},
		SLOSeconds: 20, SLOPercentile: 0.99, SLOWSeconds: 12, BurstFactor: 1, IsPoisson: true,
		Target: &target, CurrentConcurrency: 1000.0, CurrentBufferSize: 5000.0,
		Confidence: spec.RecommendationConfidenceMedium, Caveats: []string{},
	}
}

func TestTheWorkedExample(t *testing.T) {
	graph := derive.NewGraph()
	recommendations := Add(graph, workedExample())
	result, err := graph.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	for node, want := range map[string]float64{
		nodePoisson: 190, nodeConcurrency: 209, nodeConservative: 240, nodeWaitBudget: 8,
		nodeQueueMaximum: 209, nodeQueueMinimum: 5, nodeQueue: 10,
	} {
		if got, _ := result.Value(node); got != want {
			t.Errorf("%s = %v, want %v", node, got, want)
		}
	}
	if rho, _ := result.Value(nodeUtilization); math.Abs(rho-160.0/209) > 1e-9 {
		t.Errorf("ρ = %v", rho)
	}
	if wait, _ := result.Value(nodeWaitProbability); wait <= 0 || wait >= 0.5 {
		t.Errorf("P(wait) = %v at ρ 0.77 with 209 servers", wait)
	}
	if len(recommendations) != 2 || recommendations[0].Subject.Name != "bifrost.concurrency" || recommendations[1].Subject.Name != "bifrost.buffer_size" {
		t.Fatalf("recommendations = %+v", recommendations)
	}
	if recommendations[0].Current != 1000.0 || !strings.Contains(strings.Join(recommendations[0].Caveats, " "), "Poisson-like traffic") {
		t.Errorf("concurrency = %+v", recommendations[0])
	}
	// Bifrost's default queue of 5000 at μ = 26.125/s is about 191s of waiting.
	in := workedExample()
	checks := strings.Join(Checks(result, &in), " ")
	if !strings.Contains(checks, "191.388s of waiting") || !strings.Contains(checks, "past the 8s") {
		t.Errorf("checks = %s", checks)
	}
}

func TestWithoutAnSLOThereIsNoQueueSize(t *testing.T) {
	in := workedExample()
	in.SLOSeconds = 0
	graph := derive.NewGraph()
	recommendations := Add(graph, in)
	if len(recommendations) != 1 || recommendations[0].Subject.Name != "bifrost.concurrency" {
		t.Errorf("recommendations = %+v", recommendations)
	}
}

// λ·p99(W) is rarely whole; C must be, for Erlang-C and for a knob.
func TestConcurrencyIsAWholeNumberEvenWhenTheBoundIsNot(t *testing.T) {
	in := workedExample()
	in.Operating.P99Seconds = 9.37 // λ·p99(W) = 187.4 caps C
	graph := derive.NewGraph()
	Add(graph, in)
	result, err := graph.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := result.Value(nodeConcurrency); c != 188 {
		t.Errorf("C = %v, want ⌈187.4⌉ = 188", c)
	}
}

func TestATargetWithoutTheKnobsGetsNodesButNoRecommendations(t *testing.T) {
	in := workedExample()
	in.Target = nil
	graph := derive.NewGraph()
	if recommendations := Add(graph, in); recommendations != nil {
		t.Errorf("recommendations = %+v", recommendations)
	}
	result, err := graph.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := result.Value(nodeConcurrency); c != 209 {
		t.Errorf("C = %v", c)
	}
}

func TestChecksFlagHighUtilizationAndUnabsorbableBursts(t *testing.T) {
	in := workedExample()
	in.Operating.InFlightP99, in.Operating.ArrivalsP99 = 150, 400 // C below λW; huge bursts
	in.Operating.P99Seconds = 8                                   // C_cons = 160 caps C
	graph := derive.NewGraph()
	Add(graph, in)
	result, err := graph.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	checks := strings.Join(Checks(result, &in), " ")
	for _, want := range []string{"needs more C or more replicas", "cannot absorb its own bursts"} {
		if !strings.Contains(checks, want) {
			t.Errorf("checks lack %q: %s", want, checks)
		}
	}
}

// Evenly paced arrivals and constant service (the mock behind Bifrost on kind)
// fill C without queueing: a high ρ there is explained, not a call for more C.
func TestHighUtilizationWithoutRandomnessIsExplained(t *testing.T) {
	in := workedExample()
	in.IsPoisson, in.Operating.CoefficientOfVariation = false, 0.01
	in.Operating.InFlightP99, in.Operating.P99Seconds = 160, 8.05 // C capped near λW
	graph := derive.NewGraph()
	recommendations := Add(graph, in)
	result, err := graph.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	checks := strings.Join(Checks(result, &in), " ")
	if strings.Contains(checks, "needs more C") || !strings.Contains(checks, "no queue formed") {
		t.Errorf("checks = %s", checks)
	}
	if !strings.Contains(strings.Join(recommendations[0].Caveats, " "), "evenly paced arrivals") {
		t.Errorf("caveats = %v", recommendations[0].Caveats)
	}
}

func TestMemoryPerInFlightRequestIsFittedAcrossStages(t *testing.T) {
	// 100 MB at rest, 1 MB per request in flight.
	fit, ok := FitMemory([]MemoryPoint{{InFlight: 10, WorkingSetBytes: 110e6}, {InFlight: 50, WorkingSetBytes: 150e6}, {InFlight: 100, WorkingSetBytes: 200e6}})
	if !ok || math.Abs(fit.BytesPerInFlight-1e6) > 1 || math.Abs(fit.BaselineBytes-100e6) > 10 || fit.Stages != 3 {
		t.Errorf("fit = %+v", fit)
	}
	for name, points := range map[string][]MemoryPoint{
		"one stage":        {{InFlight: 10, WorkingSetBytes: 1}},
		"no spread":        {{InFlight: 10, WorkingSetBytes: 1}, {InFlight: 10, WorkingSetBytes: 2}},
		"memory that fell": {{InFlight: 10, WorkingSetBytes: 2e6}, {InFlight: 50, WorkingSetBytes: 1e6}},
	} {
		if _, ok := FitMemory(points); ok {
			t.Errorf("%s: fitted", name)
		}
	}
}

// The memory check compares the working set at C + B with the sizing's limit
// node, which lives in the same graph.
func TestTheMemoryCheckComparesCPlusBWithTheLimit(t *testing.T) {
	in := workedExample()
	in.Memory = &MemoryFit{BytesPerInFlight: 4e6, BaselineBytes: 200e6, Stages: 3}
	in.MemoryLimitNode = "target.bifrost.memory.limit"
	graph := derive.NewGraph()
	zero, two := 0.0, 2.0
	graph.AddParameter(limitHeadroom, "", "", 0.3, &derive.Bounds{Min: &zero, Max: &two}, "")
	graph.AddMeasured(in.MemoryLimitNode, "", "bytes", 1e9, nil)
	Add(graph, in)
	result, err := graph.Evaluate()
	if err != nil {
		t.Fatal(err)
	}
	// (200 MB + 4 MB × (209 + 10)) × 1.3 = 1.399 GB, past 1 GB.
	if got, _ := result.Value(nodeMemoryAtLimit); math.Abs(got-1398.8e6) > 1e3 {
		t.Errorf("memory at C + B = %v", got)
	}
	if checks := strings.Join(Checks(result, &in), " "); !strings.Contains(checks, "lower B first") {
		t.Errorf("checks = %s", checks)
	}
}

func TestTheQueueCarriesBMaxAndIsUrgentPastIt(t *testing.T) {
	for _, tc := range []struct {
		current  float64
		isUrgent bool
	}{{5000, true}, {100, false}} {
		in := workedExample()
		in.CurrentBufferSize = tc.current
		graph := derive.NewGraph()
		recommendations := Add(graph, in)
		result, err := graph.Evaluate()
		if err != nil {
			t.Fatal(err)
		}
		queue := recommendations[1]
		AnnotateQueue(result, &queue, &in)
		if queue.Max == nil || *queue.Max != 209 {
			t.Errorf("current %v: max = %v, want B_max 209", tc.current, queue.Max)
		}
		if isUrgent := queue.IsUrgent != nil && *queue.IsUrgent; isUrgent != tc.isUrgent {
			t.Errorf("current %v: urgent = %v", tc.current, isUrgent)
		}
		if tc.isUrgent && !strings.Contains(queue.Evidence[len(queue.Evidence)-1].Text, "queue of 5000 is past B_max 209") {
			t.Errorf("evidence = %+v", queue.Evidence)
		}
		concurrency := recommendations[0]
		AnnotateQueue(result, &concurrency, &in)
		if concurrency.Max != nil || concurrency.IsUrgent != nil {
			t.Error("only the queue recommendation is annotated")
		}
	}
}

func TestASlotHeldUntilTheFirstTokenIsMeasuredOnTTFT(t *testing.T) {
	stage := input.Stage{Index: 0, Start: start, End: start.Add(60 * time.Second)}
	var rows []engine.RequestRow
	for i := range 6000 { // 100 RPS, each 0.8 s end to end with the first token at 0.2 s
		ttft := 0.2
		rows = append(rows, engine.RequestRow{SentAt: start.Add(time.Duration(i) * 10 * time.Millisecond), E2ESeconds: 0.8, TTFTSeconds: &ttft})
	}
	whole, ok := Measure(stage, rows)
	slot, okSlot := MeasureSlots(stage, rows, UntilFirstToken)
	if !ok || !okSlot {
		t.Fatal("both must measure")
	}
	if math.Abs(whole.InFlightMean-80) > 2 || math.Abs(slot.InFlightMean-20) > 2 {
		t.Errorf("in flight: whole %.1f (want ~80 = 100 × 0.8), slot %.1f (want ~20 = 100 × 0.2)", whole.InFlightMean, slot.InFlightMean)
	}
	if math.Abs(slot.MeanSeconds-0.2) > 1e-9 {
		t.Errorf("W is the TTFT: %v", slot.MeanSeconds)
	}
	noTTFT := []engine.RequestRow{{SentAt: start, E2ESeconds: 1}}
	if _, ok := MeasureSlots(stage, noTTFT, UntilFirstToken); ok {
		t.Error("rows without a TTFT give no slot time")
	}
}

func TestDroppedRequestsStillCountAsArrivals(t *testing.T) {
	stage := input.Stage{Index: 0, Start: start, End: start.Add(60 * time.Second)}
	var rows []engine.RequestRow
	for i := range 6000 {
		row := engine.RequestRow{SentAt: start.Add(time.Duration(i) * 10 * time.Millisecond), E2ESeconds: 0.8}
		if i%10 != 0 { // one in ten was dropped: it ended (an error) with no first token
			ttft := 0.2
			row.TTFTSeconds = &ttft
		}
		rows = append(rows, row)
	}
	slot, ok := MeasureSlots(stage, rows, UntilFirstToken)
	if !ok {
		t.Fatal("must measure")
	}
	if math.Abs(slot.ArrivalRate-100) > 1e-9 {
		t.Errorf("λ %v: dropped requests are demand, so λ stays 100", slot.ArrivalRate)
	}
	if !slot.IsSteady() {
		t.Errorf("Little's law holds for the requests that held a slot: error %v", slot.LittleError)
	}
}
