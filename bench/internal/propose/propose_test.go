package propose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

const testdata = "../../testdata/propose"

func readSpace(t *testing.T, name string) SearchSpace {
	t.Helper()
	document, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return parseSpace(t, string(document))
}

func parseSpace(t *testing.T, document string) SearchSpace {
	t.Helper()
	space, err := ParseSearchSpace([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func readBoots(t *testing.T, document string) BootResults {
	t.Helper()
	boots, err := ParseBootResults([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	return boots
}

func fixtureBoots(t *testing.T) BootResults {
	t.Helper()
	document, err := os.ReadFile(filepath.Join(testdata, "boots.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return readBoots(t, string(document))
}

func loadBundles(t *testing.T, names ...string) []Evidence {
	t.Helper()
	dirs := make([]string, len(names))
	for i, name := range names {
		dirs[i] = filepath.Join(testdata, "runs", name)
	}
	evidence, err := LoadEvidence(context.Background(), dirs)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func mustPropose(t *testing.T, space SearchSpace, boots BootResults, evidence []Evidence) Proposal {
	t.Helper()
	proposal, err := Propose(space, boots, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

// screeningBundles are the three original-ladder runs: max_num_seqs 64, 128, 256.
var screeningBundles = []string{"seqs64-probe", "seqs128", "seqs256"}

func confirmBundles(seqs string, repeats int) []string {
	var names []string
	for i := 1; i <= repeats; i++ {
		names = append(names, "confirm-seqs"+seqs+"-r"+string(rune('0'+i)))
	}
	return names
}

func numberKnob(t *testing.T, proposed Proposed, name string) float64 {
	t.Helper()
	value, ok := proposed.Knobs[name].(float64)
	if !ok {
		t.Fatalf("%s of %v is not a number", name, proposed.Knobs)
	}
	return value
}

func seqsOf(t *testing.T, proposals []Proposed) []float64 {
	t.Helper()
	var values []float64
	for _, proposed := range proposals {
		values = append(values, numberKnob(t, proposed, "vllm.max_num_seqs"))
	}
	return values
}

func TestFirstProposalIsTheBaselineOnAProbeLadder(t *testing.T) {
	proposal := mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, nil)
	if proposal.Status != StatusPropose || proposal.Phase != PhaseBaseline || len(proposal.Proposals) != 1 {
		t.Fatalf("got %s/%s with %d proposals", proposal.Status, proposal.Phase, len(proposal.Proposals))
	}
	first := proposal.Proposals[0]
	want := Knobs{
		"vllm.enable_prefix_caching": true, "vllm.gpu_memory_utilization": 0.75, "vllm.max_num_seqs": 64.0,
		"vllm.max_num_batched_tokens": 8192.0, "vllm.async_scheduling": false, "vllm.api_server_count": 1.0,
	}
	if !reflect.DeepEqual(Knobs(first.Knobs), want) {
		t.Fatalf("baseline knobs %v, want %v", first.Knobs, want)
	}
	if first.Action != ActionRun || first.Ladder.Kind != LadderProbe || first.Ladder.CentreRps != nil || !slices.Equal(first.Ladder.Rps, defaultProbeRPS) {
		t.Fatalf("ladder %+v", first.Ladder)
	}
	if first.Rationale.Kind != KindBaseline || proposal.Leader != nil {
		t.Fatalf("rationale %+v, leader %+v", first.Rationale, proposal.Leader)
	}
	if proposal.Objective != spec.ObjectiveNameSloRpsPerGpu || proposal.NoiseFloor != DefaultNoiseFloor {
		t.Fatalf("defaults: objective %s, noise floor %g", proposal.Objective, proposal.NoiseFloor)
	}
}

func TestFeasibilityComesFirstAsBootProbesWithNoLadder(t *testing.T) {
	proposal := mustPropose(t, readSpace(t, "space-feasibility.yaml"), BootResults{}, nil)
	if proposal.Phase != PhaseFeasibility || len(proposal.Proposals) != 6 {
		t.Fatalf("phase %s with %d proposals, want 6 boot probes (3 lengths x 2 dtypes)", proposal.Phase, len(proposal.Proposals))
	}
	for _, probe := range proposal.Proposals {
		if probe.Action != ActionBootProbe || probe.Ladder != nil || *probe.Rationale.Tier != TierFeasibility {
			t.Fatalf("probe %+v", probe)
		}
	}
	if proposal.Tiers[0].State != TierActive || proposal.Tiers[1].State != TierPending {
		t.Fatalf("tiers %+v", proposal.Tiers)
	}
}

func TestInfeasibleAndTooSmallConfigurationsAreNeverProposed(t *testing.T) {
	space := readSpace(t, "space-feasibility.yaml")
	boots := fixtureBoots(t)
	proposal := mustPropose(t, space, boots, nil)
	if proposal.Phase != PhaseBaseline {
		t.Fatalf("phase %s: every combination is probed, so the baseline runs", proposal.Phase)
	}
	baseline := proposal.Proposals[0].Knobs
	if baseline["vllm.max_model_len"] != 8192.0 || baseline["vllm.kv_cache_dtype"] != "fp8" {
		t.Fatalf("baseline %v: 16384 did not boot and auto holds only 48 and 60 of the 64 requests the preset needs", baseline)
	}
	if got := proposal.Tiers[0]; got.State != TierConverged || got.Reason == nil || !strings.Contains(*got.Reason, "2 of 6") {
		t.Fatalf("feasibility tier %+v", got)
	}
	s, err := newState(space, boots, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, knobs := range []Knobs{
		{"vllm.max_model_len": 16384.0, "vllm.kv_cache_dtype": "fp8"},
		{"vllm.max_model_len": 8192.0, "vllm.kv_cache_dtype": "auto"},
		{"vllm.max_model_len": 4096.0, "vllm.kv_cache_dtype": "auto"},
	} {
		if s.viable(knobs) {
			t.Errorf("%v is viable", knobs)
		}
	}
	if !s.viable(Knobs{"vllm.max_model_len": 4096.0, "vllm.kv_cache_dtype": "fp8"}) {
		t.Error("4096 with fp8 should be viable")
	}
}

func TestNothingFeasibleIsADoneVerdict(t *testing.T) {
	space := readSpace(t, "space-feasibility.yaml")
	space.MinConcurrency = 500
	proposal := mustPropose(t, space, fixtureBoots(t), nil)
	if proposal.Status != StatusDone || proposal.Done.Outcome != OutcomeNoFeasible {
		t.Fatalf("got %s %+v", proposal.Status, proposal.Done)
	}
}

func TestAdvisorGuidedStepRaisesMaxNumSeqsAndCentresTheLadder(t *testing.T) {
	proposal := mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, loadBundles(t, "seqs64-probe"))
	if proposal.Phase != PhaseScreen || len(proposal.Proposals) != 1 {
		t.Fatalf("phase %s with %d proposals", proposal.Phase, len(proposal.Proposals))
	}
	next := proposal.Proposals[0]
	if numberKnob(t, next, "vllm.max_num_seqs") != 128 || next.Rationale.Kind != KindAdvisor || *next.Rationale.Advisor != "V1" ||
		*next.Rationale.Tier != TierScheduler || !slices.Equal(next.Rationale.BasedOnRuns, []string{"01M807ST8SRKPDH0318H1T4RYW"}) {
		t.Fatalf("proposal %+v", next)
	}
	// 21 requests/s (the recorded SLO-max of max_num_seqs 64) times the screening factors.
	if next.Ladder.Kind != LadderScreen || *next.Ladder.CentreRps != 21 || !slices.Equal(next.Ladder.Rps, []float64{14.7, 17.85, 21, 24.15, 27.3}) || *next.Repeats != 1 {
		t.Fatalf("ladder %+v repeats %d", next.Ladder, next.Repeats)
	}
	if proposal.Leader.ConfigId != *next.Rationale.Against || proposal.Tiers[2].State != TierActive || proposal.Tiers[1].Reason == nil {
		t.Fatalf("leader %+v tiers %+v", proposal.Leader, proposal.Tiers)
	}
}

func TestAdvisorStepIsBoundedByTheKVCache(t *testing.T) {
	boots := readBoots(t, `{"results": [{"knobs": {"vllm.enable_prefix_caching": true}, "outcome": "feasible", "c_kv": 200}]}`)
	proposal := mustPropose(t, readSpace(t, "space.yaml"), boots, loadBundles(t, "seqs64-probe", "seqs128"))
	// V1 on max_num_seqs 128 recommends 256, with the measured C_kv 310 as its maximum; the boot probe says 200.
	if len(proposal.Proposals) != 1 || numberKnob(t, proposal.Proposals[0], "vllm.max_num_seqs") != 200 {
		t.Fatalf("proposals %+v", proposal.Proposals)
	}
}

func TestAdvisorStepIsBoundedByTheSearchSpace(t *testing.T) {
	space := parseSpace(t, strings.Replace(readText(t, "space.yaml"), "max: 512", "max: 128", 1))
	proposal := mustPropose(t, space, BootResults{}, loadBundles(t, "seqs64-probe", "seqs128"))
	for _, proposed := range proposal.Proposals {
		if proposed.Rationale.Kind == KindAdvisor {
			t.Fatalf("an advisor step beyond max 128: %+v", proposed)
		}
	}
	if !slices.ContainsFunc(proposal.Notes, func(note string) bool { return strings.Contains(note, "V1") && strings.Contains(note, "edge") }) {
		t.Fatalf("notes %v", proposal.Notes)
	}
}

func readText(t *testing.T, name string) string {
	t.Helper()
	document, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(document)
}

func TestOtherAdvisorRules(t *testing.T) {
	space := parseSpace(t, `
base: {vllm.enable_prefix_caching: true, vllm.gpu_memory_utilization: 0.75}
knobs:
  - {name: vllm.max_num_seqs, tier: scheduler, min: 32, max: 512}
  - {name: vllm.max_num_batched_tokens, tier: scheduler, values: [2048, 4096, 8192, 16384]}
  - {name: vllm.max_model_len, tier: scheduler, min: 1024, max: 8192}
`)
	cases := []struct {
		rule, knob  string
		recommended any
		want        float64
	}{
		{"V2", "vllm.max_num_seqs", nil, 64},
		{"V3", "vllm.max_model_len", 2048.0, 2048},
		{"V4", "vllm.max_num_batched_tokens", nil, 16384},
		{"V5", "vllm.max_num_batched_tokens", nil, 4096},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			evidence := loadBundles(t, "seqs128")
			evidence[0].Run.Spec.Knobs["vllm.max_model_len"] = 8192.0
			var recommended *float64
			if value, ok := c.recommended.(float64); ok {
				recommended = &value
			}
			evidence[0].Advice = []Advice{{Rule: c.rule, Knob: c.knob, Recommended: recommended}}
			proposal := mustPropose(t, space, BootResults{}, evidence)
			if len(proposal.Proposals) != 1 {
				t.Fatalf("%d proposals, notes %v", len(proposal.Proposals), proposal.Notes)
			}
			got := proposal.Proposals[0]
			if numberKnob(t, got, c.knob) != c.want || *got.Rationale.Advisor != c.rule || got.Rationale.Kind != KindAdvisor {
				t.Fatalf("proposal %+v, want %s=%g", got, c.knob, c.want)
			}
		})
	}
}

func TestLeaderChangesOnlyByMoreThanTheNoiseFloor(t *testing.T) {
	evidence := loadBundles(t, screeningBundles...)
	// max_num_seqs 256 beats 128 by 3.9%: inside the 5% noise floor.
	proposal := mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, evidence)
	if numberKnob(t, Proposed{Knobs: proposal.Leader.Knobs}, "vllm.max_num_seqs") != 128 {
		t.Fatalf("leader %+v", proposal.Leader)
	}
	space := readSpace(t, "space.yaml")
	space.NoiseFloor = 0.03
	proposal = mustPropose(t, space, BootResults{}, loadBundles(t, screeningBundles...))
	if numberKnob(t, Proposed{Knobs: proposal.Leader.Knobs}, "vllm.max_num_seqs") != 256 {
		t.Fatalf("with a 3%% noise floor the leader is %+v", proposal.Leader)
	}
}

// coordinateStepOf is the one proposal of a coordinate step, checked to be one.
func coordinateStepOf(t *testing.T, proposal Proposal) Proposed {
	t.Helper()
	if proposal.Phase != PhaseScreen || len(proposal.Proposals) != 1 {
		t.Fatalf("phase %s with %d proposals: %+v", proposal.Phase, len(proposal.Proposals), proposal.Notes)
	}
	step := proposal.Proposals[0]
	if step.Rationale.Kind != KindGrid || step.Rationale.GridCell == nil || !strings.HasPrefix(step.Rationale.Text, "coordinate step:") {
		t.Fatalf("not a coordinate step: %+v", step.Rationale)
	}
	return step
}

func TestCoordinateStepsAfterTheAdvisorsAreSpent(t *testing.T) {
	proposal := mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, loadBundles(t, screeningBundles...))
	// Leader 128 x 8192; 64 and 256 at 8192 ran and lost, so max_num_seqs is converged. No
	// max_num_batched_tokens neighbour has run, so its first step goes up: one run, not a grid.
	step := coordinateStepOf(t, proposal)
	if numberKnob(t, step, "vllm.max_num_seqs") != 128 || numberKnob(t, step, "vllm.max_num_batched_tokens") != 16384 {
		t.Fatalf("step %v", step.Knobs)
	}
	cell := step.Rationale.GridCell
	if cell.MaxNumSeqs != PositionCentre || cell.MaxNumBatchedTokens != PositionUp || !strings.Contains(step.Rationale.Text, "up first") {
		t.Fatalf("rationale %+v %+v", step.Rationale, cell)
	}
	if step.Ladder.Kind != LadderScreen || *step.Ladder.CentreRps != 37.92 {
		t.Fatalf("ladder %+v", step.Ladder)
	}
}

// Session 3 on the RTX 3070 spent three runs on max_num_seqs 16 before trying 64, which won:
// a step now continues the way the leader was reached.
func TestACoordinateStepContinuesTheClimb(t *testing.T) {
	evidence := loadBundles(t, screeningBundles...)
	for i := range evidence {
		evidence[i].Advice = nil                                      // advisors come first; here none fires
		if evidence[i].Run.Spec.Knobs["vllm.max_num_seqs"] == 256.0 { // a tokens step instead, which lost
			evidence[i].Run.Spec.Knobs["vllm.max_num_seqs"] = 128.0
			evidence[i].Run.Spec.Knobs["vllm.max_num_batched_tokens"] = 16384.0
		}
	}
	step := coordinateStepOf(t, mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, evidence))
	// The leader 128 beat 64 below it, so 256 comes next, before any other move.
	if numberKnob(t, step, "vllm.max_num_seqs") != 256 || numberKnob(t, step, "vllm.max_num_batched_tokens") != 8192 ||
		!strings.Contains(step.Rationale.Text, "continuing up") {
		t.Fatalf("step %v: %s", step.Knobs, step.Rationale.Text)
	}
}

func TestACoordinateStepTriesTheOtherWayAfterALoss(t *testing.T) {
	evidence := loadBundles(t, screeningBundles...)
	for i := range evidence {
		evidence[i].Advice = nil
		if evidence[i].Run.Spec.Knobs["vllm.max_num_seqs"] == 256.0 {
			evidence[i].Run.Spec.Knobs["vllm.max_num_seqs"] = 128.0
			evidence[i].Run.Spec.Knobs["vllm.max_num_batched_tokens"] = 16384.0
		}
		if evidence[i].Run.Spec.Knobs["vllm.max_num_seqs"] == 64.0 { // the seqs axis done: 64 lost...
			evidence[i].Run.Spec.Knobs["vllm.max_num_seqs"] = 256.0 // ...and so did 256
		}
	}
	step := coordinateStepOf(t, mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, evidence))
	// max_num_seqs: 256 lost above, and 64 below has not run, so down; tokens waits its turn.
	if numberKnob(t, step, "vllm.max_num_seqs") != 64 || !strings.Contains(step.Rationale.Text, "continuing down") {
		t.Fatalf("step %v: %s", step.Knobs, step.Rationale.Text)
	}
}

func TestFineTuningScreensApiServerCountOnlyWhenV7Fires(t *testing.T) {
	space := readSpace(t, "space-fine.yaml")
	quiet := mustPropose(t, space, BootResults{}, loadBundles(t, "seqs128"))
	if len(quiet.Proposals) != 1 || quiet.Proposals[0].Knobs["vllm.async_scheduling"] != true || quiet.Proposals[0].Rationale.Kind != KindFineTuningAB {
		t.Fatalf("proposals %+v", quiet.Proposals)
	}
	if !slices.ContainsFunc(quiet.Notes, func(note string) bool { return strings.Contains(note, "V7") }) {
		t.Fatalf("notes %v", quiet.Notes)
	}
	frontend := mustPropose(t, space, BootResults{}, loadBundles(t, "seqs128-frontend"))
	if len(frontend.Proposals) != 2 || frontend.Proposals[1].Knobs["vllm.api_server_count"] != 2.0 {
		t.Fatalf("proposals %+v", frontend.Proposals)
	}
}

func TestStructuralTierScreensEverySurvivingValue(t *testing.T) {
	space := parseSpace(t, `
base: {vllm.enable_prefix_caching: true, vllm.gpu_memory_utilization: 0.75}
knobs:
  - {name: vllm.enable_chunked_prefill, tier: structural, values: [true, false]}
  - {name: vllm.attention_backend, tier: structural, values: [FLASH_ATTN, FLASHINFER, TRITON_ATTN]}
`)
	proposal := mustPropose(t, space, BootResults{}, loadBundles(t, "seqs128"))
	if proposal.Phase != PhaseScreen || len(proposal.Proposals) != 5 {
		t.Fatalf("%d proposals, want 2 chunked-prefill values and 3 backends: %+v", len(proposal.Proposals), proposal.Proposals)
	}
	for _, proposed := range proposal.Proposals {
		if proposed.Rationale.Kind != KindStructural || *proposed.Rationale.Tier != TierStructural {
			t.Fatalf("rationale %+v", proposed.Rationale)
		}
	}
	// A structural value a boot probe ruled out is dropped.
	boots := readBoots(t, `{"results": [{"knobs": {"vllm.attention_backend": "FLASHINFER"}, "outcome": "infeasible", "reason": "no kernel for this GPU"}]}`)
	proposal = mustPropose(t, space, boots, loadBundles(t, "seqs128"))
	if len(proposal.Proposals) != 4 {
		t.Fatalf("%d proposals after ruling out FLASHINFER", len(proposal.Proposals))
	}
}

func TestConvergedSearchAsksForTheFullLadderWithRepeats(t *testing.T) {
	space := readSpace(t, "space-seqs.yaml")
	proposal := mustPropose(t, space, BootResults{}, loadBundles(t, screeningBundles...))
	if proposal.Status != StatusPropose || proposal.Phase != PhaseConfirm || len(proposal.Proposals) != 2 {
		t.Fatalf("%s/%s with %d proposals; notes %v", proposal.Status, proposal.Phase, len(proposal.Proposals), proposal.Notes)
	}
	// The top two on screening: 256 (39.4), then 128 (37.9); both need 3 repeats.
	if got := seqsOf(t, proposal.Proposals); !slices.Equal(got, []float64{256, 128}) {
		t.Fatalf("confirming %v", got)
	}
	for _, proposed := range proposal.Proposals {
		if *proposed.Repeats != 3 || proposed.Ladder.Kind != LadderFull || len(proposed.Ladder.Rps) != 9 || proposed.Rationale.Kind != KindConfirm {
			t.Fatalf("proposal %+v ladder %+v", proposed, proposed.Ladder)
		}
	}
	for _, tier := range proposal.Tiers {
		if tier.State != TierConverged || tier.Reason == nil {
			t.Fatalf("tier %+v", tier)
		}
	}
	// Runs that already exist are not asked for again.
	evidence := loadBundles(t, append(append(slices.Clone(screeningBundles), confirmBundles("256", 3)...), confirmBundles("128", 1)...)...)
	proposal = mustPropose(t, space, BootResults{}, evidence)
	if len(proposal.Proposals) != 1 || numberKnob(t, proposal.Proposals[0], "vllm.max_num_seqs") != 128 || *proposal.Proposals[0].Repeats != 2 {
		t.Fatalf("proposals %+v", proposal.Proposals)
	}
}

func allConfirmedEvidence(t *testing.T) []Evidence {
	t.Helper()
	names := append(slices.Clone(screeningBundles), confirmBundles("256", 3)...)
	return loadBundles(t, append(names, confirmBundles("128", 3)...)...)
}

func TestConfirmedWinnerIsSignificant(t *testing.T) {
	proposal := mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, allConfirmedEvidence(t))
	if proposal.Status != StatusDone || proposal.Phase != PhaseDone || proposal.Done.Outcome != OutcomeWinner {
		t.Fatalf("%+v", proposal.Done)
	}
	winner := proposal.Done.Winner
	if !winner.IsSignificant || winner.Knobs["vllm.max_num_seqs"] != 256.0 || winner.Confidence != "high" || len(winner.RunIds) != 3 ||
		!strings.Contains(proposal.Done.Reason, "do not overlap") {
		t.Fatalf("winner %+v reason %q", winner, proposal.Done.Reason)
	}
	// The recorded fine-ladder comparison: 44.02 against 41.71 requests/s per GPU.
	if winner.ObjectiveValue < 44.01 || winner.ObjectiveValue > 44.03 {
		t.Fatalf("value %g", winner.ObjectiveValue)
	}
}

func TestConfirmationThatCannotSeparateTheLeadersSaysSo(t *testing.T) {
	evidence := allConfirmedEvidence(t)
	for _, item := range evidence {
		if item.Run.Spec.Knobs["vllm.max_num_seqs"] == 128.0 && len(item.Run.Analysis.Stages) == 9 {
			item.Run.Analysis.Capacity.SloMax.Rps = 44.0 + item.Run.Analysis.Capacity.SloMax.Rps - 41.71 // the same spread, centred on 44
		}
	}
	proposal := mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, evidence)
	if proposal.Done.Outcome != OutcomeNoSignificantWinner || proposal.Done.Winner == nil || proposal.Done.Winner.IsSignificant ||
		!strings.Contains(proposal.Done.Reason, "overlap") {
		t.Fatalf("%+v", proposal.Done)
	}
}

func TestNoConfigurationMeetingTheSLOsIsADoneVerdict(t *testing.T) {
	evidence := loadBundles(t, screeningBundles...)
	for i := range evidence {
		evidence[i].Run.Analysis.Capacity.SloMax = nil
	}
	proposal := mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, evidence)
	if proposal.Status != StatusDone || proposal.Done.Outcome != OutcomeNoSLOMet {
		t.Fatalf("%s %+v %+v", proposal.Status, proposal.Done, proposal.Proposals)
	}
}

