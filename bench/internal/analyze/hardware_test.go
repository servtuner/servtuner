package analyze

import (
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// A target outside Kubernetes has no snapshot profile: its declared machine scopes
// the results, so it can be compared with a run on the same GPUs elsewhere.
func TestDeclaredHardwareScopesARunWithNoCapturedProfile(t *testing.T) {
	declared := &spec.DeclaredHardware{Host: "gpu-box", Accelerator: "NVIDIA GeForce RTX 3070", GpuCount: 1}
	run := input.Run{Spec: spec.RunSpec{Target: spec.Target{Hardware: declared}}}
	profile, source := hardwareProfile(run)
	if profile == nil || source != spec.AnalysisResultHardwareSourceDeclared {
		t.Fatalf("profile %+v source %q", profile, source)
	}
	if profile.Accelerator != declared.Accelerator || *profile.GpuCountPerPod != 1 || *profile.NodeInstanceType != "gpu-box" || profile.HardwareProfileId == nil {
		t.Errorf("profile %+v", profile)
	}

	id, gpus := "hw-captured", 2
	run.PreSnapshot = &spec.TargetSnapshot{Workloads: []spec.Workload_2{{
		Role:            spec.WorkloadRoleTarget,
		HardwareProfile: &spec.HardwareProfile{HardwareProfileId: &id, Accelerator: "NVIDIA-A100-SXM4-80GB", GpuCountPerPod: &gpus},
	}}}
	profile, source = hardwareProfile(run)
	if source != spec.AnalysisResultHardwareSourceCaptured || *profile.HardwareProfileId != id {
		t.Errorf("a captured profile wins: %+v %q", profile, source)
	}

	if profile, _ := hardwareProfile(input.Run{}); profile != nil {
		t.Errorf("no snapshot and no declaration: %+v", profile)
	}
}
