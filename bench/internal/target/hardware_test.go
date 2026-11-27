package target

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func podWithGPUs(count string) corev1.Pod {
	return corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse(count)}},
	}}}}
}

func TestHardwareProfileOfAGPUWorkload(t *testing.T) {
	workload := snapshotVLLM(t)
	profile := workload.HardwareProfile
	if profile == nil {
		t.Fatal("no hardware profile")
	}
	if profile.Accelerator != "NVIDIA-A10G" || *profile.NodeInstanceType != "g5.2xlarge" || *profile.CpuModel != "Test CPU" {
		t.Errorf("profile = %+v", profile)
	}
	if *profile.GpuCountPerPod != 1 || *profile.GpuMemoryBytes != 23028*bytesPerMiB || *profile.CpuCores != 8 {
		t.Errorf("counts: gpus %d memory %d cores %d", *profile.GpuCountPerPod, *profile.GpuMemoryBytes, *profile.CpuCores)
	}
	if *profile.DriverVersion != "550.54.15" || *profile.CudaVersion != "12.4" {
		t.Errorf("driver %s cuda %s", *profile.DriverVersion, *profile.CudaVersion)
	}
	if profile.HardwareProfileId == nil || *profile.HardwareProfileId == "" {
		t.Error("no hardware_profile_id")
	}
}

func TestHardwareProfileIDIsDeterministicAndSensitiveToHardware(t *testing.T) {
	nodes := []nodeObservation{{name: "n", labels: gpuNode("n").Labels, cpuCores: 8}}
	pods := []corev1.Pod{podWithGPUs("1")}
	first := buildHardwareProfile(pods, nodes)
	second := buildHardwareProfile(pods, nodes)
	if *first.HardwareProfileId != *second.HardwareProfileId {
		t.Errorf("ids differ for equal input: %s %s", *first.HardwareProfileId, *second.HardwareProfileId)
	}
	otherLabels := gpuNode("n").Labels
	otherLabels["nvidia.com/gpu.product"] = "NVIDIA-H100"
	other := buildHardwareProfile(pods, []nodeObservation{{name: "n", labels: otherLabels, cpuCores: 8}})
	if *first.HardwareProfileId == *other.HardwareProfileId {
		t.Error("a different GPU model gave the same id")
	}
	renamed := buildHardwareProfile(pods, []nodeObservation{{name: "another-name", labels: gpuNode("n").Labels, cpuCores: 8}})
	if *first.HardwareProfileId != *renamed.HardwareProfileId {
		t.Error("the node's name must not change the profile")
	}
}

func TestHardwareProfileWithoutAGPUIsNoneAndLocalWithoutAnInstanceType(t *testing.T) {
	nodes := []nodeObservation{{name: "n", labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-A10G"}, cpuCores: 4}}
	profile := buildHardwareProfile([]corev1.Pod{{}}, nodes)
	if profile.Accelerator != "none" || profile.GpuCountPerPod != nil {
		t.Errorf("a pod that asks for no GPU must be none: %+v", profile)
	}
	if *profile.NodeInstanceType != "local" {
		t.Errorf("instance type = %s", *profile.NodeInstanceType)
	}
}

func TestHardwareProfileWithAGPURequestAndNoLabelIsUnknown(t *testing.T) {
	profile := buildHardwareProfile([]corev1.Pod{podWithGPUs("2")}, []nodeObservation{{name: "n", labels: map[string]string{}}})
	if profile.Accelerator != "unknown" || *profile.GpuCountPerPod != 2 {
		t.Errorf("profile = %+v", profile)
	}
}

func TestHardwareProfileOnMixedNodesJoinsModelsAndDropsDisagreeingNumbers(t *testing.T) {
	nodes := []nodeObservation{
		{name: "a", labels: map[string]string{"nvidia.com/gpu.product": "A10G"}, cpuCores: 8},
		{name: "b", labels: map[string]string{"nvidia.com/gpu.product": "T4"}, cpuCores: 16},
	}
	profile := buildHardwareProfile([]corev1.Pod{podWithGPUs("1")}, nodes)
	if profile.Accelerator != "A10G+T4" || profile.CpuCores != nil {
		t.Errorf("profile = %+v", profile)
	}
}

func TestHardwareProfileNeedsNodes(t *testing.T) {
	if buildHardwareProfile([]corev1.Pod{podWithGPUs("1")}, nil) != nil {
		t.Error("no nodes must give no profile")
	}
}
