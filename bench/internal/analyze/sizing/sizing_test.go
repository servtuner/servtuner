package sizing

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

const (
	mebibyte  = 1048576 // bytes in one MiB (1024 × 1024)
	namespace = "llm"
	podName   = "router-0"
)

var start = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// fixture is a two-stage run against one target pod, every 15s for 3 minutes
// per stage, with a usage function per series.
type fixture struct {
	targetType   string
	analysis     string // the RunSpec's analysis block, JSON
	requests     spec.ResourceQuantities
	limits       spec.ResourceQuantities
	cpu, memory  func(stage int, offset time.Duration) float64
	stageLength  time.Duration
	throttle     float64
	restartedOOM bool
	// restartedOther restarts the container for a reason other than OOM.
	restartedOther bool
	stageValid     []bool
}

func newFixture() *fixture {
	return &fixture{
		targetType: "openai-compatible",
		analysis:   `{}`,
		requests:   spec.ResourceQuantities{"cpu": "250m", "memory": "64Mi"},
		limits:     spec.ResourceQuantities{"memory": "256Mi"},
		cpu: func(stage int, offset time.Duration) float64 {
			return 0.5*float64(stage+1) + 0.01*offset.Minutes() // rises a little within the stage
		},
		memory:      func(stage int, _ time.Duration) float64 { return float64(100+20*stage) * mebibyte },
		stageLength: 3 * time.Minute,
		stageValid:  []bool{true, true},
	}
}

func (f *fixture) stages() []input.Stage {
	return []input.Stage{
		{Index: 0, OfferedRPS: 20, Start: start, End: start.Add(f.stageLength)},
		{Index: 1, OfferedRPS: 50, Start: start.Add(f.stageLength), End: start.Add(2 * f.stageLength)},
	}
}

func (f *fixture) series(value func(stage int, offset time.Duration) float64) []input.Series {
	var samples []input.Sample
	for _, stage := range f.stages() {
		for offset := time.Duration(0); offset < stage.Duration(); offset += 15 * time.Second {
			samples = append(samples, input.Sample{At: stage.Start.Add(offset), Value: value(stage.Index, offset)})
		}
	}
	labels := map[string]string{"namespace": namespace, "pod": podName, "container": "router"}
	// A pod of another namespace with the same name must never be read.
	stranger := map[string]string{"namespace": "other", "pod": podName, "container": "router"}
	huge := make([]input.Sample, len(samples))
	for i, sample := range samples {
		huge[i] = input.Sample{At: sample.At, Value: 1e12}
	}
	return []input.Series{{Labels: labels, Samples: samples}, {Labels: stranger, Samples: huge}}
}

func (f *fixture) input(t *testing.T) Input {
	t.Helper()
	runSpec, err := spec.ParseRunSpec([]byte(`{
	  "apiVersion": "servtuner.dev/v1", "kind": "RunSpec", "name": "sizing",
	  "target": {"type": "` + f.targetType + `", "endpoint": "http://router/v1", "model": "m", "tls": {"mode": "platform"},
	             "workload": {"namespace": "` + namespace + `", "selector": {"app": "router"}}},
	  "engine": {"type": "loadgen"},
	  "workload": {"streaming": true, "dataset": {"type": "synthetic", "input_tokens": {"mean": 8}, "output_tokens": {"mean": 8}}},
	  "load": {"stages": [{"rate": 20, "duration": "3m"}, {"rate": 50, "duration": "3m"}]},
	  "analysis": ` + f.analysis + `
	}`))
	if err != nil {
		t.Fatal(err)
	}
	analysis := spec.AnalysisResult{AnalyzerVersion: "test", Capacity: spec.CapacityModel{
		SloMax:     &spec.SloMaxRps{Rps: 50, ValidStageIndex: 1},
		PerReplica: &spec.PerReplica{ReadyReplicas: 1, RpsPerReplica: 50},
	}}
	analysis.Quality.Coverage = &spec.Coverage{Catalogs: []spec.CatalogCoverage{{Catalog: spec.CatalogNameKubernetes, IsCollected: true}}}
	for i, rps := range []float64{20, 50} {
		analysis.Stages = append(analysis.Stages, spec.StageAggregate{StageIndex: i, OfferedRps: rps, AchievedRps: rps, Requests: 100,
			SloResults: []spec.SloResult{{Slo: spec.SloNameE2EP95, IsMet: true, Threshold: 2}}})
		analysis.Quality.Stages = append(analysis.Quality.Stages, spec.StageQuality{StageIndex: i, IsValid: f.stageValid[i]})
	}
	container := func(restarts int, reason *string) spec.Container {
		return spec.Container{Name: "router", Image: "router:1", Requests: f.requests, Limits: f.limits, RestartCount: &restarts, LastTerminationReason: reason}
	}
	snapshot := func(restarts int, reason *string) *spec.TargetSnapshot {
		return &spec.TargetSnapshot{Workloads: []spec.Workload_2{{
			Role: spec.WorkloadRoleTarget, TargetType: spec.TargetType(f.targetType),
			Pods: []spec.Pod{{Name: podName, Containers: []spec.Container{container(restarts, reason)}}},
		}}}
	}
	post := snapshot(0, nil)
	if f.restartedOOM {
		reason := oomKilledReason
		post = snapshot(1, &reason)
	}
	if f.restartedOther {
		reason := "Error"
		post = snapshot(1, &reason)
	}
	throttle := f.throttle
	return Input{
		AnalyzerVersion: "test", RunSpec: runSpec, Analysis: analysis, Stages: f.stages(), Interval: 15 * time.Second,
		Pre: snapshot(0, nil), Post: post,
		Matrix: input.MapMatrix{
			cpuCores:      f.series(f.cpu),
			workingSet:    f.series(f.memory),
			throttleRatio: f.series(func(int, time.Duration) float64 { return throttle }),
		},
	}
}

