package compare

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

const version = "test"

// fixture describes one synthetic analysed run.
type fixture struct {
	id        string
	knobs     spec.RunSpecKnobs
	sloMax    float64 // 0: no SLO-max
	cores     float64 // sized CPU request per replica
	ttftScale float64 // TTFTs are ttftScale * (1 + i/n) for i < n
	workload  string
	excluded  bool
	offered   []float64
	targetRPS float64
	gpus      int
	// neverFailed: every stage met the SLOs, so SLO-max is a lower bound.
	neverFailed bool
	// targetType defaults to bifrost; model and hardware to none.
	targetType spec.TargetType
	model      string
	hardware   string
}

func (f fixture) run() Run {
	offered := f.offered
	if offered == nil {
		offered = []float64{50, 100}
	}
	analysis := spec.AnalysisResult{
		AnalyzerVersion: version,
		Quality:         spec.RunQuality{Grade: spec.QualityGradeGood, IsExcludedFromComparison: f.excluded},
	}
	for i, rps := range offered {
		output := rps * 10
		analysis.Stages = append(analysis.Stages, spec.StageAggregate{StageIndex: i, OfferedRps: rps, AchievedRps: rps, Requests: 2000, OutputTokensPerSecond: &output})
		analysis.Quality.Stages = append(analysis.Quality.Stages, spec.StageQuality{StageIndex: i, IsValid: true})
	}
	if f.sloMax > 0 {
		analysis.Capacity.SloMax = &spec.SloMaxRps{Rps: f.sloMax, ValidStageIndex: len(offered) - 2, IsInterpolated: true}
		if f.neverFailed {
			analysis.Capacity.SloMax = &spec.SloMaxRps{Rps: f.sloMax, ValidStageIndex: len(offered) - 1}
		} else {
			failed := len(offered) - 1
			analysis.Capacity.SloMax.FailedStageIndex = &failed
		}
	}
	perReplica := &spec.PerReplica{ReadyReplicas: 1, RpsPerReplica: f.sloMax}
	if f.gpus > 0 {
		perReplica.TotalGpus = &f.gpus
	}
	analysis.Capacity.PerReplica = perReplica
	cores := f.cores
	if cores == 0 {
		cores = 1
	}
	workload := f.workload
	if workload == "" {
		workload = "sha256:w"
	}
	experiment := "exp"
	targetType := f.targetType
	if targetType == "" {
		targetType = spec.TargetTypeBifrost
	}
	if f.hardware != "" {
		analysis.HardwareProfileId = &f.hardware
	}
	runSpec := spec.RunSpec{Experiment: &experiment, Knobs: f.knobs, Target: spec.Target{Type: targetType, Model: f.model}, Load: spec.Load{Pacing: spec.PacingPoisson}}
	if f.targetRPS > 0 {
		runSpec.Analysis = &spec.Analysis{TargetRps: &f.targetRPS}
	}
	scale := f.ttftScale
	if scale == 0 {
		scale = 0.1
	}
	return Run{
		ID: f.id, Spec: runSpec, Analysis: analysis,
		Model: &spec.RunCapacityModel{Identity: spec.ModelIdentity{WorkloadHash: workload}, PerReplica: &spec.ModelPerReplica{CpuRequestCores: cores}},
		TTFT: func(int) ([]float64, error) {
			values := make([]float64, 2000)
			for i := range values {
				values[i] = scale * (1 + float64(i)/float64(len(values)))
			}
			return values, nil
		},
	}
}

func knobs(concurrency int) spec.RunSpecKnobs {
	return spec.RunSpecKnobs{"bifrost.concurrency": float64(concurrency), "fixed": true}
}

func runs(fixtures ...fixture) []Run {
	result := make([]Run, len(fixtures))
	for i, f := range fixtures {
		result[i] = f.run()
	}
	return result
}

func mustCompare(t *testing.T, input []Run, objective spec.ObjectiveName) spec.Comparison {
	t.Helper()
	result, err := Compare(input, objective, version)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaComparison, encoded); err != nil {
		t.Fatalf("comparison.json breaks its schema: %v\n%s", err, encoded)
	}
	return result
}