// TestTooFewSamplesSayWhy: stages too short or too slow for a p95 judge no
// SLO; the verdict says so instead of blaming the target.
func TestTooFewSamplesSayWhy(t *testing.T) {
	evidence := loadBundles(t, screeningBundles...)
	for i := range evidence {
		evidence[i].Run.Analysis.Capacity.SloMax = nil
		for j := range evidence[i].Run.Analysis.Stages {
			evidence[i].Run.Analysis.Stages[j].SloResults = nil
		}
	}
	proposal := mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, evidence)
	if proposal.Done == nil || proposal.Done.Outcome != OutcomeNoSLOMet || !strings.Contains(proposal.Done.Reason, "the requests its percentile needs") {
		t.Fatalf("%+v", proposal.Done)
	}
}

func TestOutputIsIndependentOfBundleOrder(t *testing.T) {
	space := readSpace(t, "space.yaml")
	forward := mustPropose(t, space, BootResults{}, loadBundles(t, screeningBundles...))
	backward := mustPropose(t, space, BootResults{}, loadBundles(t, "seqs256", "seqs128", "seqs64-probe"))
	if !reflect.DeepEqual(forward, backward) {
		t.Fatal("the proposal depends on the order of the bundles")
	}
}

func TestRunGivenTwiceIsAnError(t *testing.T) {
	_, err := Propose(readSpace(t, "space.yaml"), BootResults{}, loadBundles(t, "seqs128", "seqs128"))
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err %v", err)
	}
}