func recommend(t *testing.T, f *fixture) spec.Recommendations {
	t.Helper()
	return recommendAll(t, f).Recommendations
}

// recommendAll runs the sizing and checks every output against its schema.
func recommendAll(t *testing.T, f *fixture) Outputs {
	t.Helper()
	outputs, err := Recommend(f.input(t))
	if err != nil {
		t.Fatal(err)
	}
	capacity, err := json.Marshal(outputs.Capacity)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaCapacity, capacity); err != nil {
		t.Fatalf("capacity.json is invalid: %v\n%s", err, capacity)
	}
	document := outputs.Recommendations
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaRecommendations, raw); err != nil {
		t.Fatalf("recommendations.json is invalid: %v\n%s", err, raw)
	}
	checkRenderingsReevaluate(t, document)
	return outputs
}

// Every derived node's rendering is a formula with its own value.
func checkRenderingsReevaluate(t *testing.T, document spec.Recommendations) {
	t.Helper()
	for _, node := range document.Derivation.Nodes {
		if node.Kind != spec.DerivationNodeKindDerived {
			continue
		}
		formula, _, _ := strings.Cut(*node.Rendered, " = ")
		expr, err := derive.Parse(formula)
		if err != nil {
			t.Fatalf("%s: rendering %q does not parse: %v", node.Id, *node.Rendered, err)
		}
		value, err := derive.Eval(expr, func(string) (float64, bool) { return 0, false })
		if err != nil || math.Abs(value-node.Value) > 1e-4*math.Max(1, math.Abs(node.Value)) {
			t.Errorf("%s: rendering %q gives %v, %v; the node says %v", node.Id, *node.Rendered, value, err, node.Value)
		}
	}
}

func find(t *testing.T, document spec.Recommendations, subject string) spec.Recommendation {
	t.Helper()
	for _, recommendation := range document.Recommendations {
		if recommendation.Subject.Name == subject {
			return recommendation
		}
	}
	t.Fatalf("no %s recommendation in %+v", subject, document.Recommendations)
	return spec.Recommendation{}
}

func has(document spec.Recommendations, subject string) bool {
	for _, recommendation := range document.Recommendations {
		if recommendation.Subject.Name == subject {
			return true
		}
	}
	return false
}

func number(t *testing.T, value spec.RecommendationValue) float64 {
	t.Helper()
	n, ok := value.(float64)
	if !ok {
		t.Fatalf("recommended %v is not a number", value)
	}
	return n
}