func repeats(prefix string, concurrency int, values ...float64) []fixture {
	var fixtures []fixture
	for i, v := range values {
		fixtures = append(fixtures, fixture{id: fmt.Sprintf("%s%d", prefix, i), knobs: knobs(concurrency), sloMax: v})
	}
	return fixtures
}

func TestSeparatedRepeatsWinWithHighConfidence(t *testing.T) {
	input := runs(append(repeats("A", 100, 200, 210, 205), repeats("B", 400, 320, 330, 325)...)...)
	result := mustCompare(t, input, "")
	if result.Objective != spec.ObjectiveNameSloRpsPerCore {
		t.Errorf("default objective for bifrost = %s, want slo_rps_per_core", result.Objective)
	}
	if !reflect.DeepEqual(result.VariedKnobs, []string{"bifrost.concurrency"}) || !result.IsFactorial {
		t.Errorf("varied %v factorial %v", result.VariedKnobs, result.IsFactorial)
	}
	if len(result.Configurations) != 2 || result.Configurations[0].Id != "c1" || len(result.Configurations[0].RunIds) != 3 {
		t.Fatalf("configurations: %+v", result.Configurations)
	}
	primary := result.Rankings[0]
	if primary.Objective != result.Objective || primary.WinnerId == nil || *primary.WinnerId != "c2" || primary.Confidence != spec.ComparisonConfidenceHigh {
		t.Fatalf("primary ranking: %+v", primary)
	}
	recommendation := result.Recommendation
	if recommendation == nil || recommendation.ConfigurationId != "c2" || recommendation.Confidence != spec.ComparisonConfidenceHigh {
		t.Fatalf("recommendation: %+v", recommendation)
	}
	if !reflect.DeepEqual(map[string]spec.KnobValue(recommendation.Knobs), map[string]spec.KnobValue{"bifrost.concurrency": float64(400)}) {
		t.Errorf("recommended knobs %v: only the varied knob belongs there", recommendation.Knobs)
	}
	estimate, _ := objectiveOf(result.Configurations[0], spec.ObjectiveNameSloMaxRps)
	if *estimate.Value != 205 || *estimate.Method != spec.IntervalMethodRepeats || estimate.Samples != 3 {
		t.Errorf("c1 slo_max: %+v", estimate)
	}
	// mean 205, sd 5, t(2) = 4.303: half width 4.303 * 5 / sqrt(3) = 12.42
	if half := *estimate.CiHigh - 205; half < 12.41 || half > 12.43 {
		t.Errorf("c1 interval half width %g, want 12.42", half)
	}
}

func TestOverlappingRepeatsHaveNoWinner(t *testing.T) {
	input := runs(append(repeats("A", 100, 200, 260, 230), repeats("B", 400, 220, 280, 250)...)...)
	result := mustCompare(t, input, spec.ObjectiveNameSloMaxRps)
	primary := result.Rankings[0]
	if primary.IsSignificant || primary.WinnerId != nil || primary.Confidence != spec.ComparisonConfidenceLow {
		t.Fatalf("overlapping intervals must not win: %+v", primary)
	}
	if primary.Order[0].ConfigurationId != "c2" {
		t.Errorf("still ranked best first: %+v", primary.Order)
	}
	if result.Recommendation != nil || !strings.Contains(primary.Reason, "overlap") {
		t.Errorf("recommendation %+v, reason %q", result.Recommendation, primary.Reason)
	}
}

func TestSingleSamplesNeedTwiceTheNoiseFloor(t *testing.T) {
	cases := []struct {
		name   string
		b      float64
		winner bool
	}{{"20% lead", 240, true}, {"5% lead", 210, false}, {"exactly 10% lead", 220, false}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := runs(fixture{id: "A", knobs: knobs(100), sloMax: 200}, fixture{id: "B", knobs: knobs(400), sloMax: c.b})
			result := mustCompare(t, input, spec.ObjectiveNameSloMaxRps)
			primary := result.Rankings[0]
			if primary.IsSignificant != c.winner {
				t.Fatalf("significant = %v, want %v: %s", primary.IsSignificant, c.winner, primary.Reason)
			}
			if c.winner && (primary.Confidence != spec.ComparisonConfidenceMedium || result.Recommendation == nil) {
				t.Errorf("a single-sample win is medium confidence: %+v", primary)
			}
			estimate, _ := objectiveOf(result.Configurations[0], spec.ObjectiveNameSloMaxRps)
			if *estimate.Method != spec.IntervalMethodSingleSample || estimate.CiLow != nil {
				t.Errorf("single sample estimate: %+v", estimate)
			}
		})
	}
}

