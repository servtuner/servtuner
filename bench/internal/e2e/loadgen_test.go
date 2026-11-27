//go:build e2e

// Package e2e runs bench end to end on this machine, with no cluster: the
// repository's OpenAI mock as the target and the loadgen engine through the
// real CLI. Run it with `just e2e-local-loadgen`.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/cli"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

const (
	mockModel       = "mock-model"
	warmupRate      = 20 // the first stage's rate, which loadgen offers during warmup
	readyTimeout    = 30 * time.Second
	stopGracePeriod = 5 * time.Second
)

// stageRates are the offered rates of the ladder, one stage per entry.
var stageRates = []float64{warmupRate, 40}

func TestLoadgenAgainstTheMockWritesACompleteBundle(t *testing.T) {
	endpoint := startMock(t)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	specFile := writeRunSpec(t, endpoint)

	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), []string{"run", "--spec", specFile, "--out", bundleDir}, &stdout, &stderr)
	if code != cli.ExitOK {
		t.Fatalf("servtuner run exited %d\nstderr:\n%s", code, stderr.String())
	}

	manifest := readDocument[spec.Manifest](t, bundleDir, bundle.ManifestJSON, spec.SchemaManifest)
	if manifest.Status != spec.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, error %+v", manifest.Status, manifest.Error)
	}
	for name, schema := range map[string]string{
		bundle.EnvironmentJSON:     spec.SchemaEnvironment,
		bundle.PhasesJSON:          spec.SchemaPhases,
		bundle.AnalysisJSON:        spec.SchemaAnalysis,
		bundle.RecommendationsJSON: spec.SchemaRecommendations,
		bundle.SummaryJSON:         spec.SchemaSummary,
		bundle.ResolvedSpecJSON:    spec.SchemaRunSpec,
	} {
		if err := spec.Validate(schema, readFile(t, bundleDir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	logAnalysis(t, bundleDir)
	checkRequestRows(t, bundleDir)
	checkClientSamples(t, bundleDir)
	checkEnvironment(t, bundleDir)
}

// TestShardedLoadgenMergesEveryShardsRows runs a leader and two followers of a
// three-shard run, as three bench processes would: the leader's bundle must
// hold every shard's rows, tagged by worker, adding up to the offered rate.
func TestShardedLoadgenMergesEveryShardsRows(t *testing.T) {
	const shards = 3
	const runID = "01JSHARD00000000000000000A"
	endpoint := startMock(t)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	specFile := writeRunSpecWith(t, endpoint, map[string]any{"type": "loadgen", "shards": shards})

	codes := make([]int, shards)
	outputs := make([]bytes.Buffer, shards)
	done := make(chan struct{})
	for index := range shards {
		go func() {
			defer func() { done <- struct{}{} }()
			arguments := []string{"run", "--spec", specFile, "--out", bundleDir, "--run-id", runID, "--shard-index", fmt.Sprint(index)}
			codes[index] = cli.Execute(context.Background(), arguments, &outputs[index], &outputs[index])
		}()
	}
	for range shards {
		<-done
	}
	for index, code := range codes {
		if code != cli.ExitOK {
			t.Fatalf("shard %d exited %d:\n%s", index, code, outputs[index].String())
		}
	}
	checkRequestRows(t, bundleDir)
	perWorker := map[int64]int{}
	for row, err := range bundle.ReadRequestRows(bundleDir) {
		if err != nil {
			t.Fatal(err)
		}
		if row.Worker == nil {
			t.Fatal("a sharded run's row has no worker")
		}
		perWorker[*row.Worker]++
	}
	for worker := range int64(shards) {
		if perWorker[worker] == 0 {
			t.Errorf("no rows from shard %d (%v)", worker, perWorker)
		}
	}
	phases := readDocument[spec.Phases](t, bundleDir, bundle.PhasesJSON, spec.SchemaPhases)
	for _, phase := range phases.Phases {
		if phase.StageIndex != nil {
			if want := stageRates[*phase.StageIndex]; phase.OfferedRps == nil || *phase.OfferedRps < want*0.95 {
				t.Errorf("stage %d offered %v RPS, want the shards' sum, about %g", *phase.StageIndex, phase.OfferedRps, want)
			}
		}
	}
	t.Logf("rows per shard: %v", perWorker)
}

// checkRequestRows expects warmup rows and rows for every stage, all
// successful, with the timings a streaming request has.
func checkRequestRows(t *testing.T, bundleDir string) {
	t.Helper()
	perStage := map[int64]int{}
	warmupRows := 0
	for row, err := range bundle.ReadRequestRows(bundleDir) {
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != http.StatusOK || row.Error != nil {
			t.Errorf("request failed: status %d, error %v", row.Status, row.Error)
			continue
		}
		if row.TTFTSeconds == nil || row.TTFBSeconds == nil || row.ScheduledAt == nil || row.SendLagSeconds == nil {
			t.Errorf("row lacks streaming timings: %+v", row)
		}
		switch row.Phase {
		case engine.RowPhaseWarmup:
			warmupRows++
		case engine.RowPhaseStage:
			perStage[*row.StageIndex]++
		default:
			t.Errorf("row phase %q", row.Phase)
		}
	}
	if warmupRows == 0 {
		t.Error("no warmup rows")
	}
	for index, rate := range stageRates {
		// A stage offers rate x duration requests, less the hit due on its
		// closing boundary; allow a little for scheduling on a busy machine.
		want := int(rate*stageSeconds) - 1
		if got := perStage[int64(index)]; got < want*95/100 {
			t.Errorf("stage %d: %d rows, want about %d", index, got, want)
		}
	}
}

// logAnalysis prints what the run measured, so a passing run still shows its
// numbers. A local run has no Prometheus, so the grade is at best degraded.
func logAnalysis(t *testing.T, bundleDir string) {
	t.Helper()
	analysis := readDocument[spec.AnalysisResult](t, bundleDir, bundle.AnalysisJSON, spec.SchemaAnalysis)
	t.Logf("grade %s", analysis.Quality.Grade)
	for _, stage := range analysis.Stages {
		p95 := "n/a"
		if stage.Ttft != nil && stage.Ttft.P95Seconds.Value != nil {
			p95 = fmt.Sprintf("%.1fms", *stage.Ttft.P95Seconds.Value*1000)
		}
		t.Logf("stage %d: offered %.1f rps, achieved %.1f rps, %d requests, %d errors, ttft p95 %s",
			stage.StageIndex, stage.OfferedRps, stage.AchievedRps, stage.Requests, stage.Errors, p95)
	}
	for _, stage := range analysis.Quality.Stages {
		if !stage.IsValid {
			t.Errorf("stage %d is not valid: %+v", stage.StageIndex, stage.Issues)
		}
	}
}

func checkClientSamples(t *testing.T, bundleDir string) {
	t.Helper()
	samples := 0
	for sample, err := range bundle.ReadClientSamples(bundleDir) {
		if err != nil {
			t.Fatal(err)
		}
		if sample.Source != string(spec.ClientSampleSourceLoadgen) {
			t.Errorf("sample source %q", sample.Source)
		}
		samples++
	}
	// One sample a second over warmup and both stages.
	if minimum := int(warmupSeconds+stageSeconds*float64(len(stageRates))) - 2; samples < minimum {
		t.Errorf("%d client samples, want at least %d", samples, minimum)
	}
}

func checkEnvironment(t *testing.T, bundleDir string) {
	t.Helper()
	environment := readDocument[spec.Environment](t, bundleDir, bundle.EnvironmentJSON, spec.SchemaEnvironment)
	if environment.Engine.Name != string(spec.EngineTypeLoadgen) || environment.Transport.Target != (spec.HopTransport{}) {
		t.Errorf("environment engine %+v, target transport %+v", environment.Engine, environment.Transport.Target)
	}
	if environment.Workload == nil || environment.Workload.Hash == "" {
		t.Errorf("environment workload = %+v", environment.Workload)
	}
	if environment.HttpVersion == nil {
		t.Error("environment has no http_version")
	}
}

const (
	warmupSeconds = 3.0
	stageSeconds  = 5.0
)

// writeRunSpec writes a short ladder against the mock, without TLS.
func writeRunSpec(t *testing.T, endpoint string) string {
	t.Helper()
	return writeRunSpecWith(t, endpoint, map[string]any{"type": "loadgen"})
}

func writeRunSpecWith(t *testing.T, endpoint string, engineBlock map[string]any) string {
	t.Helper()
	stages := make([]map[string]any, len(stageRates))
	for index, rate := range stageRates {
		stages[index] = map[string]any{"rate": rate, "duration": fmt.Sprintf("%gs", stageSeconds)}
	}
	runSpec := map[string]any{
		"apiVersion": "servtuner.dev/v2",
		"kind":       "RunSpec",
		"name":       "e2e-local-loadgen",
		"target": map[string]any{
			"type": "openai-compatible", "endpoint": endpoint, "model": mockModel,
			"tls": map[string]any{"enabled": false},
		},
		"engine": engineBlock,
		"workload": map[string]any{
			"streaming": true,
			"dataset": map[string]any{
				"type": "synthetic", "input_tokens": map[string]any{"mean": 64}, "output_tokens": map[string]any{"mean": 16},
			},
		},
		"load": map[string]any{"warmup": fmt.Sprintf("%gs", warmupSeconds), "stages": stages},
	}
	document, err := json.Marshal(runSpec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runspec.json")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// startMock builds the repository's mock, starts it on a free loopback port
// without TLS and returns its /v1 endpoint. The process is stopped when the
// test ends.
func startMock(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	mockSource := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "deploy", "mocks", "openai-mock")
	binary := filepath.Join(t.TempDir(), "openai-mock")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = mockSource
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the mock: %v\n%s", err, output)
	}

	address := freeLoopbackAddress(t)
	mock := exec.Command(binary, "--listen", address, "--tls", "false", "--model", mockModel,
		"--ttft", "20ms", "--itl", "2ms")
	mock.Stdout, mock.Stderr = os.Stderr, os.Stderr
	if err := mock.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopProcess(t, mock) })

	deadline := time.Now().Add(readyTimeout)
	for {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = connection.Close()
			return "http://" + address + "/v1"
		}
		if time.Now().After(deadline) {
			t.Fatalf("mock did not listen on %s within %s: %v", address, readyTimeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// freeLoopbackAddress picks a free port. The mock prints no bound address, so
// port 0 cannot be handed to it; the brief race is acceptable in a local test.
func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func stopProcess(t *testing.T, process *exec.Cmd) {
	_ = process.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case <-done:
	case <-time.After(stopGracePeriod):
		_ = process.Process.Kill()
		<-done
		t.Log("mock did not stop on interrupt; killed it")
	}
}

func readFile(t *testing.T, bundleDir, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(bundleDir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func readDocument[T any](t *testing.T, bundleDir, name, schema string) T {
	t.Helper()
	document, err := spec.Parse[T](schema, readFile(t, bundleDir, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return document
}