func TestSizingAtTheSLOMaxStageFollowsThePolicy(t *testing.T) {
	document := recommend(t, newFixture())
	if document.OperatingPoint.Mode != spec.OperatingPointModeSloMax || *document.OperatingPoint.StageIndex != 1 {
		t.Fatalf("operating point = %+v", document.OperatingPoint)
	}
	// Stage 1 after the first minute (the rate window): CPU 1.01 to 1.0275, so
	// p95 ≈ 1.026, × 1.2 = 1.23, up to 1.25.
	cpu := find(t, document, "cpu.request")
	if got := number(t, cpu.Recommended); got != 1.25 {
		t.Errorf("cpu.request = %v, want 1.25", got)
	}
	if cpu.Current != "250m" || cpu.Confidence != spec.RecommendationConfidenceMedium {
		t.Errorf("cpu.request current %v, confidence %s", cpu.Current, cpu.Confidence)
	}
	if !strings.Contains(strings.Join(cpu.Caveats, " "), "Max observed CPU") {
		t.Errorf("no CPU limit is recommended, so the max observed must be reported: %v", cpu.Caveats)
	}
	if has(document, "cpu.limit") {
		t.Error("cpu: follow recommends no limit when the captured spec has none")
	}
	// Memory 120 MiB flat in stage 1: request 144 MiB, limit 156 MiB.
	if got := number(t, find(t, document, "memory.request").Recommended); got != 144*mebibyte {
		t.Errorf("memory.request = %v MiB", got/mebibyte)
	}
	if got := number(t, find(t, document, "memory.limit").Recommended); got != 156*mebibyte {
		t.Errorf("memory.limit = %v MiB", got/mebibyte)
	}
	if has(document, "GOMEMLIMIT") || has(document, "GOMAXPROCS") {
		t.Error("Go settings for a target that is not Go")
	}
}

func TestACapturedCPULimitIsFollowedAndThrottlingIsEvidence(t *testing.T) {
	f := newFixture()
	f.limits = spec.ResourceQuantities{"cpu": "1", "memory": "256Mi"}
	f.throttle = 0.12
	document := recommend(t, f)
	limit := find(t, document, "cpu.limit")
	// Peak 1.0275 × 1.3 = 1.33575, up to 1.35.
	if got := number(t, limit.Recommended); got != 1.35 {
		t.Errorf("cpu.limit = %v, want 1.35", got)
	}
	if !strings.Contains(limit.Evidence[len(limit.Evidence)-1].Text, "throttled 12.0%") {
		t.Errorf("throttling is not evidence: %+v", limit.Evidence)
	}
}

func TestGuaranteedQoSSetsLimitsEqualToRequests(t *testing.T) {
	f := newFixture()
	f.analysis = `{"limits": {"qos": "guaranteed"}}`
	document := recommend(t, f)
	for _, resource := range []string{"cpu", "memory"} {
		request := number(t, find(t, document, resource+".request").Recommended)
		limit := number(t, find(t, document, resource+".limit").Recommended)
		if request != limit {
			t.Errorf("%s: request %v != limit %v", resource, request, limit)
		}
	}
	if got := number(t, find(t, document, "cpu.request").Recommended); got != 1.35 {
		t.Errorf("guaranteed raises the request to the limit formula: %v", got)
	}
}

func TestALeakAtConstantLoadAllowsForThePodLifetime(t *testing.T) {
	f := newFixture()
	// +1 KiB/s through two 10-minute stages at constant load; over the default
	// 24h lifetime that is 84 MiB, more than the 30% headroom on the peak.
	f.stageLength = 10 * time.Minute
	f.memory = func(stage int, offset time.Duration) float64 {
		return float64(100*mebibyte) + 1024*(float64(stage)*600+offset.Seconds())
	}
	document := recommend(t, f)
	limit := find(t, document, "memory.limit")
	peak := float64(100*mebibyte) + 1024*(600+585)
	if want := math.Ceil((peak+1024*86400)/mebibyte) * mebibyte; number(t, limit.Recommended) != want {
		t.Errorf("memory.limit = %v, want peak + 1 KiB/s × 24h rounded up = %v", number(t, limit.Recommended), want)
	}
	if !strings.Contains(strings.Join(limit.Caveats, " "), "possible leak") {
		t.Errorf("caveats = %v", limit.Caveats)
	}
}

func TestNoLeakWhenTheWorkingSetIsFlat(t *testing.T) {
	document := recommend(t, newFixture())
	for _, node := range document.Derivation.Nodes {
		if strings.HasSuffix(node.Id, "memory.growth") && node.Value != 0 {
			t.Errorf("growth = %v on a flat working set", node.Value)
		}
	}
}

