package target

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

const (
	testNamespace = "inference"
	testDigest    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func int32Pointer(value int32) *int32 { return &value }

func vllmPod(name, nodeName string, args []string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: map[string]string{"app": "vllm"}},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{
				Name:  "server",
				Image: "example.invalid/vllm:test",
				Args:  args,
				Env: []corev1.EnvVar{
					{Name: "VLLM_LOGGING_LEVEL", Value: "INFO"},
					{Name: "HF_TOKEN", Value: "sentinel-secret-env-token"},
					{Name: "OPENAI_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}, Key: "k"}}},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")},
					Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("16Gi")},
				},
			}},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "server",
			ImageID:      "docker-pullable://example.invalid/vllm@" + testDigest,
			RestartCount: 2,
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
			},
		}}},
	}
}

func gpuNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"node.kubernetes.io/instance-type":          "g5.2xlarge",
			"nvidia.com/gpu.product":                    "NVIDIA-A10G",
			"nvidia.com/gpu.memory":                     "23028",
			"nvidia.com/cuda.driver-version.full":       "550.54.15",
			"nvidia.com/cuda.runtime-version.full":      "12.4",
			"feature.node.kubernetes.io/cpu-model.name": "Test CPU",
		}},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}},
	}
}

func vllmCluster() []runtime.Object {
	return []runtime.Object{
		vllmPod("vllm-b", "node-1", []string{"--max-num-seqs", "256", "--api-key", "sentinel-secret-api-key-arg"}),
		vllmPod("vllm-a", "node-1", []string{"--max-num-seqs=256", "--no-enable-prefix-caching"}),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: testNamespace, Labels: map[string]string{"app": "other"}}},
		gpuNode("node-1"),
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: testNamespace},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Pointer(2),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vllm", "extra": "x"}}},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "vllm-hpa", Namespace: testNamespace},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "vllm"},
				MinReplicas:    int32Pointer(1),
				MaxReplicas:    4,
				Metrics: []autoscalingv2.MetricSpec{{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name:   corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{AverageUtilization: int32Pointer(70)},
					},
				}},
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 2},
		},
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated-hpa", Namespace: testNamespace},
			Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "other"}, MaxReplicas: 9},
		},
	}
}

var vllmSelector = spec.WorkloadSelector{Namespace: testNamespace, Selector: spec.WorkloadSelectorSelector{"app": "vllm"}}

func snapshotVLLM(t *testing.T) spec.Workload_2 {
	t.Helper()
	workload, err := newVLLMAdapter().Snapshot(context.Background(), fake.NewSimpleClientset(vllmCluster()...), vllmSelector)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return workload
}

func TestSnapshotReadsPodsContainersReplicasHPAAndNodes(t *testing.T) {
	workload := snapshotVLLM(t)

	if workload.TargetType != spec.TargetTypeVllm {
		t.Errorf("target_type = %s", workload.TargetType)
	}
	if len(workload.Pods) != 2 || workload.Pods[0].Name != "vllm-a" || workload.Pods[1].Name != "vllm-b" {
		t.Fatalf("pods = %+v, want vllm-a and vllm-b in name order", workload.Pods)
	}
	container := workload.Pods[1].Containers[0]
	if container.Image != "example.invalid/vllm:test" || container.ImageDigest == nil || *container.ImageDigest != testDigest {
		t.Errorf("image = %s digest = %v", container.Image, container.ImageDigest)
	}
	if container.Requests["cpu"] != "4" || container.Requests["memory"] != "16Gi" || container.Limits["nvidia.com/gpu"] != "1" {
		t.Errorf("requests = %v limits = %v", container.Requests, container.Limits)
	}
	if container.RestartCount == nil || *container.RestartCount != 2 || container.LastTerminationReason == nil || *container.LastTerminationReason != "OOMKilled" {
		t.Errorf("restart_count = %v reason = %v", container.RestartCount, container.LastTerminationReason)
	}
	if workload.Replicas == nil || workload.Replicas.Desired != 2 || workload.Replicas.Ready != 1 {
		t.Errorf("replicas = %+v", workload.Replicas)
	}
	if workload.Hpa == nil || workload.Hpa.Name != "vllm-hpa" || workload.Hpa.MaxReplicas != 4 || workload.Hpa.CurrentReplicas != 2 {
		t.Fatalf("hpa = %+v", workload.Hpa)
	}
	if metric := workload.Hpa.Metrics[0]; metric.Type != "Resource" || metric.Name != "cpu" || metric.Target != "70%" {
		t.Errorf("hpa metric = %+v", metric)
	}
	if len(workload.Nodes) != 1 || workload.Nodes[0].Labels["nvidia.com/gpu.product"] != "NVIDIA-A10G" {
		t.Errorf("nodes = %+v", workload.Nodes)
	}
}