func TestMissingGPUCountIsReportedNotGuessed(t *testing.T) {
	evidence := loadBundles(t, "seqs128")
	evidence[0].Run.Analysis.Capacity.PerReplica.TotalGpus = nil
	_, err := Propose(readSpace(t, "space.yaml"), BootResults{}, evidence)
	if err == nil || !strings.Contains(err.Error(), "no GPU count") {
		t.Fatalf("err %v", err)
	}
}

func TestProposalsCarryTheLabelsTheCallerMustSet(t *testing.T) {
	proposal := mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, nil)
	first := proposal.Proposals[0]
	if first.Labels[LabelLadder] != "probe" || first.Labels[LabelConfig] != first.ConfigId {
		t.Fatalf("labels %v", first.Labels)
	}
	probes := mustPropose(t, readSpace(t, "space-feasibility.yaml"), BootResults{}, nil)
	if _, ok := probes.Proposals[0].Labels[LabelLadder]; ok {
		t.Fatalf("a boot probe has no ladder: %v", probes.Proposals[0].Labels)
	}
}

func TestAConfirmationRunWhoseLadderAbortedStillCountsAsConfirmation(t *testing.T) {
	names := append(slices.Clone(screeningBundles), confirmBundles("256", 3)...)
	names = append(names, confirmBundles("128", 2)...)
	names = append(names, "confirm-seqs128-aborted")
	evidence := loadBundles(t, names...)
	proposal := mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, evidence)
	// 8 stages, not 9, but labelled full: the third repeat of 128 is in, so the
	// search is judged rather than asked for another run. The comparison
	// rightly refuses to rank runs whose ladders differ.
	if proposal.Status != StatusDone || proposal.Done.Outcome != OutcomeNoSignificantWinner || !strings.Contains(proposal.Done.Reason, "not comparable") {
		t.Fatalf("%s %+v %+v", proposal.Status, proposal.Done, proposal.Proposals)
	}
}