func TestAnOOMKillDuringTheRunLowersConfidenceAndRaisesTheLimit(t *testing.T) {
	f := newFixture()
	f.restartedOOM = true
	document := recommend(t, f)
	limit := find(t, document, "memory.limit")
	if limit.Confidence != spec.RecommendationConfidenceLow {
		t.Errorf("confidence = %s", limit.Confidence)
	}
	// The container died at its 256 MiB limit, so its true peak was above 256 MiB
	// whatever the 120 MiB sampled peak says: the limit is 256 × 1.3, rounded up.
	if got := number(t, limit.Recommended); got != 333*mebibyte {
		t.Errorf("memory.limit = %v MiB, want the killed 256 MiB limit × 1.3 = 333 MiB", got/mebibyte)
	}
	if !strings.Contains(strings.Join(limit.Caveats, " "), "OOM-killed at its 256 MiB limit") {
		t.Errorf("caveats = %v", limit.Caveats)
	}
}

func TestAnOOMKillWithoutALimitAndOtherRestartsUseOneAndAHalfTimesThePeak(t *testing.T) {
	// No recorded limit: nothing to bound by, so 1.5 × the 120 MiB peak.
	f := newFixture()
	f.restartedOOM = true
	f.limits = spec.ResourceQuantities{}
	if got := number(t, find(t, recommend(t, f), "memory.limit").Recommended); got != 180*mebibyte {
		t.Errorf("OOM without a limit: memory.limit = %v MiB, want 180", got/mebibyte)
	}
	// A restart that was not an OOM kill says nothing about the limit.
	f = newFixture()
	f.restartedOther = true
	if got := number(t, find(t, recommend(t, f), "memory.limit").Recommended); got != 180*mebibyte {
		t.Errorf("restart: memory.limit = %v MiB, want 180", got/mebibyte)
	}
}

func TestGoTargetsGetGOMEMLIMITAndAGOMAXPROCSHypothesis(t *testing.T) {
	f := newFixture()
	f.targetType = "bifrost"
	document := recommend(t, f)
	limit := number(t, find(t, document, "memory.limit").Recommended)
	if got := number(t, find(t, document, "GOMEMLIMIT").Recommended); got != math.Floor(limit*0.9) {
		t.Errorf("GOMEMLIMIT = %v, want 90%% of %v", got, limit)
	}
	maxProcs := find(t, document, "GOMAXPROCS")
	if number(t, maxProcs.Recommended) != 2 || !strings.Contains(maxProcs.Caveats[0], "hypothesis") {
		t.Errorf("GOMAXPROCS = %+v", maxProcs)
	}
}

func TestOneUsableStageIsLowConfidence(t *testing.T) {
	f := newFixture()
	f.stageValid = []bool{false, true}
	if got := find(t, recommend(t, f), "cpu.request").Confidence; got != spec.RecommendationConfidenceLow {
		t.Errorf("confidence = %s", got)
	}
}

func TestOperatingPoint(t *testing.T) {
	f := newFixture()
	analysis := f.input(t).Analysis
	target := func(rps float64) *float64 { return &rps }
	if point := OperatingPoint(analysis, target(30)); point.Mode != spec.OperatingPointModeTargetRps || *point.StageIndex != 1 {
		t.Errorf("target 30 maps to the stage at or above it: %+v", point)
	}
	if point := OperatingPoint(analysis, target(20)); *point.StageIndex != 0 {
		t.Errorf("target 20 is stage 0: %+v", point)
	}
	if point := OperatingPoint(analysis, target(80)); point.Mode != spec.OperatingPointModeSloMax || point.Reason == nil {
		t.Errorf("a target above every stage is never extrapolated: %+v", point)
	}
	analysis.Capacity.SloMax = nil
	if point := OperatingPoint(analysis, nil); point.Mode != spec.OperatingPointModeUnavailable {
		t.Errorf("no SLO-max: %+v", point)
	}
}

func TestNoOperatingPointMeansNoRecommendations(t *testing.T) {
	f := newFixture()
	in := f.input(t)
	in.Analysis.Capacity.SloMax = nil
	outputs, err := Recommend(in)
	if err != nil || len(outputs.Recommendations.Recommendations) != 0 || outputs.Recommendations.OperatingPoint.Mode != spec.OperatingPointModeUnavailable {
		t.Errorf("outputs = %+v, %v", outputs, err)
	}
	if len(outputs.Capacity.Rows) != 0 || outputs.Capacity.Reason == nil {
		t.Errorf("capacity without an SLO-max = %+v", outputs.Capacity)
	}
}