func TestDifferentWorkloadsAreNotComparable(t *testing.T) {
	input := runs(fixture{id: "A", knobs: knobs(100), sloMax: 100}, fixture{id: "B", knobs: knobs(400), sloMax: 300, workload: "sha256:other"})
	result := mustCompare(t, input, spec.ObjectiveNameSloMaxRps)
	if result.Comparability.IsComparable || !hasIssue(result.Comparability, spec.ComparisonIssueCodeWorkloadDiffers) {
		t.Fatalf("comparability: %+v", result.Comparability)
	}
	if result.Recommendation != nil || result.Rankings[0].WinnerId != nil {
		t.Errorf("nothing wins across different workloads")
	}
	if len(result.Rankings[0].Order) != 2 {
		t.Errorf("not comparable runs are still ranked for inspection")
	}
}

func TestDifferentLaddersAreNotComparable(t *testing.T) {
	input := runs(fixture{id: "A", knobs: knobs(100), sloMax: 100}, fixture{id: "B", knobs: knobs(400), sloMax: 300, offered: []float64{50, 200}})
	result := mustCompare(t, input, spec.ObjectiveNameSloMaxRps)
	if result.Comparability.IsComparable || !hasIssue(result.Comparability, spec.ComparisonIssueCodeLadderDiffers) {
		t.Fatalf("comparability: %+v", result.Comparability)
	}
}

func TestExcludedRunsTakeNoPart(t *testing.T) {
	input := runs(fixture{id: "A", knobs: knobs(100), sloMax: 100}, fixture{id: "B", knobs: knobs(400), sloMax: 900, excluded: true})
	result := mustCompare(t, input, spec.ObjectiveNameSloMaxRps)
	if len(result.ExcludedRuns) != 1 || result.ExcludedRuns[0].RunId != "B" {
		t.Fatalf("excluded: %+v", result.ExcludedRuns)
	}
	if len(result.Configurations) != 1 || !hasIssue(result.Comparability, spec.ComparisonIssueCodeTooFewConfigurations) {
		t.Fatalf("configurations %+v issues %+v", result.Configurations, result.Comparability.Issues)
	}
	if result.Recommendation != nil {
		t.Errorf("one configuration cannot win")
	}
}

func TestTwoKnobsWithoutEveryCombinationAreNotFactorial(t *testing.T) {
	a := spec.RunSpecKnobs{"x": float64(1), "y": "a"}
	b := spec.RunSpecKnobs{"x": float64(2), "y": "b"}
	result := mustCompare(t, runs(fixture{id: "A", knobs: a, sloMax: 100}, fixture{id: "B", knobs: b, sloMax: 300}), spec.ObjectiveNameSloMaxRps)
	if result.IsFactorial || !hasIssue(result.Comparability, spec.ComparisonIssueCodeNotFactorial) {
		t.Fatalf("factorial %v issues %+v", result.IsFactorial, result.Comparability.Issues)
	}
	c := spec.RunSpecKnobs{"x": float64(1), "y": "b"}
	d := spec.RunSpecKnobs{"x": float64(2), "y": "a"}
	full := mustCompare(t, runs(fixture{id: "A", knobs: a, sloMax: 1}, fixture{id: "B", knobs: b, sloMax: 2}, fixture{id: "C", knobs: c, sloMax: 3}, fixture{id: "D", knobs: d, sloMax: 4}), spec.ObjectiveNameSloMaxRps)
	if !full.IsFactorial {
		t.Errorf("a 2x2 design is factorial")
	}
}

