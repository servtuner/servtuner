package analyzetest

import (
	"encoding/json"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Snapshot returns a vLLM target of two pods, both ready, one GPU per
// pod, and the flags --max-num-seqs 128 and --enable-prefix-caching.
func Snapshot(phase spec.SnapshotPhase) *spec.TargetSnapshot {
	restarts, gpus := 0, 1
	profile := "hardware-synthetic"
	instance := "example.large"
	return &spec.TargetSnapshot{
		Phase: phase, CapturedAt: spec.NewUTCTimestamp(StartTime),
		Workloads: []spec.Workload_2{{
			Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeVllm,
			Pods:     []spec.Pod{pod(TargetPods[0], &restarts), pod(TargetPods[1], &restarts)},
			Replicas: &spec.Replicas{Desired: 2, Ready: 2},

			HardwareProfile: &spec.HardwareProfile{HardwareProfileId: &profile, Accelerator: "example-gpu", GpuCountPerPod: &gpus, NodeInstanceType: &instance},
		}},
	}
}

func pod(name string, restarts *int) spec.Pod {
	count := *restarts
	return spec.Pod{Name: name, Containers: []spec.Container{{
		Name: "vllm", Image: "registry.invalid/vllm:v0.0.0",
		Args: []string{"--max-num-seqs", "128", "--enable-prefix-caching"},
		Env: []spec.EnvVar{
			map[string]any{"name": "LOG_LEVEL", "is_redacted": false, "value": "INFO"},
			map[string]any{"name": "HF_TOKEN", "is_redacted": true},
		},
		Requests: spec.ResourceQuantities{"cpu": "4"}, Limits: spec.ResourceQuantities{"memory": "32Gi"},
		RestartCount: &count,
	}}}
}

// Clone deep-copies a snapshot, so a scenario can change the post-run one
// without touching the pre-run one.
func Clone(snapshot *spec.TargetSnapshot) *spec.TargetSnapshot {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	var copied spec.TargetSnapshot
	if err := json.Unmarshal(encoded, &copied); err != nil {
		panic(err)
	}
	return &copied
}

// QueriesFor lists every rendered catalog entry of the scenario as a collected
// query, plus the given optional-catalog results.
func QueriesFor(s Scenario, optional ...spec.OptionalCatalogResult) *spec.PrometheusQueries {
	window := spec.TimeWindow{Start: spec.NewUTCTimestamp(StartTime), End: spec.NewUTCTimestamp(s.stageWindow(len(s.Stages) - 1).End)}
	queries := &spec.PrometheusQueries{Queries: []spec.Query{}, OptionalCatalogs: append([]spec.OptionalCatalogResult{}, optional...)}
	for _, entry := range s.EntryRefs() {
		queries.Queries = append(queries.Queries, spec.Query{
			Catalog: entry.Catalog, Name: spec.MetricName(entry.Name), Query: "synthetic_" + entry.Name,
			StepSeconds: seriesStep.Seconds(), Window: window, Result: spec.QueryResultCollected,
		})
	}
	return queries
}