func TestSnapshotRedactsEnvAndArgsAndNothingSecretReachesTheDocument(t *testing.T) {
	workload := snapshotVLLM(t)
	encoded, err := json.Marshal(workload)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range sentinelSecrets {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("snapshot contains %q:\n%s", secret, encoded)
		}
	}
	env := map[string]EnvEntry{}
	for _, variable := range workload.Pods[1].Containers[0].Env {
		entry := variable.(EnvEntry)
		env[entry.Name] = entry
	}
	if entry := env["VLLM_LOGGING_LEVEL"]; entry.IsRedacted || entry.Value == nil || *entry.Value != "INFO" {
		t.Errorf("ordinary variable = %+v", entry)
	}
	if !env["HF_TOKEN"].IsRedacted || !env["OPENAI_KEY"].IsRedacted {
		t.Errorf("secret variables must be redacted: %+v", env)
	}
}

func TestSnapshotLeavesTheDocumentValidAgainstTheSchema(t *testing.T) {
	document, err := json.Marshal(spec.TargetSnapshot{
		Phase:      spec.SnapshotPhasePre,
		CapturedAt: "2026-10-29T12:00:00Z",
		Workloads: []spec.Workload_2{func() spec.Workload_2 {
			workload := snapshotVLLM(t)
			workload.Role = spec.WorkloadRoleTarget
			return workload
		}()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaTargetSnapshot, document); err != nil {
		t.Errorf("snapshot does not satisfy the schema: %v\n%s", err, document)
	}
}

func TestSnapshotOnlyReadsFromTheCluster(t *testing.T) {
	client := fake.NewSimpleClientset(vllmCluster()...)
	if _, err := newVLLMAdapter().Snapshot(context.Background(), client, vllmSelector); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if verb := action.GetVerb(); verb != "list" && verb != "get" {
			t.Errorf("snapshot performed %s on %s", verb, action.GetResource().Resource)
		}
	}
}

func TestSnapshotReturnsThePodsWhenAnotherReadFails(t *testing.T) {
	client := fake.NewSimpleClientset(vllmCluster()...)
	client.PrependReactor("list", "horizontalpodautoscalers", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	workload, err := newVLLMAdapter().Snapshot(context.Background(), client, vllmSelector)
	if err == nil || !strings.Contains(err.Error(), "horizontalpodautoscalers") {
		t.Fatalf("want an error naming the HPA read, got %v", err)
	}
	if len(workload.Pods) != 2 || workload.Replicas == nil {
		t.Errorf("partial workload lost data: %+v", workload)
	}
}

func TestSnapshotReportsAPodListFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	workload, err := newVLLMAdapter().Snapshot(context.Background(), client, vllmSelector)
	if err == nil || workload.TargetType != spec.TargetTypeVllm || workload.Pods == nil {
		t.Errorf("workload = %+v err = %v", workload, err)
	}
}

func TestSnapshotFallsBackToAStatefulSet(t *testing.T) {
	client := fake.NewSimpleClientset(
		vllmPod("vllm-0", "", nil),
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: testNamespace},
			Spec:       appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vllm"}}}},
			Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
		},
	)
	workload, err := newVLLMAdapter().Snapshot(context.Background(), client, vllmSelector)
	if err != nil {
		t.Fatal(err)
	}
	if workload.Replicas == nil || workload.Replicas.Desired != 1 || workload.Replicas.Ready != 1 {
		t.Errorf("replicas = %+v (an unset spec.replicas means 1)", workload.Replicas)
	}
	if workload.HardwareProfile != nil {
		t.Errorf("pods on no node have no hardware profile: %+v", workload.HardwareProfile)
	}
}

func bifrostPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bifrost-0", Namespace: testNamespace, Labels: map[string]string{"app": "bifrost"}},
		Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "bifrost", Image: "example.invalid/bifrost:test"}}},
	}
}
