package propose

import (
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

const sglangSpace = `knobs:
  - {name: sglang.max_running_requests, tier: scheduler, min: 8, max: 512, default: 64}
  - {name: sglang.chunked_prefill_size, tier: scheduler, min: 1024, max: 16384, default: 4096}
  - {name: sglang.schedule_policy, tier: scheduler, values: [fcfs, lpm]}
`

func TestAnSGLangSpaceIsValidAndTunesSGLang(t *testing.T) {
	space := parseSpace(t, sglangSpace)
	if space.Server() != spec.KnobCatalogServerSglang {
		t.Errorf("server = %s", space.Server())
	}
	if parseSpace(t, `knobs: [{name: vllm.max_num_seqs, tier: scheduler, min: 1, max: 8}]`).Server() != spec.KnobCatalogServerVllm {
		t.Error("a vLLM space must tune vLLM")
	}
}

func TestASpaceTunesOneServer(t *testing.T) {
	for name, document := range map[string]string{
		"mixed knobs":     "knobs: [{name: vllm.max_num_seqs, tier: scheduler, values: [8]}, {name: sglang.max_running_requests, tier: scheduler, values: [8]}]",
		"mixed base":      "base: {vllm.max_model_len: 4096}\nknobs: [{name: sglang.max_running_requests, tier: scheduler, values: [8]}]",
		"half sequences":  "knobs: [{name: sglang.max_running_requests, tier: scheduler, values: [8.5]}]",
		"owned or secret": "knobs: [{name: sglang.api_key, tier: scheduler, values: [x]}]",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSearchSpace([]byte(document)); err == nil {
				t.Errorf("%s accepted", name)
			}
		})
	}
}

func TestSGLangsTunedKnobsTakeBounds(t *testing.T) {
	for _, knob := range []string{target.KnobSGLangMaxRunningRequests, target.KnobSGLangChunkedPrefillSize} {
		if !target.IsRangeTunedKnob(knob) || !target.IsWholeNumberKnob(knob) {
			t.Errorf("%s: range tuned %t, whole %t", knob, target.IsRangeTunedKnob(knob), target.IsWholeNumberKnob(knob))
		}
	}
	if target.IsWholeNumberKnob(target.KnobSGLangMemFractionStatic) {
		t.Error("mem_fraction_static is a fraction")
	}
}

// sglangState is a state with one screened leader whose run carried the given advice.
func sglangState(t *testing.T, advice []Advice, boots BootResults) *state {
	t.Helper()
	space := parseSpace(t, sglangSpace)
	leader := &configuration{
		id: "c-leader", knobs: Knobs{target.KnobSGLangMaxRunningRequests: 64.0, target.KnobSGLangChunkedPrefillSize: 4096.0},
		screening: []compare.Run{{ID: "run-1"}}, hasSLOMax: true, score: 10, sloMaxRPS: 10,
	}
	return &state{
		space: space, boots: boots, leader: leader, screened: []*configuration{leader},
		byKey: map[string]*configuration{leader.knobs.key(): leader}, advice: map[string][]Advice{"run-1": advice},
	}
}

func TestSGLangAdviceMovesTheRunningCap(t *testing.T) {
	cases := map[string]struct {
		advice Advice
		boots  BootResults
		want   float64
	}{
		"S3 raises it":          {Advice{Rule: "S3", Knob: target.KnobSGLangMaxRunningRequests}, BootResults{}, 128},
		"S3 within C_kv":        {Advice{Rule: "S3", Knob: target.KnobSGLangMaxRunningRequests}, readBootsValue(t, `{"results": [{"knobs": {"sglang.chunked_prefill_size": 4096}, "outcome": "feasible", "c_kv": 96}]}`), 96},
		"S2 lowers it":          {Advice{Rule: "S2", Knob: target.KnobSGLangMaxRunningRequests}, BootResults{}, 32},
		"a vLLM rule is no use": {Advice{Rule: "V1", Knob: target.KnobVLLMMaxNumSeqs}, BootResults{}, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := sglangState(t, []Advice{c.advice}, c.boots)
			b := newBatch(s)
			s.followAdvisors(b)
			if c.want == 0 {
				if len(b.proposals) != 0 {
					t.Fatalf("proposals %+v", b.proposals)
				}
				return
			}
			if len(b.proposals) != 1 {
				t.Fatalf("proposals %+v, notes %v", b.proposals, b.notes)
			}
			got, _ := Knobs(b.proposals[0].Knobs).number(target.KnobSGLangMaxRunningRequests)
			if got != c.want || !strings.Contains(b.proposals[0].Rationale.Text, c.advice.Rule) {
				t.Errorf("max_running_requests = %v, want %v (%s)", got, c.want, b.proposals[0].Rationale.Text)
			}
		})
	}
}

func TestSGLangsCoordinateStepMovesItsOwnKnobs(t *testing.T) {
	s := sglangState(t, nil, BootResults{})
	b := newBatch(s)
	s.coordinateStep(b)
	if len(b.proposals) != 1 {
		t.Fatalf("proposals %+v, notes %v", b.proposals, b.notes)
	}
	step := Knobs(b.proposals[0].Knobs)
	if running, _ := step.number(target.KnobSGLangMaxRunningRequests); running == 64 {
		if chunk, _ := step.number(target.KnobSGLangChunkedPrefillSize); chunk == 4096 {
			t.Errorf("the step moved nothing: %v", step)
		}
	}
	if cell := b.proposals[0].Rationale.GridCell; cell == nil || cell.MaxNumSeqs != PositionUp {
		t.Errorf("the running cap goes up first: %+v", cell)
	}
}

func readBootsValue(t *testing.T, document string) BootResults {
	t.Helper()
	return readBoots(t, document)
}
