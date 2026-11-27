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
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestTheCapacityModelIsWrittenPerReplica(t *testing.T) {
	dir := writeScenario(t, analyzetest.ScenarioNamed("clean-ladder"))
	result, err := analyze.AnalyzeBundle(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, bundle.CapacityModelJSON))
	if err != nil {
		t.Fatal(err)
	}
	model, err := spec.Parse[spec.RunCapacityModel](spec.SchemaCapacityModel, raw)
	if err != nil {
		t.Fatal(err)
	}
	if model.ReadyReplicas < 1 || len(model.Stages) != len(result.Stages) {
		t.Fatalf("model = %+v", model)
	}
	for i, stage := range model.Stages {
		want := result.Stages[i].AchievedRps / float64(model.ReadyReplicas)
		if stage.RpsPerReplica != want {
			t.Errorf("stage %d: %v RPS per replica, want %v", i, stage.RpsPerReplica, want)
		}
	}
	if model.Identity.QualityGrade != result.Quality.Grade || len(model.Identity.WorkloadHash) != 64 {
		t.Errorf("identity = %+v", model.Identity)
	}
}

func TestTheWorkloadHashIgnoresTheSeedButNotTheMix(t *testing.T) {
	var workload spec.Workload
	if err := json.Unmarshal([]byte(`{"streaming": true, "dataset": {"type": "synthetic", "input_tokens": {"mean": 128}, "output_tokens": {"mean": 32}}}`), &workload); err != nil {
		t.Fatal(err)
	}
	base, _ := analyze.WorkloadHash(workload)
	seeded := workload
	seeded.Seed = 42
	if hash, _ := analyze.WorkloadHash(seeded); hash != base {
		t.Error("the seed changed the workload hash")
	}
	other := workload
	other.Streaming = false
	if hash, _ := analyze.WorkloadHash(other); hash == base {
		t.Error("a different request mix has the same hash")
	}
}

// The router's memory line reaches capacity_model.json with the request size
// it was measured at, for the planner's m(s).
func TestTheCapacityModelCarriesTheMemoryLine(t *testing.T) {
	_, dir := analyzeGolden(t, "bifrost-throttled")
	encoded, err := os.ReadFile(filepath.Join(dir, bundle.CapacityModelJSON))
	if err != nil {
		t.Fatal(err)
	}
	var model spec.RunCapacityModel
	if err := json.Unmarshal(encoded, &model); err != nil {
		t.Fatal(err)
	}
	memory := model.Memory
	if memory == nil || memory.BytesPerInFlight <= 0 || memory.Stages < 2 {
		t.Fatalf("memory = %+v", memory)
	}
	if memory.RequestBytesMean == nil || *memory.RequestBytesMean != analyzetest.SyntheticRequestBytes {
		t.Errorf("request_bytes_mean = %v", memory.RequestBytesMean)
	}
	if memory.QueueSize == nil || memory.LimitHeadroom == nil {
		t.Errorf("B and h_lim missing: %+v", memory)
	}
}

// TestTheCapacityModelNamesItsPreset: the identity carries the run's preset,
// so a plan built from it can name the workload.
func TestTheCapacityModelNamesItsPreset(t *testing.T) {
	dir := writeScenario(t, analyzetest.ScenarioNamed("clean-ladder"))
	path := filepath.Join(dir, bundle.ResolvedSpecJSON)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var resolved map[string]any
	if err := json.Unmarshal(raw, &resolved); err != nil {
		t.Fatal(err)
	}
	resolved["workload_preset"] = map[string]any{"name": "chat-short", "version": 1}
	if raw, err = json.Marshal(resolved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := analyze.AnalyzeBundle(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	modelRaw, err := os.ReadFile(filepath.Join(dir, bundle.CapacityModelJSON))
	if err != nil {
		t.Fatal(err)
	}
	model, err := spec.Parse[spec.RunCapacityModel](spec.SchemaCapacityModel, modelRaw)
	if err != nil {
		t.Fatal(err)
	}
	if preset := model.Identity.WorkloadPreset; preset == nil || preset.Name != "chat-short" || preset.Version != 1 {
		t.Errorf("identity preset = %v", preset)
	}
}
