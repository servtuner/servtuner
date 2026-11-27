package spec

import "testing"

func TestResolveAnalysisFillsEveryAbsentBlockFromTheSchema(t *testing.T) {
	resolved, err := ResolveAnalysis(nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Headroom == nil || resolved.Headroom.Request != 0.2 || resolved.Headroom.Limit != 0.3 {
		t.Errorf("headroom = %+v", resolved.Headroom)
	}
	if resolved.Limits == nil || resolved.Limits.Cpu != LimitsPolicyFollow || resolved.Limits.Memory != LimitsPolicySet {
		t.Errorf("limits = %+v (memory defaults to set: the OOM boundary)", resolved.Limits)
	}
	if resolved.BurstFactor != 1 || resolved.ExpectedPodLifetime != "24h" || resolved.StageValidity == nil {
		t.Errorf("analysis defaults = %+v", resolved)
	}
}

func TestResolveAnalysisKeepsWhatTheRunSpecDeclares(t *testing.T) {
	runSpec, err := ParseRunSpec([]byte(`{
	  "apiVersion": "servtuner.dev/v1", "kind": "RunSpec", "name": "declared",
	  "target": {"type": "vllm", "endpoint": "http://x", "model": "m", "tls": {"mode": "platform"}},
	  "engine": {"type": "loadgen"},
	  "workload": {"streaming": false, "dataset": {"type": "synthetic", "input_tokens": {"mean": 1}, "output_tokens": {"mean": 1}}},
	  "load": {"stages": [{"rate": 1, "duration": "1s"}]},
	  "analysis": {"headroom": {"request": 0}, "burst_factor": 2}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveAnalysis(runSpec.Analysis)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Headroom.Request != 0 || resolved.Headroom.Limit != 0.3 {
		t.Errorf("an explicit zero headroom must stay zero: %+v", resolved.Headroom)
	}
	if resolved.BurstFactor != 2 || resolved.Limits == nil || resolved.Limits.Memory != LimitsPolicySet {
		t.Errorf("resolved = %+v, limits %+v", resolved, resolved.Limits)
	}
}