func TestABundleWithoutTheLadderLabelIsRefused(t *testing.T) {
	evidence := loadBundles(t, "seqs128")
	delete(evidence[0].Run.Spec.Labels, LabelLadder)
	_, err := Propose(readSpace(t, "space.yaml"), BootResults{}, evidence)
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "01M808ZJD5N88S47PGSSC5WK78") || !strings.Contains(err.Error(), LabelLadder) {
		t.Fatalf("err %v", err)
	}
	evidence = loadBundles(t, "seqs128")
	evidence[0].Run.Spec.Labels[LabelLadder] = "huge"
	if _, err := Propose(readSpace(t, "space.yaml"), BootResults{}, evidence); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err %v", err)
	}
}

func TestRunsThatMeasuredDifferentThingsAreRefused(t *testing.T) {
	mutations := map[string]func(*Evidence){
		"target model": func(e *Evidence) { e.Run.Spec.Target.Model = "another/model" },
		"target type":  func(e *Evidence) { e.Run.Spec.Target.Type = spec.TargetTypeLitellm },
		"SLOs":         func(e *Evidence) { e.Run.Spec.Slos = &spec.Slos{} },
		"GPU count":    func(e *Evidence) { two := 2; e.Run.Analysis.Capacity.PerReplica.TotalGpus = &two },
		"workload preset": func(e *Evidence) {
			e.Run.Spec.WorkloadPreset = &spec.PresetRef{Name: "chat"}
		},
		"workload hash": func(e *Evidence) {
			e.Run.Model = &spec.RunCapacityModel{Identity: spec.ModelIdentity{WorkloadHash: "sha256:other"}}
		},
	}
	for field, mutate := range mutations {
		t.Run(field, func(t *testing.T) {
			evidence := loadBundles(t, "seqs64-probe", "seqs128")
			mutate(&evidence[1])
			_, err := Propose(readSpace(t, "space.yaml"), BootResults{}, evidence)
			if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "01M808ZJD5N88S47PGSSC5WK78") || !strings.Contains(err.Error(), field) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestARunWhoseKnobWasContradictedIsExcludedAndReported(t *testing.T) {
	evidence := loadBundles(t, "seqs64-probe", "seqs128", "seqs256")
	captured := "64"
	evidence[2].Run.Analysis.Quality.Knobs = []spec.KnobCheck{
		{Name: "vllm.max_num_seqs", Declared: "256", Actual: &captured, Status: spec.KnobStatusMismatch},
		{Name: "vllm.enable_prefix_caching", Declared: "true", Status: spec.KnobStatusUnverified},
	}
	// With 3% noise, 256 would lead if it were ranked.
	space := readSpace(t, "space.yaml")
	space.NoiseFloor = 0.03
	proposal := mustPropose(t, space, BootResults{}, evidence)
	if numberKnob(t, Proposed{Knobs: proposal.Leader.Knobs}, "vllm.max_num_seqs") != 128 {
		t.Fatalf("leader %+v", proposal.Leader)
	}
	if !slices.ContainsFunc(proposal.Notes, func(n string) bool {
		return strings.Contains(n, "01M80A7FZ1CWEKRXX4ZTN1R4NB") && strings.Contains(n, "declared 256, captured 64")
	}) {
		t.Fatalf("notes %v", proposal.Notes)
	}
	// The configuration stays tried: it is not proposed again as it was.
	for _, proposed := range proposal.Proposals {
		if proposed.Knobs["vllm.max_num_seqs"] == 256.0 && proposed.Knobs["vllm.max_num_batched_tokens"] == 8192.0 {
			t.Fatalf("re-proposed %v", proposed.Knobs)
		}
	}
}

func TestGridRespectsTheAdvisorsKVMaximum(t *testing.T) {
	evidence := loadBundles(t, "seqs128")
	evidence[0].Advice = []Advice{{Rule: "V1", Knob: "vllm.max_num_seqs", Recommended: ptr(256.0), Max: ptr(100.0)}}
	proposal := mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, evidence)
	if len(proposal.Proposals) == 0 {
		t.Fatalf("no proposals: %v", proposal.Notes)
	}
	for _, proposed := range proposal.Proposals {
		if numberKnob(t, proposed, "vllm.max_num_seqs") > 128 {
			t.Fatalf("%v is above the advisor's C_kv of 100 for max_num_seqs (the leader's 128 is already past it)", proposed.Knobs)
		}
	}
	// The same bound steers the advisor step: it refuses, and says so.
	if !slices.ContainsFunc(proposal.Notes, func(note string) bool { return strings.Contains(note, "V1") && strings.Contains(note, "edge") }) {
		t.Fatalf("notes %v", proposal.Notes)
	}
}

const feasibilityMaxModelLenSpace = `
base: {vllm.enable_prefix_caching: true, vllm.gpu_memory_utilization: 0.75}
knobs:
  - {name: vllm.max_model_len, tier: feasibility, values: [8192, 6144, 4096], default: 8192}
  - {name: vllm.max_num_seqs, tier: scheduler, min: 32, max: 512}
`

// maxModelLenEvidence is a leader at max_model_len 8192 with V3 firing, and a
// run at 4096 that did not beat it.
func maxModelLenEvidence(t *testing.T) []Evidence {
	t.Helper()
	leader, other := loadBundles(t, "seqs128"), loadBundles(t, "seqs256")
	leader[0].Run.Spec.Knobs["vllm.max_model_len"] = 8192.0
	leader[0].Advice = []Advice{{Rule: "V3", Knob: "vllm.max_model_len", Recommended: ptr(5000.0)}}
	other[0].Run.Spec.Knobs["vllm.max_model_len"] = 4096.0
	other[0].Run.Spec.Knobs["vllm.max_num_seqs"] = 128.0
	return append(leader, other...)
}

func maxModelLenBoots(t *testing.T, sixThousandBoots bool) BootResults {
	t.Helper()
	probe := func(length int, outcome string) string {
		return `{"knobs": {"vllm.enable_prefix_caching": true, "vllm.gpu_memory_utilization": 0.75, "vllm.max_model_len": ` +
			formatValue(float64(length)) + `}, "outcome": "` + outcome + `", "reason": "no room for the KV cache"}`
	}
	sixThousand := "infeasible"
	if sixThousandBoots {
		sixThousand = "feasible"
	}
	return readBoots(t, `{"results": [`+probe(8192, "feasible")+","+probe(6144, sixThousand)+","+probe(4096, "feasible")+`]}`)
}

func TestV3MoveOnAFeasibilityKnobIsNeverDroppedSilently(t *testing.T) {
	space := parseSpace(t, feasibilityMaxModelLenSpace)
	// 6144 did not boot: V3's move is not viable, and the output says why.
	proposal := mustPropose(t, space, maxModelLenBoots(t, false), maxModelLenEvidence(t))
	if !slices.ContainsFunc(proposal.Notes, func(note string) bool {
		return strings.Contains(note, "V3") && strings.Contains(note, "6144") && strings.Contains(note, "boot probe")
	}) {
		t.Fatalf("notes %v, proposals %+v", proposal.Notes, proposal.Proposals)
	}
	// 6144 booted: it is a surviving value, so the structural tier screens it.
	proposal = mustPropose(t, space, maxModelLenBoots(t, true), maxModelLenEvidence(t))
	if proposal.Phase != PhaseScreen || len(proposal.Proposals) != 1 || proposal.Proposals[0].Knobs["vllm.max_model_len"] != 6144.0 {
		t.Fatalf("phase %s, proposals %+v", proposal.Phase, proposal.Proposals)
	}
}

func TestAFeasibilityKnobWithoutValuesIsRejected(t *testing.T) {
	_, err := ParseSearchSpace([]byte(`knobs: [{name: vllm.max_model_len, tier: feasibility, min: 4096, max: 16384}]`))
	if err == nil || !strings.Contains(err.Error(), "feasibility knob needs values") {
		t.Fatalf("err %v", err)
	}
}

func TestIntegerKnobsMustBeWholeNumbers(t *testing.T) {
	for _, knob := range []string{
		`{name: vllm.max_num_seqs, tier: scheduler, min: 32, max: 512, default: 64.5}`,
		`{name: vllm.max_num_batched_tokens, tier: scheduler, values: [2048.5, 4096]}`,
		`{name: vllm.max_num_seqs, tier: scheduler, min: 32.5, max: 512}`,
		`{name: vllm.tensor_parallel_size, tier: structural, values: [1, 1.5]}`,
		`{name: vllm.stream_interval, tier: fine_tuning, values: [2.5]}`,
		`{name: vllm.max_num_scheduled_tokens, tier: fine_tuning, values: [2048.5]}`, // a catalog knob
	} {
		if _, err := ParseSearchSpace([]byte("knobs: [" + knob + "]")); err == nil || !strings.Contains(err.Error(), "whole number") {
			t.Errorf("%s: err %v", knob, err)
		}
	}
	// A fractional value is fine for a fractional knob.
	parseSpace(t, `knobs: [{name: vllm.gpu_memory_utilization, tier: scheduler, values: [0.8, 0.9]}]`)
}

func TestAContradictedConfirmationRunDropsTheConfigurationInsteadOfLoopingForever(t *testing.T) {
	evidence := allConfirmedEvidence(t)
	captured := "64"
	for i := range evidence {
		if evidence[i].Run.ID == "01M80CJ54CMCEWYJES6JV5F8K3" { // confirm-seqs128-r1
			evidence[i].Run.Analysis.Quality.Knobs = []spec.KnobCheck{{Name: "vllm.max_num_seqs", Declared: "128", Actual: &captured, Status: spec.KnobStatusMismatch}}
		}
	}
	proposal := mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, evidence)
	// 128 is out; the next best, 64, is confirmed in its place. 128 is never asked for again.
	for _, proposed := range proposal.Proposals {
		if numberKnob(t, proposed, "vllm.max_num_seqs") == 128 {
			t.Fatalf("the contradicted configuration was asked for again: %+v", proposed)
		}
	}
	if !slices.ContainsFunc(proposal.Notes, func(note string) bool { return strings.Contains(note, "dropped from confirmation") }) {
		t.Fatalf("notes %v", proposal.Notes)
	}
	// Every configuration's confirmation contradicted: nothing to confirm, and the verdict says so.
	evidence = loadBundles(t, append([]string{"seqs128", "seqs256"}, append(confirmBundles("256", 3), confirmBundles("128", 3)...)...)...)
	for i := range evidence {
		if len(evidence[i].Run.Analysis.Stages) == 9 {
			evidence[i].Run.Analysis.Quality.Knobs = []spec.KnobCheck{{Name: "vllm.max_num_seqs", Declared: "x", Actual: &captured, Status: spec.KnobStatusMismatch}}
		}
	}
	proposal = mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, evidence)
	if proposal.Done == nil || proposal.Done.Outcome != OutcomeNoUsableRun || len(proposal.Proposals) != 0 {
		t.Fatalf("%+v %+v", proposal.Done, proposal.Proposals)
	}
}