func TestParetoKeepsTradeOffsAndDropsDominated(t *testing.T) {
	input := runs(
		fixture{id: "A", knobs: knobs(1), sloMax: 100, ttftScale: 0.05}, // slow but fast-responding
		fixture{id: "B", knobs: knobs(2), sloMax: 300, ttftScale: 0.20}, // fast but slower to respond
		fixture{id: "C", knobs: knobs(3), sloMax: 90, ttftScale: 0.25},  // dominated by both
	)
	result := mustCompare(t, input, spec.ObjectiveNameSloMaxRps)
	if !reflect.DeepEqual(result.ParetoConfigurationIds, []string{"c1", "c2"}) {
		t.Fatalf("pareto %v, want [c1 c2]", result.ParetoConfigurationIds)
	}
	if result.Configurations[2].IsPareto || !result.Configurations[0].IsPareto {
		t.Errorf("is_pareto flags disagree with the set")
	}
	if result.ReferenceStageIndex == nil || *result.ReferenceStageIndex != 1 {
		t.Errorf("reference stage %v, want 1", result.ReferenceStageIndex)
	}
	ttft := result.Configurations[0].ReferenceTtftP95Seconds
	if ttft == nil || *ttft.Method != spec.IntervalMethodBootstrap || ttft.Samples != 2000 {
		t.Errorf("reference TTFT: %+v", ttft)
	}
}

func TestLatencyObjectiveIsMinimizedFromPooledRecords(t *testing.T) {
	input := runs(
		fixture{id: "A0", knobs: knobs(1), sloMax: 100, ttftScale: 0.10, targetRPS: 100},
		fixture{id: "A1", knobs: knobs(1), sloMax: 100, ttftScale: 0.10, targetRPS: 100},
		fixture{id: "B0", knobs: knobs(2), sloMax: 100, ttftScale: 0.30, targetRPS: 100},
	)
	result := mustCompare(t, input, spec.ObjectiveNameP95TtftAtTargetRps)
	primary := result.Rankings[0]
	if primary.Direction != spec.ObjectiveDirectionMinimize || primary.WinnerId == nil || *primary.WinnerId != "c1" {
		t.Fatalf("primary: %+v", primary)
	}
	estimate, _ := objectiveOf(result.Configurations[0], spec.ObjectiveNameP95TtftAtTargetRps)
	if estimate.Samples != 4000 {
		t.Errorf("pooled samples %d, want both repeats' 4000", estimate.Samples)
	}
	if primary.Confidence != spec.ComparisonConfidenceMedium {
		t.Errorf("fewer than three repeats is medium confidence, got %s", primary.Confidence)
	}
}

func TestUnavailableObjectivesAreNotRanked(t *testing.T) {
	result := mustCompare(t, runs(fixture{id: "A", knobs: knobs(1), sloMax: 100}, fixture{id: "B", knobs: knobs(2), sloMax: 0}), spec.ObjectiveNameSloMaxRps)
	for _, ranking := range result.Rankings {
		if ranking.Objective == spec.ObjectiveNameSloMaxRps || ranking.Objective == spec.ObjectiveNameSloRpsPerGpu {
			t.Errorf("%s is unavailable for c2 and must not be ranked", ranking.Objective)
		}
	}
	estimate, _ := objectiveOf(result.Configurations[1], spec.ObjectiveNameSloMaxRps)
	if estimate.UnavailableReason == nil || !strings.Contains(*estimate.UnavailableReason, "run B") {
		t.Errorf("unavailable estimate names the run: %+v", estimate)
	}
	if result.Recommendation != nil {
		t.Errorf("no ranking on the primary objective means no recommendation")
	}
}

func TestTheResultDoesNotDependOnInputOrder(t *testing.T) {
	fixtures := append(repeats("A", 100, 200, 210, 205), repeats("B", 400, 320, 330, 325)...)
	forward := mustCompare(t, runs(fixtures...), "")
	reversed := make([]fixture, len(fixtures))
	for i, f := range fixtures {
		reversed[len(fixtures)-1-i] = f
	}
	backward := mustCompare(t, runs(reversed...), "")
	a, _ := json.Marshal(forward)
	b, _ := json.Marshal(backward)
	if string(a) != string(b) {
		t.Fatalf("input order changed the result:\n%s\n%s", a, b)
	}
}

