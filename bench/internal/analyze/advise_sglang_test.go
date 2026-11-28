package analyze

import (
	"slices"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/advisors/sglang"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

func sglangRun(targetType spec.TargetType) (input.Run, spec.AnalysisResult, []input.Stage, *sizing.Outputs) {
	start := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	gauge := func(values ...float64) []input.Series {
		one := input.Series{Labels: map[string]string{"pod": "sglang-0", "tp_rank": "0"}}
		for i, value := range values {
			one.Samples = append(one.Samples, input.Sample{At: at(10 * i), Value: value})
		}
		return []input.Series{one}
	}
	ref := func(name string) input.SeriesRef { return input.SeriesRef{Catalog: spec.CatalogNameSglang, Name: name} }
	run := input.Run{
		Spec: spec.RunSpec{Target: spec.Target{Type: targetType}},
		Matrix: input.MapMatrix{
			ref("num_running_reqs"): gauge(30, 60, 64),
			ref("num_queue_reqs"):   gauge(0, 5, 20),
			ref("token_usage"):      gauge(0.6, 0.97, 0.98),
		},
		CapturedKnobs: map[string]spec.KnobValue{
			target.KnobSGLangMemFractionStatic: 0.7,
			target.KnobSGLangSchedulePolicy:    "fcfs",
			target.KnobSGLangDisableRadixCache: false,
		},
	}
	stages := []input.Stage{{Index: 0, Start: at(0), End: at(30)}}
	analysis := spec.AnalysisResult{
		Quality: spec.RunQuality{Stages: []spec.StageQuality{{IsValid: true}}},
		Stages:  []spec.StageAggregate{{AchievedRps: 12}},
	}
	operating := 0
	outputs := &sizing.Outputs{}
	outputs.Recommendations.OperatingPoint.StageIndex = &operating
	return run, analysis, stages, outputs
}

func TestSGLangAdvisorRunsOnlyForSGLangTargets(t *testing.T) {
	run, analysis, stages, outputs := sglangRun(spec.TargetTypeVllm)
	if err := adviseSGLang(run, analysis, stages, outputs); err != nil || len(outputs.Recommendations.Recommendations) != 0 {
		t.Fatalf("a vLLM run got SGLang advice: %v %v", outputs.Recommendations.Recommendations, err)
	}
	run, analysis, stages, outputs = sglangRun(spec.TargetTypeSglang)
	if err := adviseSGLang(run, analysis, stages, outputs); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, recommendation := range outputs.Recommendations.Recommendations {
		got = append(got, recommendation.Rule)
	}
	if !slices.Equal(got, []string{sglang.RuleKVBound}) {
		t.Errorf("rules = %v", got)
	}
}

func TestSGLangKnobsReadTheCapturedFlags(t *testing.T) {
	knobs := sglangKnobs(map[string]spec.KnobValue{
		target.KnobSGLangMemFractionStatic:  0.8,
		target.KnobSGLangMaxRunningRequests: 128.0,
		target.KnobSGLangSchedulePolicy:     "lpm",
		target.KnobSGLangDisableRadixCache:  true,
	})
	want := sglang.Knobs{MemFractionStatic: 0.8, MaxRunningRequests: 128, SchedulePolicy: "lpm", IsRadixCacheDisabled: true, IsRadixCacheCaptured: true}
	if knobs != want {
		t.Errorf("knobs = %+v", knobs)
	}
	if empty := sglangKnobs(nil); empty != (sglang.Knobs{}) {
		t.Errorf("no captured knobs = %+v", empty)
	}
}