// Growth in one stage only is a load change, not a leak: it must hold in every
// usable stage at constant load.
func TestGrowthInOneStageIsNotALeak(t *testing.T) {
	f := newFixture()
	f.stageLength = 10 * time.Minute
	f.memory = func(stage int, offset time.Duration) float64 {
		if stage == 0 {
			return float64(100*mebibyte) + 1024*offset.Seconds()
		}
		return 120 * mebibyte
	}
	for _, node := range recommend(t, f).Derivation.Nodes {
		if strings.HasSuffix(node.Id, "memory.growth") && node.Value != 0 {
			t.Errorf("growth = %v from a single stage", node.Value)
		}
	}
}

// Two and a half minutes per stage cannot tell a leak from the heap settling
// at a new load level (seen on kind with Bifrost): no growth allowance.
func TestShortStagesNeverReportALeak(t *testing.T) {
	f := newFixture()
	f.memory = func(stage int, offset time.Duration) float64 {
		return float64(100*mebibyte) + 1024*(float64(stage)*180+offset.Seconds())
	}
	for _, node := range recommend(t, f).Derivation.Nodes {
		if strings.HasSuffix(node.Id, "memory.growth") && node.Value != 0 {
			t.Errorf("growth = %v from 3-minute stages", node.Value)
		}
	}
}

// A value repeated by a fine query step is one observation (cAdvisor refreshes
// every 15s, the step is 5s).
func TestRepeatedSamplesCountOnce(t *testing.T) {
	var samples []input.Sample
	for i := range 12 {
		samples = append(samples, input.Sample{At: start.Add(time.Duration(i) * 5 * time.Second), Value: float64(i / 3)})
	}
	if _, _, n := fitSlope(samples, start, start.Add(time.Hour)); n != 4 {
		t.Errorf("fitted %d samples, want the 4 distinct values", n)
	}
}

func TestAnUnfailedLadderSaysTheSizesFitTheHighestLoadTested(t *testing.T) {
	if caveats := strings.Join(find(t, recommend(t, newFixture()), "cpu.request").Caveats, " "); !strings.Contains(caveats, "highest load tested") {
		t.Errorf("caveats = %s", caveats)
	}
	f := newFixture()
	in := f.input(t)
	failed := 1
	in.Analysis.Capacity.SloMax.FailedStageIndex, in.Analysis.Capacity.SloMax.ValidStageIndex = &failed, 0
	outputs, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	if caveats := strings.Join(find(t, outputs.Recommendations, "cpu.request").Caveats, " "); strings.Contains(caveats, "highest load tested") {
		t.Errorf("a ladder that found the limit needs no such caveat: %s", caveats)
	}
}

func TestTheCapacityTableScalesThePerReplicaSizing(t *testing.T) {
	outputs := recommendAll(t, newFixture())
	table := outputs.Capacity
	if *table.RpsPerReplica != 50 || *table.ReplicaHeadroom != 0.2 || *table.IsScaleOutVerified {
		t.Fatalf("table = %+v", table)
	}
	// One replica plans for 50 × 0.8 = 40 RPS, so 100 RPS takes 3 replicas.
	replicasAt := map[float64]int{}
	for _, row := range table.Rows {
		replicasAt[row.Rps] = row.Replicas
		if row.CpuRequestCores != float64(row.Replicas)*table.PerReplica.CpuRequestCores {
			t.Errorf("%v RPS: CPU %v is not replicas × %v", row.Rps, row.CpuRequestCores, table.PerReplica.CpuRequestCores)
		}
	}
	for rps, want := range map[float64]int{1: 1, 50: 2, 100: 3, 1000: 25} {
		if got := replicasAt[rps]; got != want {
			t.Errorf("%v RPS: %d replicas, want %d", rps, got, want)
		}
	}
	if table.PerReplica.CpuRequestCores != 1.25 || table.PerReplica.MemoryRequestBytes != 144*mebibyte {
		t.Errorf("per replica = %+v", table.PerReplica)
	}
	if !strings.Contains(strings.Join(table.Caveats, " "), "Linear scale-out is assumed") {
		t.Errorf("caveats = %v", table.Caveats)
	}
	csv := string(CapacityCSV(table))
	if !strings.HasPrefix(csv, "rps,replicas,") || !strings.Contains(csv, "\n100,3,,3.75,") {
		t.Errorf("capacity.csv =\n%s", csv)
	}
}