func TestDuplicateRunsAreRefused(t *testing.T) {
	if _, err := Compare(runs(fixture{id: "A", sloMax: 1}, fixture{id: "A", sloMax: 1}), "", version); err == nil {
		t.Fatal("a run given twice must be refused")
	}
}

func TestALowerBoundRunnerUpCannotBeBeaten(t *testing.T) {
	censored := repeats("A", 100, 100, 100, 100)
	for i := range censored {
		censored[i].neverFailed = true
	}
	input := runs(append(censored, repeats("B", 400, 300, 310, 305)...)...)
	result := mustCompare(t, input, spec.ObjectiveNameSloMaxRps)
	primary := result.Rankings[0]
	if primary.IsSignificant || !strings.Contains(primary.Reason, "lower bound") {
		t.Fatalf("c1 never failed its SLOs; c2 cannot be called the winner: %+v", primary)
	}
	estimate, _ := objectiveOf(result.Configurations[0], spec.ObjectiveNameSloMaxRps)
	if !estimate.IsLowerBound {
		t.Errorf("c1's estimate is a lower bound: %+v", estimate)
	}
	// A lower-bound winner over a measured runner-up still wins: its true value is higher still.
	for i := range censored {
		censored[i].sloMax = 400
	}
	result = mustCompare(t, runs(append(censored, repeats("B", 400, 300, 310, 305)...)...), spec.ObjectiveNameSloMaxRps)
	if result.Rankings[0].WinnerId == nil || *result.Rankings[0].WinnerId != "c1" {
		t.Errorf("a lower-bound leader beats a measured runner-up: %+v", result.Rankings[0])
	}
}

// TestTheSharedPresetIsNamed: the comparison names the workload preset only
// when every run named the same one.
func TestTheSharedPresetIsNamed(t *testing.T) {
	named := func(fixtures []Run, presets ...*spec.PresetRef) []Run {
		for i := range fixtures {
			fixtures[i].Spec.WorkloadPreset = presets[i]
		}
		return fixtures
	}
	chat := &spec.PresetRef{Name: "chat-short", Version: 1}
	rag := &spec.PresetRef{Name: "rag-prefill", Version: 1}
	pair := func() []Run {
		return runs(fixture{id: "A", knobs: knobs(100), sloMax: 100}, fixture{id: "B", knobs: knobs(400), sloMax: 300})
	}
	same := mustCompare(t, named(pair(), chat, &spec.PresetRef{Name: "chat-short", Version: 1}), spec.ObjectiveNameSloMaxRps)
	if got := same.Comparability.WorkloadPreset; got == nil || *got != *chat {
		t.Errorf("both runs used chat-short@1: preset = %v", got)
	}
	for name, input := range map[string][]Run{
		"one without a preset": named(pair(), chat, nil),
		"different presets":    named(pair(), chat, rag),
		"no presets":           pair(),
	} {
		if got := mustCompare(t, input, spec.ObjectiveNameSloMaxRps).Comparability.WorkloadPreset; got != nil {
			t.Errorf("%s: preset = %v, want none", name, got)
		}
	}
}

// Model servers compare by SLO RPS per GPU, routers per core, anything else by SLO-max RPS.
func TestDefaultObjectivePerTargetType(t *testing.T) {
	for targetType, want := range map[spec.TargetType]spec.ObjectiveName{
		spec.TargetTypeVllm:             spec.ObjectiveNameSloRpsPerGpu,
		spec.TargetTypeSglang:           spec.ObjectiveNameSloRpsPerGpu,
		spec.TargetTypeBifrost:          spec.ObjectiveNameSloRpsPerCore,
		spec.TargetTypeLitellm:          spec.ObjectiveNameSloRpsPerCore,
		spec.TargetTypeOpenaiCompatible: spec.ObjectiveNameSloMaxRps,
	} {
		if got := DefaultObjective(targetType); got != want {
			t.Errorf("%s: objective %s, want %s", targetType, got, want)
		}
	}
}

