package propose

import (
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestSearchSpaceDefaults(t *testing.T) {
	space := parseSpace(t, `knobs: [{name: vllm.max_num_seqs, tier: scheduler, min: 1, max: 8}]`)
	if space.Objective != DefaultObjective || space.NoiseFloor != 0.05 || space.Confirm.Top != 3 || space.Confirm.Repeats != 3 {
		t.Fatalf("defaults %+v", space)
	}
}

func TestInvalidSearchSpaces(t *testing.T) {
	cases := map[string]struct{ document, want string }{
		"unknown field":      {`{"knobz": []}`, "knobz"},
		"bad tier":           {`knobs: [{name: vllm.max_num_seqs, tier: soon, values: [1]}]`, "tier"},
		"not a vllm knob":    {`knobs: [{name: bifrost.concurrency, tier: scheduler, values: [1]}]`, "not a vLLM or SGLang knob"},
		"bounds on a flag":   {`knobs: [{name: vllm.stream_interval, tier: fine_tuning, min: 1, max: 4}]`, "needs values"},
		"values and bounds":  {`knobs: [{name: vllm.max_num_seqs, tier: scheduler, values: [1], min: 1, max: 2}]`, "not both"},
		"neither":            {`knobs: [{name: vllm.max_num_seqs, tier: scheduler}]`, "values or bounds"},
		"inverted bounds":    {`knobs: [{name: vllm.max_num_seqs, tier: scheduler, min: 9, max: 2}]`, "min not above max"},
		"duplicate":          {`knobs: [{name: vllm.max_num_seqs, tier: scheduler, values: [1]}, {name: vllm.max_num_seqs, tier: scheduler, values: [2]}]`, "twice"},
		"base and tuned":     {"base: {vllm.max_num_seqs: 8}\nknobs: [{name: vllm.max_num_seqs, tier: scheduler, values: [1]}]", "both in base"},
		"too many confirmed": {"confirm: {top: 4}\nknobs: []", "/confirm/top"},
		"one repeat":         {"confirm: {repeats: 1}\nknobs: []", "/confirm/repeats"},
		"noise floor":        {"noise_floor: 1.5\nknobs: []", "noise_floor"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSearchSpace([]byte(c.document))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestInvalidBootResults(t *testing.T) {
	for _, document := range []string{`{"results": [{"knobs": {"a": 1}, "outcome": "maybe"}]}`, `{"results": [{"outcome": "feasible"}]}`, `{"result": []}`, `{"results": [{"knobs": {}, "outcome": "feasible"}]}`} {
		if _, err := ParseBootResults([]byte(document)); err == nil {
			t.Errorf("%s was accepted", document)
		}
	}
}

func TestStepAmongListedValuesAndBounds(t *testing.T) {
	listed := KnobSpec{spec.TuningKnobSpec{Name: "k", Values: []KnobValue{2048.0, 4096.0, 8192.0, 16384.0}}}
	bounded := func(low, high float64) KnobSpec {
		return KnobSpec{spec.TuningKnobSpec{Name: "k", Min: &low, Max: &high}}
	}
	number := func(v float64) *float64 { return &v }
	cases := []struct {
		name            string
		knob            KnobSpec
		current         float64
		direction       int
		target, ceiling *float64
		want            float64
		ok              bool
	}{
		{"listed up, adjacent", listed, 4096, +1, nil, nil, 8192, true},
		{"listed down, adjacent", listed, 4096, -1, nil, nil, 2048, true},
		{"listed up to a target takes the nearest value not past it", listed, 2048, +1, number(9000), nil, 8192, true},
		{"listed up, ceiling", listed, 2048, +1, nil, number(3000), 0, false},
		{"listed at the end", listed, 16384, +1, nil, nil, 0, false},
		{"listed down to a target takes the nearest value not below it", listed, 16384, -1, number(5000), nil, 8192, true},
		{"bounded up doubles", bounded(32, 512), 64, +1, nil, nil, 128, true},
		{"bounded down halves", bounded(32, 512), 64, -1, nil, nil, 32, true},
		{"bounded clamps to max", bounded(32, 100), 64, +1, nil, nil, 100, true},
		{"bounded at the max", bounded(32, 512), 512, +1, nil, nil, 0, false},
		{"bounded ceiling is floored", bounded(32, 512), 64, +1, nil, number(100.9), 100, true},
		{"bounded below the ceiling is a stop", bounded(32, 512), 128, +1, nil, number(100), 0, false},
		{"bounded to a target", bounded(1024, 8192), 8192, -1, number(1536), nil, 1536, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.knob.step(c.current, c.direction, c.target, c.ceiling)
			if ok != c.ok || got != c.want {
				t.Fatalf("got %g, %v; want %g, %v", got, ok, c.want, c.ok)
			}
		})
	}
}
