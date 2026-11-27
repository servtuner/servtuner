package pipeline

import (
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestAnOffClusterTargetIsSelectedByItsMetricsLabels(t *testing.T) {
	r := &run{
		opts:    Options{Getenv: env(map[string]string{"POD_NAMESPACE": "servtuner", "POD_NAME": "bench-01abc"})},
		runSpec: spec.RunSpec{Target: spec.Target{Metrics: &spec.TargetMetrics{Job: prometheus.ExternalJob, Namespace: "servtuner-external", Pod: "vllm-lan"}}},
	}
	selectors, err := r.selectors()
	if err != nil {
		t.Fatal(err)
	}
	if selectors.Job != prometheus.ExternalJob {
		t.Errorf("job %q, want %q", selectors.Job, prometheus.ExternalJob)
	}
	// The bench pod stays selected: client_bound needs its CPU.
	if selectors.NamespaceRegex != "servtuner|servtuner-external" || selectors.PodRegex != "bench-01abc|vllm-lan" {
		t.Errorf("namespaces %q, pods %q", selectors.NamespaceRegex, selectors.PodRegex)
	}
}

func TestAKubernetesTargetKeepsTheTargetsJob(t *testing.T) {
	node := "kind-control-plane"
	r := &run{
		opts:        Options{Getenv: env(nil)},
		runSpec:     spec.RunSpec{Target: spec.Target{Workload: &spec.WorkloadSelector{Namespace: "llm"}}},
		preSnapshot: &spec.TargetSnapshot{Workloads: []spec.Workload_2{{Pods: []spec.Pod{{Name: "vllm-0", Node: &node}}}}},
	}
	selectors, err := r.selectors()
	if err != nil {
		t.Fatal(err)
	}
	if selectors.Job != "" || selectors.PodRegex != "vllm-0" {
		t.Errorf("job %q (empty means servtuner-targets), pods %q", selectors.Job, selectors.PodRegex)
	}
}

func TestNoSnapshotAndNoMetricsSelectsNothing(t *testing.T) {
	r := &run{opts: Options{Getenv: env(nil)}}
	if _, err := r.selectors(); err == nil || !strings.Contains(err.Error(), "target.metrics") {
		t.Errorf("the error should point at target.metrics: %v", err)
	}
}

// TestAnUnknownTargetStillSelectsBenchsOwnPod: a target outside the cluster with no target.metrics
// (an external API, the starter's mock) has no pods to select, but bench's CPU is still worth
// collecting: client_bound reads it. Before, the whole collection was skipped.
func TestAnUnknownTargetStillSelectsBenchsOwnPod(t *testing.T) {
	r := &run{opts: Options{Getenv: env(map[string]string{"POD_NAMESPACE": "servtuner", "POD_NAME": "bench-01abc"})}}
	selectors, err := r.selectors()
	if err != nil {
		t.Fatal(err)
	}
	if selectors.NamespaceRegex != "servtuner" || selectors.PodRegex != "bench-01abc" {
		t.Errorf("namespaces %q, pods %q", selectors.NamespaceRegex, selectors.PodRegex)
	}
}