// vLLM against SGLang: the same workload, model, hardware and SLOs make the
// server a compared dimension, a varied knob.
func serverFixture(id string, server spec.TargetType, sloMax float64) fixture {
	return fixture{id: id, sloMax: sloMax, gpus: 1, targetType: server, model: "qwen-0.5b", hardware: "rtx-3070x1"}
}

func TestVLLMAndSGLangCompareAsTheServerDimension(t *testing.T) {
	input := runs(serverFixture("A", spec.TargetTypeVllm, 100), serverFixture("B", spec.TargetTypeSglang, 140))
	result := mustCompare(t, input, "")
	if !result.Comparability.IsComparable {
		t.Fatalf("comparability: %+v", result.Comparability)
	}
	if len(result.VariedKnobs) != 1 || result.VariedKnobs[0] != ServerKnob {
		t.Errorf("varied knobs = %v, want [server]", result.VariedKnobs)
	}
	if result.Comparability.TargetType != nil {
		t.Errorf("a mixed comparison has no one target type: %v", *result.Comparability.TargetType)
	}
	servers := map[string]bool{}
	for _, configuration := range result.Configurations {
		server, _ := configuration.Knobs[ServerKnob].(string)
		servers[server] = true
	}
	if !servers["vllm"] || !servers["sglang"] || len(result.Configurations) != 2 {
		t.Errorf("configurations = %+v", result.Configurations)
	}
	if result.Rankings[0].Objective != spec.ObjectiveNameSloRpsPerGpu {
		t.Errorf("objective = %s, want the model servers' default", result.Rankings[0].Objective)
	}
}

func TestVLLMAndSGLangNeedTheSameModelAndKnownHardware(t *testing.T) {
	otherModel := serverFixture("B", spec.TargetTypeSglang, 140)
	otherModel.model = "llama-8b"
	result := mustCompare(t, runs(serverFixture("A", spec.TargetTypeVllm, 100), otherModel), "")
	if result.Comparability.IsComparable || !hasIssue(result.Comparability, spec.ComparisonIssueCodeTargetTypeDiffers) {
		t.Errorf("different models: %+v", result.Comparability)
	}
	otherGPU := serverFixture("B", spec.TargetTypeSglang, 140)
	otherGPU.hardware = "a100x1"
	result = mustCompare(t, runs(serverFixture("A", spec.TargetTypeVllm, 100), otherGPU), "")
	if result.Comparability.IsComparable || !hasIssue(result.Comparability, spec.ComparisonIssueCodeHardwareDiffers) {
		t.Errorf("different hardware: %+v", result.Comparability)
	}
	unknownA, unknownB := serverFixture("A", spec.TargetTypeVllm, 100), serverFixture("B", spec.TargetTypeSglang, 140)
	unknownA.hardware, unknownB.hardware = "", ""
	result = mustCompare(t, runs(unknownA, unknownB), "")
	if result.Comparability.IsComparable || !hasIssue(result.Comparability, spec.ComparisonIssueCodeHardwareDiffers) {
		t.Errorf("unknown hardware: %+v", result.Comparability)
	}
}

func TestRoutersAndModelServersStillNeverMix(t *testing.T) {
	router := serverFixture("B", spec.TargetTypeBifrost, 140)
	result := mustCompare(t, runs(serverFixture("A", spec.TargetTypeVllm, 100), router), "")
	if result.Comparability.IsComparable || !hasIssue(result.Comparability, spec.ComparisonIssueCodeTargetTypeDiffers) {
		t.Errorf("vllm and bifrost: %+v", result.Comparability)
	}
	if slices.Contains(result.VariedKnobs, ServerKnob) {
		t.Error("the server knob is only for vLLM against SGLang")
	}
}

func TestOneServerComparisonsAreUnchanged(t *testing.T) {
	result := mustCompare(t, runs(serverFixture("A", spec.TargetTypeSglang, 100), serverFixture("B", spec.TargetTypeSglang, 140)), "")
	if slices.Contains(result.VariedKnobs, ServerKnob) || result.Comparability.TargetType == nil || *result.Comparability.TargetType != spec.TargetTypeSglang {
		t.Errorf("varied %v, target type %v", result.VariedKnobs, result.Comparability.TargetType)
	}
}
