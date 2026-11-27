package target

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/servtuner/servtuner/bench/internal/spec"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// kubeReader is the Kubernetes half of every adapter: the same read-only
// calls for vllm, litellm, bifrost and openai-compatible. It only lists and
// gets (RBAC: pods, deployments, statefulsets, HPAs and nodes).
type kubeReader struct {
	targetType spec.TargetType
}

// Type implements TargetAdapter for every adapter that embeds the reader.
func (r kubeReader) Type() spec.TargetType { return r.targetType }

var imageDigestPattern = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// controller is the Deployment or StatefulSet that owns the selected pods.
type controller struct {
	kind            string
	name            string
	desiredReplicas int
	readyReplicas   int
}

// Snapshot implements TargetAdapter. Failures other than listing the pods
// leave the matching part of the workload empty and are returned together.
func (r kubeReader) Snapshot(ctx context.Context, kube kubernetes.Interface, selector spec.WorkloadSelector) (spec.Workload_2, error) {
	workload := spec.Workload_2{TargetType: r.targetType, Pods: []spec.Pod{}}
	labelSelector := labels.SelectorFromSet(labels.Set(selector.Selector))

	podList, err := kube.CoreV1().Pods(selector.Namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		return workload, fmt.Errorf("list pods in namespace %s with selector %s: %w", selector.Namespace, labelSelector, err)
	}
	sort.Slice(podList.Items, func(i, j int) bool { return podList.Items[i].Name < podList.Items[j].Name })
	for _, pod := range podList.Items {
		workload.Pods = append(workload.Pods, podFacts(pod))
	}

	var problems []error
	owner, err := findController(ctx, kube, selector, labelSelector)
	if err != nil {
		problems = append(problems, err)
	}
	if owner != nil {
		workload.Replicas = &spec.Replicas{Desired: owner.desiredReplicas, Ready: owner.readyReplicas}
		hpa, err := findHPA(ctx, kube, selector.Namespace, *owner)
		if err != nil {
			problems = append(problems, err)
		}
		workload.Hpa = hpa
	}

	observations, err := readNodes(ctx, kube, podList.Items)
	if err != nil {
		problems = append(problems, err)
	}
	for _, observation := range observations {
		workload.Nodes = append(workload.Nodes, spec.Node{Name: observation.name, Labels: observation.labels})
	}
	workload.HardwareProfile = buildHardwareProfile(podList.Items, observations)
	return workload, errors.Join(problems...)
}

func podFacts(pod corev1.Pod) spec.Pod {
	facts := spec.Pod{Name: pod.Name, Containers: []spec.Container{}}
	if pod.Spec.NodeName != "" {
		nodeName := pod.Spec.NodeName
		facts.Node = &nodeName
	}
	statuses := map[string]corev1.ContainerStatus{}
	for _, status := range pod.Status.ContainerStatuses {
		statuses[status.Name] = status
	}
	for _, container := range pod.Spec.Containers {
		facts.Containers = append(facts.Containers, containerFacts(container, statuses[container.Name]))
	}
	return facts
}

func containerFacts(container corev1.Container, status corev1.ContainerStatus) spec.Container {
	facts := spec.Container{
		Name:     container.Name,
		Image:    container.Image,
		Args:     RedactArgs(container.Args),
		Requests: quantities(container.Resources.Requests),
		Limits:   quantities(container.Resources.Limits),
	}
	for _, variable := range container.Env {
		facts.Env = append(facts.Env, RedactEnv(variable.Name, variable.Value, variable.ValueFrom != nil))
	}
	if digest := imageDigestPattern.FindString(status.ImageID); digest != "" {
		facts.ImageDigest = &digest
	}
	if status.Name != "" {
		restartCount := int(status.RestartCount)
		facts.RestartCount = &restartCount
	}
	if terminated := status.LastTerminationState.Terminated; terminated != nil && terminated.Reason != "" {
		reason := terminated.Reason
		facts.LastTerminationReason = &reason
	}
	return facts
}

func quantities(list corev1.ResourceList) spec.ResourceQuantities {
	if len(list) == 0 {
		return nil
	}
	converted := make(spec.ResourceQuantities, len(list))
	for name, quantity := range list {
		converted[string(name)] = quantity.String()
	}
	return converted
}

// findController returns the Deployment, else the StatefulSet, whose pod
// template carries every label of the selector. It returns nil when there is none.
func findController(ctx context.Context, kube kubernetes.Interface, selector spec.WorkloadSelector, labelSelector labels.Selector) (*controller, error) {
	deployments, err := kube.AppsV1().Deployments(selector.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments in namespace %s: %w", selector.Namespace, err)
	}
	sort.Slice(deployments.Items, func(i, j int) bool { return deployments.Items[i].Name < deployments.Items[j].Name })
	for _, deployment := range deployments.Items {
		if labelSelector.Matches(labels.Set(deployment.Spec.Template.Labels)) {
			return deploymentController(deployment), nil
		}
	}
	statefulSets, err := kube.AppsV1().StatefulSets(selector.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets in namespace %s: %w", selector.Namespace, err)
	}
	sort.Slice(statefulSets.Items, func(i, j int) bool { return statefulSets.Items[i].Name < statefulSets.Items[j].Name })
	for _, statefulSet := range statefulSets.Items {
		if labelSelector.Matches(labels.Set(statefulSet.Spec.Template.Labels)) {
			return statefulSetController(statefulSet), nil
		}
	}
	return nil, nil
}