func TestABaselineWhoseOnlyRunWasContradictedEndsWithAVerdict(t *testing.T) {
	evidence := loadBundles(t, "seqs64-probe")
	captured := "32"
	evidence[0].Run.Analysis.Quality.Knobs = []spec.KnobCheck{{Name: "vllm.max_num_seqs", Declared: "64", Actual: &captured, Status: spec.KnobStatusMismatch}}
	proposal := mustPropose(t, readSpace(t, "space.yaml"), BootResults{}, evidence)
	if proposal.Status != StatusDone || proposal.Done.Outcome != OutcomeNoUsableRun || !strings.Contains(proposal.Done.Reason, "baseline") {
		t.Fatalf("%s %+v", proposal.Status, proposal.Done)
	}
}

func TestVerdictReasonsNameConfigurationsByConfigID(t *testing.T) {
	proposal := mustPropose(t, readSpace(t, "space-seqs.yaml"), BootResults{}, allConfirmedEvidence(t))
	reason := proposal.Done.Reason
	if !strings.Contains(reason, proposal.Done.Winner.ConfigId) || strings.Contains(reason, "c1") || strings.Contains(reason, "c2") {
		t.Fatalf("reason %q", reason)
	}
}

// The RTX 3070 on vLLM 0.34: 0.75 and 0.95 boot with the
// same 5312 blocks, sized by free memory, so they are one configuration. The default
// 0.95 is the one dropped, and its twin 0.75 takes its place as the baseline.
func TestSameCapacityBootsAreOneConfiguration(t *testing.T) {
	space := parseSpace(t, `
base: {vllm.max_model_len: 4096}
knobs:
  - {name: vllm.gpu_memory_utilization, tier: feasibility, values: [0.6, 0.75, 0.95], default: 0.95}
  - {name: vllm.max_num_seqs, tier: scheduler, min: 16, max: 256, default: 32}
`)
	boots := readBoots(t, `
results:
  - {knobs: {vllm.max_model_len: 4096, vllm.gpu_memory_utilization: 0.6}, outcome: feasible, num_gpu_blocks: 3840, c_kv: 60,
     kv_sizing: {bound: utilization, kv_cache_gib: 2.81, requested_gib: 4.8}}
  - {knobs: {vllm.max_model_len: 4096, vllm.gpu_memory_utilization: 0.75}, outcome: feasible, num_gpu_blocks: 5312, c_kv: 83,
     kv_sizing: {bound: memory, kv_cache_gib: 3.89, requested_gib: 6.0}}
  - {knobs: {vllm.max_model_len: 4096, vllm.gpu_memory_utilization: 0.95}, outcome: feasible, num_gpu_blocks: 5312, c_kv: 83,
     kv_sizing: {bound: memory, kv_cache_gib: 3.89, requested_gib: 7.6}}
`)
	proposal := mustPropose(t, space, boots, nil)
	if proposal.Phase != PhaseBaseline || proposal.Proposals[0].Knobs["vllm.gpu_memory_utilization"] != 0.75 {
		t.Fatalf("phase %s, baseline %v: want 0.75, the kept twin of the default 0.95", proposal.Phase, proposal.Proposals[0].Knobs)
	}
	if got := *proposal.Tiers[0].Reason; !strings.Contains(got, "2 of 3") {
		t.Fatalf("feasibility reason %q: 0.6 and 0.75 survive", got)
	}
	if len(proposal.Notes) != 1 || !strings.Contains(proposal.Notes[0], "vllm.gpu_memory_utilization=0.95 booted with the same KV cache as vllm.gpu_memory_utilization=0.75 (5312 blocks, sized by the memory free after warmup)") ||
		!strings.Contains(proposal.Notes[0], "leave --gpu-memory-utilization out unless something else shares this GPU") {
		t.Fatalf("notes %q", proposal.Notes)
	}
	s, err := newState(space, boots, nil)
	if err != nil {
		t.Fatal(err)
	}
	dropped := Knobs{"vllm.max_model_len": 4096.0, "vllm.gpu_memory_utilization": 0.95, "vllm.max_num_seqs": 64.0}
	if reason, unviable := s.whyNotViable(dropped); !unviable || !strings.Contains(reason, "same configuration") {
		t.Fatalf("0.95 with other knobs: %q %v", reason, unviable)
	}
	if !s.viable(Knobs{"vllm.max_model_len": 4096.0, "vllm.gpu_memory_utilization": 0.6, "vllm.max_num_seqs": 64.0}) {
		t.Fatal("0.6 has its own capacity and stays a configuration")
	}
}

// Different block counts, or no block count, keep combinations apart.
func TestDifferentCapacityIsNotFolded(t *testing.T) {
	space := parseSpace(t, `
knobs:
  - {name: vllm.gpu_memory_utilization, tier: feasibility, values: [0.75, 0.95], default: 0.75}
`)
	boots := readBoots(t, `
results:
  - {knobs: {vllm.gpu_memory_utilization: 0.75}, outcome: feasible, num_gpu_blocks: 5312}
  - {knobs: {vllm.gpu_memory_utilization: 0.95}, outcome: feasible}
`)
	proposal := mustPropose(t, space, boots, nil)
	if len(proposal.Notes) != 0 || !strings.Contains(*proposal.Tiers[0].Reason, "2 of 2") {
		t.Fatalf("notes %q, tiers %+v", proposal.Notes, proposal.Tiers[0])
	}
}
