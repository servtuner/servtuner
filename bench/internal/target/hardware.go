package target

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"

	corev1 "k8s.io/api/core/v1"
)

// Node labels the hardware profile is read from. They are the labels that GPU
// Feature Discovery, the cloud providers and Kubernetes itself set; a label
// that is absent leaves its profile field out.
const (
	labelInstanceType        = "node.kubernetes.io/instance-type"
	labelInstanceTypeBeta    = "beta.kubernetes.io/instance-type"
	labelNvidiaGPUProduct    = "nvidia.com/gpu.product"
	labelEKSAccelerator      = "k8s.amazonaws.com/accelerator"
	labelGKEAccelerator      = "cloud.google.com/gke-accelerator"
	labelNvidiaGPUMemoryMiB  = "nvidia.com/gpu.memory"
	labelNvidiaDriverVersion = "nvidia.com/cuda.driver-version.full"
	labelNvidiaCUDAVersion   = "nvidia.com/cuda.runtime-version.full"
	labelCPUModel            = "feature.node.kubernetes.io/cpu-model.name"
	// LabelServtunerCPUModel lets an operator name the CPU model on nodes that
	// Node Feature Discovery does not label.
	LabelServtunerCPUModel = "servtuner.dev/cpu-model"

	resourceNvidiaGPU = "nvidia.com/gpu"

	// acceleratorNone is the profile's accelerator for a workload that requests
	// no GPU; acceleratorUnknown is one that requests a GPU on nodes that carry
	// no label naming it.
	acceleratorNone    = "none"
	acceleratorUnknown = "unknown"

	instanceTypeLocal = "local"

	bytesPerMiB = 1024 * 1024
)

var acceleratorLabels = []string{labelNvidiaGPUProduct, labelEKSAccelerator, labelGKEAccelerator}

// nodeObservation is what the hardware profile needs from a node.
type nodeObservation struct {
	name     string
	labels   map[string]string
	cpuCores int
}

// buildHardwareProfile derives the profile of the nodes the pods run on. It
// returns nil when there are no nodes, or when neither an instance type nor a
// CPU model is known: the schema then has nothing to scope results to.
func buildHardwareProfile(pods []corev1.Pod, nodes []nodeObservation) *spec.HardwareProfile {
	if len(nodes) == 0 {
		return nil
	}
	gpuCount := gpusPerPod(pods)
	profile := &spec.HardwareProfile{Accelerator: acceleratorNone}
	if gpuCount > 0 {
		profile.Accelerator = valueAcrossNodes(nodes, acceleratorUnknown, func(node nodeObservation) string {
			return firstLabel(node.labels, acceleratorLabels...)
		})
		profile.GpuCountPerPod = &gpuCount
		profile.GpuMemoryBytes = integerAcrossNodes(nodes, func(node nodeObservation) int {
			mebibytes, _ := strconv.Atoi(node.labels[labelNvidiaGPUMemoryMiB])
			return mebibytes * bytesPerMiB
		})
		profile.DriverVersion = optionalAcrossNodes(nodes, labelNvidiaDriverVersion)
		profile.CudaVersion = optionalAcrossNodes(nodes, labelNvidiaCUDAVersion)
	}
	instanceType := valueAcrossNodes(nodes, instanceTypeLocal, func(node nodeObservation) string {
		return firstLabel(node.labels, labelInstanceType, labelInstanceTypeBeta)
	})
	profile.NodeInstanceType = &instanceType
	profile.CpuModel = optionalAcrossNodes(nodes, labelCPUModel, LabelServtunerCPUModel)
	profile.CpuCores = integerAcrossNodes(nodes, func(node nodeObservation) int { return node.cpuCores })

	hardwareProfileID := hardwareProfileID(*profile)
	profile.HardwareProfileId = &hardwareProfileID
	return profile
}

// gpusPerPod is the most GPUs any pod asks for. A limit wins over a request,
// because Kubernetes requires the two to be equal for extended resources.
func gpusPerPod(pods []corev1.Pod) int {
	most := 0
	for _, pod := range pods {
		total := 0
		for _, container := range pod.Spec.Containers {
			quantity, ok := container.Resources.Limits[resourceNvidiaGPU]
			if !ok {
				quantity = container.Resources.Requests[resourceNvidiaGPU]
			}
			total += int(quantity.Value())
		}
		most = max(most, total)
	}
	return most
}

func firstLabel(nodeLabels map[string]string, names ...string) string {
	for _, name := range names {
		if value := nodeLabels[name]; value != "" {
			return value
		}
	}
	return ""
}

// valueAcrossNodes joins the distinct non-empty values nodes report, in order,
// with "+", so a pod set spread over two GPU models gets a profile of its own.
// It returns fallback when no node reports one.
func valueAcrossNodes(nodes []nodeObservation, fallback string, read func(nodeObservation) string) string {
	var distinct []string
	seen := map[string]bool{}
	for _, node := range nodes {
		if value := read(node); value != "" && !seen[value] {
			seen[value] = true
			distinct = append(distinct, value)
		}
	}
	if len(distinct) == 0 {
		return fallback
	}
	return strings.Join(distinct, "+")
}

func optionalAcrossNodes(nodes []nodeObservation, labelNames ...string) *string {
	value := valueAcrossNodes(nodes, "", func(node nodeObservation) string { return firstLabel(node.labels, labelNames...) })
	if value == "" {
		return nil
	}
	return &value
}

// integerAcrossNodes returns the value every node agrees on, or nil when one is
// unknown (zero) or the nodes differ.
func integerAcrossNodes(nodes []nodeObservation, read func(nodeObservation) int) *int {
	agreed := read(nodes[0])
	for _, node := range nodes[1:] {
		if read(node) != agreed {
			return nil
		}
	}
	if agreed == 0 {
		return nil
	}
	return &agreed
}

// hardwareProfileID hashes the profile without its own id (schema
// hardwareProfile). Go marshals struct fields in declaration order, so the
// same profile always gives the same id.
func hardwareProfileID(profile spec.HardwareProfile) string {
	profile.HardwareProfileId = nil
	encoded, err := json.Marshal(profile)
	if err != nil {
		// A struct of strings and integers always marshals.
		panic("marshal hardware profile: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return "hw-" + hex.EncodeToString(digest[:8])
}
