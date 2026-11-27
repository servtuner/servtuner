package analyze_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/signals"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/target"
)

func loadRun(t *testing.T, dir string) (input.Run, error) {
	t.Helper()
	return analyze.LoadRun(context.Background(), storage.NewFileStore(dir), analyze.ParquetRecords(dir))
}

func TestLoadRunWithoutOptionalFiles(t *testing.T) {
	dir := writeScenario(t, analyzetest.ScenarioNamed("achieved-rps-low"))
	for _, name := range []string{"prometheus", "target", "environment.json"} {
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
	run, err := loadRun(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if run.Targets != nil || run.Queries != nil || run.PreSnapshot != nil || run.PostSnapshot != nil || run.BenchPod != "" || run.ID != analyzetest.RunID {
		t.Errorf("run = %+v", run)
	}
	if _, ok, _ := run.Matrix.Matrix(signals.CPUCores); ok {
		t.Error("no series file means not collected")
	}
	if _, err := analyze.Analyze(run); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRunReportsMissingAndInvalidFiles(t *testing.T) {
	dir := writeScenario(t, analyzetest.ScenarioNamed("knobs"))
	if err := os.Remove(filepath.Join(dir, "phases.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRun(t, dir); err == nil {
		t.Error("phases.json is required")
	}
	dir = writeScenario(t, analyzetest.ScenarioNamed("knobs"))
	if err := os.WriteFile(filepath.Join(dir, "target", "snapshot-pre.json"), []byte(`{"phase":"pre"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRun(t, dir); err == nil {
		t.Error("a snapshot that breaks its schema must be refused")
	}
}

func TestLoadRunTakesTheBenchPodAndKnobsFromTheRecordedFiles(t *testing.T) {
	run, err := loadRun(t, writeScenario(t, analyzetest.ScenarioNamed("knobs")))
	if err != nil {
		t.Fatal(err)
	}
	if run.BenchPod != analyzetest.BenchPod {
		t.Errorf("bench pod = %q", run.BenchPod)
	}
	if run.CapturedKnobs[target.KnobVLLMMaxNumSeqs] != 128.0 || run.CapturedKnobs[target.KnobVLLMEnablePrefixCaching] != true {
		t.Errorf("captured = %v", run.CapturedKnobs)
	}
}

func writeSeriesFile(t *testing.T, dir string, entry input.SeriesRef, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(bundle.PrometheusSeriesFile(string(entry.Catalog), entry.Name)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBundleMatrixKeepsEverySeriesWithItsLabelsAndDropsNaN(t *testing.T) {
	dir := t.TempDir()
	writeSeriesFile(t, dir, signals.VLLMKVCacheUsage, `{"resultType":"matrix","result":[
	  {"metric":{"pod":"a"},"values":[[1767225660,"0.2"],[1767225665,"NaN"],[1767225670,"0.5"]]},
	  {"metric":{"pod":"b"},"values":[[1767225660,"0.4"]]}]}`)
	series, ok, err := analyze.NewBundleMatrix(context.Background(), storage.NewFileStore(dir)).Matrix(signals.VLLMKVCacheUsage)
	if err != nil || !ok || len(series) != 2 {
		t.Fatalf("series = %v, %v, %v", series, ok, err)
	}
	if series[0].Labels["pod"] != "a" || len(series[0].Samples) != 2 || series[0].Samples[1].Value != 0.5 ||
		!series[0].Samples[0].At.Equal(time.Unix(1767225660, 0)) || series[1].Labels["pod"] != "b" {
		t.Errorf("series = %+v", series)
	}
}

func TestBundleMatrixRejectsANonMatrixAndIgnoresAMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := analyze.NewBundleMatrix(context.Background(), storage.NewFileStore(dir))
	if _, ok, err := store.Matrix(signals.VLLMWaiting); ok || err != nil {
		t.Errorf("a missing file is not collected: %v %v", ok, err)
	}
	writeSeriesFile(t, dir, signals.VLLMWaiting, `{"resultType":"vector","result":[]}`)
	if _, _, err := store.Matrix(signals.VLLMWaiting); err == nil {
		t.Error("an instant vector is not a range result")
	}
}

func TestStagesFromPhases(t *testing.T) {
	scenario := analyzetest.ScenarioNamed("clean-ladder")
	stages, err := input.StagesFromPhases(scenario.Phases())
	if err != nil || len(stages) != 6 || stages[5].Index != 5 || stages[0].Duration() != 60*time.Second {
		t.Fatalf("stages = %+v, %v", stages, err)
	}
	broken := scenario.Phases()
	broken.Phases[1].OfferedRps = nil
	if _, err := input.StagesFromPhases(broken); err == nil {
		t.Error("a stage without offered_rps is invalid")
	}
	duplicate := scenario.Phases()
	duplicate.Phases = append(duplicate.Phases, duplicate.Phases[1])
	if _, err := input.StagesFromPhases(duplicate); err == nil {
		t.Error("a repeated stage is invalid")
	}
	reversed := scenario.Phases()
	reversed.Phases[1].End = reversed.Phases[1].Start
	if _, err := input.StagesFromPhases(reversed); err == nil {
		t.Error("a stage that ends when it starts is invalid")
	}
}

// Knobs read from the target's config API (Bifrost's) are in environment.json
// only; the pod snapshot cannot show them.
func TestLoadRunTakesConfigAPIKnobsFromEnvironmentJSON(t *testing.T) {
	dir := writeScenario(t, analyzetest.ScenarioNamed("knobs"))
	path := filepath.Join(dir, bundle.EnvironmentJSON)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]any
	if err := json.Unmarshal(raw, &environment); err != nil {
		t.Fatal(err)
	}
	environment["knobs"] = map[string]any{target.KnobBifrostConcurrency: 1000, target.KnobBifrostBufferSize: 5000}
	raw, _ = json.Marshal(environment)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := loadRun(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if run.CapturedKnobs[target.KnobBifrostConcurrency] != 1000.0 || run.CapturedKnobs[target.KnobBifrostBufferSize] != 5000.0 {
		t.Errorf("captured = %v", run.CapturedKnobs)
	}
	if run.CapturedKnobs[target.KnobVLLMMaxNumSeqs] != 128.0 {
		t.Errorf("the snapshot's knobs were lost: %v", run.CapturedKnobs)
	}
}
