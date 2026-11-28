package target

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderSGLangArgsSpellsEveryKind(t *testing.T) {
	args, err := RenderSGLangArgs(Knobs{
		KnobSGLangMemFractionStatic:  0.82,
		KnobSGLangMaxRunningRequests: 128.0,
		KnobSGLangSchedulePolicy:     "lpm",
		KnobSGLangDisableRadixCache:  true,
		KnobSGLangMaxTotalTokens:     400000.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--disable-radix-cache", "--max-running-requests=128", "--max-total-tokens=400000", "--mem-fraction-static=0.82", "--schedule-policy=lpm"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v\nwant %v", args, want)
	}
}

// A false switch is SGLang's default: nothing to pass.
func TestRenderSGLangArgsOmitsAFalseSwitch(t *testing.T) {
	args, err := RenderSGLangArgs(Knobs{KnobSGLangDisableRadixCache: false})
	if err != nil || len(args) != 0 {
		t.Errorf("args %v, err %v", args, err)
	}
}

// The args capture back as the same knobs, so a deployed target verifies.
func TestRenderedSGLangArgsCaptureBack(t *testing.T) {
	knobs := Knobs{KnobSGLangTPSize: 2.0, KnobSGLangMemFractionStatic: 0.8, KnobSGLangSchedulePolicy: "lpm", KnobSGLangDisableRadixCache: true}
	args, err := RenderSGLangArgs(knobs)
	if err != nil {
		t.Fatal(err)
	}
	if got := sglangKnobsFromArgs(args); !reflect.DeepEqual(got, knobs) {
		t.Errorf("captured %v, rendered from %v", got, knobs)
	}
}

func TestRenderSGLangArgsRefusesWhatIsNotAKnob(t *testing.T) {
	_, err := RenderSGLangArgs(Knobs{
		"vllm.max_num_seqs":   8.0,
		"sglang.port":         30000.0,
		"sglang.api_key":      "x",
		"sglang.no_such_flag": 1.0,
		KnobSGLangTPSize:      1.5,
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"vllm.max_num_seqs\" is not an SGLang knob", "sglang.port\" is not a knob", "sglang.api_key\" is not a knob", "not a known SGLang knob", "takes a whole number"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}
