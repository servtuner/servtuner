package analyze_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/bundle"
)

// A host target has no Kubernetes workload to count replicas from: with its machine
// declared in target.hardware it is one replica holding the declared GPUs, so its SLO-max divides
// per replica and per GPU like a cluster run's.
func TestADeclaredHostIsOneReplicaWithItsGPUs(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("send-lag-high")
	scenario.Pre, scenario.Post = nil, nil
	dir := writeScenario(t, scenario)

	without, err := analyze.AnalyzeBundle(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if without.Capacity.SloMax == nil {
		t.Fatal("the scenario needs an SLO-max")
	}
	if without.Capacity.PerReplica != nil {
		t.Skipf("the scenario still counts replicas from its series: %+v", without.Capacity.PerReplica)
	}

	path := filepath.Join(dir, bundle.ResolvedSpecJSON)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["target"].(map[string]any)["hardware"] = map[string]any{
		"host": "gpu-box", "accelerator": "NVIDIA GeForce RTX 3070", "gpu_count": 2,
	}
	raw, _ = json.Marshal(document)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	with, err := analyze.AnalyzeBundle(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	per := with.Capacity.PerReplica
	if per == nil || per.ReadyReplicas != 1 || per.TotalGpus == nil || *per.TotalGpus != 2 {
		t.Fatalf("per replica = %+v", per)
	}
	if want := with.Capacity.SloMax.Rps; per.RpsPerReplica != want || *per.RpsPerGpu != want/2 {
		t.Errorf("rps per replica %v, per GPU %v; SLO-max %v", per.RpsPerReplica, *per.RpsPerGpu, want)
	}
}