func deploymentController(deployment appsv1.Deployment) *controller {
	return &controller{kind: "Deployment", name: deployment.Name, desiredReplicas: desiredReplicas(deployment.Spec.Replicas), readyReplicas: int(deployment.Status.ReadyReplicas)}
}

func statefulSetController(statefulSet appsv1.StatefulSet) *controller {
	return &controller{kind: "StatefulSet", name: statefulSet.Name, desiredReplicas: desiredReplicas(statefulSet.Spec.Replicas), readyReplicas: int(statefulSet.Status.ReadyReplicas)}
}

// desiredReplicas applies the Kubernetes default of one when spec.replicas is unset.
func desiredReplicas(replicas *int32) int {
	if replicas == nil {
		return 1
	}
	return int(*replicas)
}

func findHPA(ctx context.Context, kube kubernetes.Interface, namespace string, owner controller) (*spec.Hpa, error) {
	list, err := kube.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list horizontalpodautoscalers in namespace %s: %w", namespace, err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	for _, hpa := range list.Items {
		if hpa.Spec.ScaleTargetRef.Kind != owner.kind || hpa.Spec.ScaleTargetRef.Name != owner.name {
			continue
		}
		facts := &spec.Hpa{
			Name:            hpa.Name,
			MaxReplicas:     int(hpa.Spec.MaxReplicas),
			CurrentReplicas: int(hpa.Status.CurrentReplicas),
			MinReplicas:     1, // Kubernetes default when minReplicas is unset
		}
		if hpa.Spec.MinReplicas != nil {
			facts.MinReplicas = int(*hpa.Spec.MinReplicas)
		}
		for _, metric := range hpa.Spec.Metrics {
			facts.Metrics = append(facts.Metrics, hpaMetricFacts(metric))
		}
		return facts, nil
	}
	return nil, nil
}

func hpaMetricFacts(metric autoscalingv2.MetricSpec) spec.HpaMetric {
	facts := spec.HpaMetric{Type: string(metric.Type)}
	var name string
	var target autoscalingv2.MetricTarget
	switch metric.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if metric.Resource != nil {
			name, target = string(metric.Resource.Name), metric.Resource.Target
		}
	case autoscalingv2.ContainerResourceMetricSourceType:
		if metric.ContainerResource != nil {
			name, target = string(metric.ContainerResource.Name), metric.ContainerResource.Target
		}
	case autoscalingv2.PodsMetricSourceType:
		if metric.Pods != nil {
			name, target = metric.Pods.Metric.Name, metric.Pods.Target
		}
	case autoscalingv2.ObjectMetricSourceType:
		if metric.Object != nil {
			name, target = metric.Object.Metric.Name, metric.Object.Target
		}
	case autoscalingv2.ExternalMetricSourceType:
		if metric.External != nil {
			name, target = metric.External.Metric.Name, metric.External.Target
		}
	}
	facts.Name = name
	facts.Target = metricTargetText(target)
	return facts
}

// metricTargetText prints a target the way Kubernetes does: 70% for a
// utilization, the quantity otherwise.
func metricTargetText(target autoscalingv2.MetricTarget) string {
	switch {
	case target.AverageUtilization != nil:
		return fmt.Sprintf("%d%%", *target.AverageUtilization)
	case target.AverageValue != nil:
		return target.AverageValue.String()
	case target.Value != nil:
		return target.Value.String()
	default:
		return "unset"
	}
}

// readNodes gets each node the pods run on, once, in name order. A node that
// cannot be read is left out and reported.
func readNodes(ctx context.Context, kube kubernetes.Interface, pods []corev1.Pod) ([]nodeObservation, error) {
	names := map[string]bool{}
	for _, pod := range pods {
		if pod.Spec.NodeName != "" {
			names[pod.Spec.NodeName] = true
		}
	}
	var observations []nodeObservation
	var problems []error
	for _, name := range sortedKeys(names) {
		node, err := kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			problems = append(problems, fmt.Errorf("get node %s: %w", name, err))
			continue
		}
		observation := nodeObservation{name: node.Name, labels: node.Labels}
		if cores, ok := node.Status.Capacity[corev1.ResourceCPU]; ok {
			observation.cpuCores = int(cores.Value())
		}
		observations = append(observations, observation)
	}
	return observations, errors.Join(problems...)
}
