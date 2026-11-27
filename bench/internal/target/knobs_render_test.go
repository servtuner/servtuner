package target

import (
	"reflect"
	"strings"
	"testing"
)

// representativeValue is a value of the kind's Go type that renders and
// captures back exactly. Sizes avoid a suffix (render writes plain integers).
func representativeValue(kind knobKind) []any {
	switch kind {
	case knobBoolean:
		return []any{true, false}
	case knobSwitch:
		return []any{true}
	case knobNumber:
		return []any{256.0, 0.9}
	case knobInteger:
		return []any{256.0}
	case knobSize:
		return []any{8192.0}
	default:
		return []any{"FULL_DECODE_ONLY"}
	}
}

func assertRoundTrip(t *testing.T, knob string, value any) {
	t.Helper()
	want := Knobs{knob: value}
	args, err := RenderVLLMArgs(want)
	if err != nil {
		t.Fatalf("%s=%v: render: %v", knob, value, err)
	}
	if got := vllmKnobsFromArgs(args); !reflect.DeepEqual(got, want) {
		t.Errorf("%s=%v: rendered %v, captured %v", knob, value, args, got)
	}
}

// Every table entry renders and captures back. Iterating the tables covers a
// knob added later without editing this test.
func TestRenderVLLMArgsRoundTripsEveryKnob(t *testing.T) {
	for _, flag := range vllmFlags {
		for _, value := range representativeValue(flag.kind) {
			assertRoundTrip(t, flag.knob, value)
		}
	}
	for _, field := range compilationConfigKnobs {
		for _, value := range representativeValue(field.kind) {
			assertRoundTrip(t, field.knob, value)
		}
	}
}

func TestRenderVLLMArgsRoundTripsAllKnobsTogether(t *testing.T) {
	want := Knobs{}
	for _, flag := range vllmFlags {
		want[flag.knob] = representativeValue(flag.kind)[0]
	}
	for _, field := range compilationConfigKnobs {
		want[field.knob] = representativeValue(field.kind)[0]
	}
	args, err := RenderVLLMArgs(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := vllmKnobsFromArgs(args); !reflect.DeepEqual(got, want) {
		t.Errorf("captured %v, want %v", got, want)
	}
}

func TestRenderVLLMArgsSpellings(t *testing.T) {
	args, err := RenderVLLMArgs(Knobs{
		KnobVLLMMaxNumSeqs:             256.0,
		KnobVLLMGPUMemoryUtil:          0.9,
		KnobVLLMMaxNumBatchedTokens:    8192.0,
		KnobVLLMKVCacheMemoryBytes:     1e9,
		KnobVLLMDtype:                  "bfloat16",
		KnobVLLMEnablePrefixCaching:    false,
		KnobVLLMEnforceEager:           false,
		KnobVLLMEnableLogRequests:      true,
		KnobVLLMDisableCustomAllReduce: false,
		KnobVLLMCudagraphMode:          "FULL",
		KnobVLLMMaxCudagraphCapture:    512.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Table order, long spellings, the paired or --no- form for false.
	want := []string{
		"--max-num-seqs=256",
		"--max-num-batched-tokens=8192",
		"--gpu-memory-utilization=0.9",
		"--dtype=bfloat16",
		"--disable-prefix-caching",
		"--no-enforce-eager",
		"--kv-cache-memory-bytes=1000000000",
		"--enable-custom-all-reduce",
		"--max-cudagraph-capture-size=512",
		"--enable-log-requests",
		"--compilation-config.cudagraph_mode=FULL",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v\nwant   %v", args, want)
	}
	for _, got := range args {
		if strings.Contains(got, "e+") || strings.Contains(got, ".0") {
			t.Errorf("%q is not a plain number", got)
		}
	}
}

// vLLM rejects --max-cudagraph-capture-size together with the config field, so
// the size is rendered through its flag only.
func TestRenderVLLMArgsNeverConflictsOnCudagraphSize(t *testing.T) {
	args, err := RenderVLLMArgs(Knobs{KnobVLLMMaxCudagraphCapture: 256.0, KnobVLLMCudagraphMode: "FULL"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--max-cudagraph-capture-size=256", "--compilation-config.cudagraph_mode=FULL"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestRenderVLLMArgsIsDeterministic(t *testing.T) {
	knobs := Knobs{}
	for _, flag := range vllmFlags {
		knobs[flag.knob] = representativeValue(flag.kind)[0]
	}
	first, err := RenderVLLMArgs(knobs)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, _ := RenderVLLMArgs(knobs)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("order changed: %v vs %v", first, again)
		}
	}
	if len(first) == 0 || first[0] != "--max-num-seqs=256" {
		t.Errorf("table order expected, got %v", first)
	}
}

func TestRenderVLLMArgsErrorsNameTheKnob(t *testing.T) {
	cases := []struct {
		name  string
		knobs Knobs
		knob  string
	}{
		{"non-vLLM knob", Knobs{KnobBifrostConcurrency: 10.0}, KnobBifrostConcurrency},
		{"unknown knob", Knobs{"vllm.no_such_knob": 1.0}, "vllm.no_such_knob"},
		{"number given text", Knobs{KnobVLLMMaxNumSeqs: "lots"}, KnobVLLMMaxNumSeqs},
		{"boolean given number", Knobs{KnobVLLMEnforceEager: 1.0}, KnobVLLMEnforceEager},
		{"text given boolean", Knobs{KnobVLLMDtype: true}, KnobVLLMDtype},
		{"fractional size", Knobs{KnobVLLMMaxModelLen: 10.5}, KnobVLLMMaxModelLen},
		{"empty text", Knobs{KnobVLLMDtype: ""}, KnobVLLMDtype},
		{"false store_true flag", Knobs{KnobVLLMDisableLogStats: false}, KnobVLLMDisableLogStats},
	}
	for _, test := range cases {
		args, err := RenderVLLMArgs(test.knobs)
		if err == nil || !strings.Contains(err.Error(), `"`+test.knob+`"`) {
			t.Errorf("%s: err = %v, want one naming %s", test.name, err, test.knob)
		}
		if args != nil {
			t.Errorf("%s: args = %v, want none on error", test.name, args)
		}
	}
	_, err := RenderVLLMArgs(Knobs{KnobBifrostConcurrency: 1.0, "vllm.zzz": 1.0})
	if err == nil || !strings.Contains(err.Error(), KnobBifrostConcurrency) || !strings.Contains(err.Error(), "vllm.zzz") {
		t.Errorf("every bad knob is reported: %v", err)
	}
}

func TestRenderVLLMArgsNamesTheFlagsServtunerSets(t *testing.T) {
	_, err := RenderVLLMArgs(Knobs{"vllm.port": 9.0, "vllm.ssl_certfile": "x"})
	if err == nil {
		t.Fatal("rendered flags servtuner sets itself")
	}
	for _, want := range []string{"servtuner sets --port itself", "servtuner sets --ssl-certfile itself"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