func TestTargetRPSAddsReplicasAndTheHPAMaximum(t *testing.T) {
	f := newFixture()
	f.analysis = `{"target_rps": 130}`
	outputs := recommendAll(t, f)
	if got := number(t, find(t, outputs.Recommendations, "replicas").Recommended); got != 4 {
		t.Errorf("replicas at 130 RPS = %v, want ⌈130 / 40⌉ = 4", got)
	}
	if got := number(t, find(t, outputs.Recommendations, "max_replicas").Recommended); got != 4 {
		t.Errorf("HPA max = %v", got)
	}
	var target []spec.CapacityRow
	for _, row := range outputs.Capacity.Rows {
		if row.IsTargetRps {
			target = append(target, row)
		}
	}
	if len(target) != 1 || target[0].Rps != 130 || target[0].Replicas != 4 {
		t.Errorf("target row = %+v", target)
	}
}

func TestWithoutTargetRPSThereIsNoReplicaCountOrHPAMaximum(t *testing.T) {
	document := recommend(t, newFixture())
	if has(document, "replicas") || has(document, "max_replicas") {
		t.Error("replicas need traffic, which the run does not know")
	}
	if got := number(t, find(t, document, "min_replicas").Recommended); got != 2 {
		t.Errorf("HPA min = %v", got)
	}
	// CPU p95 1.0264 over the 1.25 request, less 20%: ⌊65.69⌋.
	if got := number(t, find(t, document, "cpu_utilization").Recommended); got != 65 {
		t.Errorf("HPA CPU target = %v%%", got)
	}
}

func TestTheResourcesPatchIsReadyToApply(t *testing.T) {
	f := newFixture()
	f.targetType = "bifrost"
	f.analysis = `{"target_rps": 130}`
	patch := string(recommendAll(t, f).ResourcesPatch)
	for _, want := range []string{
		"# servtuner resources patch: run ", "operating point slo_max, stage 1 at 50 RPS",
		"namespace: llm", "replicas: 4", "name: router", "cpu: 1250m", "memory: 144Mi", "memory: 156Mi",
		"name: GOMEMLIMIT", "value: 140MiB", "minReplicas: 2", "maxReplicas: 4", "targetCPUUtilizationPercent: 65",
	} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch lacks %q:\n%s", want, patch)
		}
	}
}

func TestQuantities(t *testing.T) {
	for cores, want := range map[float64]string{2: "2", 0.3: "300m", 1.25: "1250m"} {
		if got := CPUQuantity(cores); got != want {
			t.Errorf("CPUQuantity(%v) = %s", cores, got)
		}
	}
	for bytes, want := range map[float64]string{2 << 30: "2Gi", 170 << 20: "170Mi", 3072: "3Ki", 1536: "1536"} {
		if got := MemoryQuantity(bytes); got != want {
			t.Errorf("MemoryQuantity(%v) = %s", bytes, got)
		}
	}
}

func TestBytesTextKeepsSmallRatesReadable(t *testing.T) {
	for bytes, want := range map[float64]string{40_000: "39 KiB", 300 << 20: "300 MiB", 3 << 30: "3 GiB"} {
		if got := bytesText(bytes); got != want {
			t.Errorf("bytesText(%v) = %q, want %q", bytes, got, want)
		}
	}
}

// The capacity table names the input it lacks: a host run has an SLO-max but no replica count, and
// saying "no SLO-max" there contradicted its own analysis.
func TestTheCapacityTableSaysWhyItHasNoRows(t *testing.T) {
	for _, c := range []struct {
		plan capacityPlan
		want string
	}{
		{capacityPlan{}, "the run has no SLO-max"},
		{capacityPlan{hasSloMax: true}, "ready replicas are unknown"},
		{capacityPlan{hasSloMax: true, perReplica: &spec.PerReplica{}}, "no container of the target workload was sized"},
	} {
		table, err := c.plan.table(nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		if table.Reason == nil || !strings.Contains(*table.Reason, c.want) {
			t.Errorf("%+v: reason %v, want %q", c.plan, table.Reason, c.want)
		}
	}
}
