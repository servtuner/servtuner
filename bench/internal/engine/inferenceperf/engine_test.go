package inferenceperf

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

const testdataDir = "../../../testdata/inferenceperf"

func loadSpec(t *testing.T, name string) spec.RunSpec {
	t.Helper()
	document, err := os.ReadFile(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	runSpec, _, err := spec.LoadRunSpec(document)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return runSpec
}

func prepared(t *testing.T, options Options, runSpec spec.RunSpec) *Engine {
	t.Helper()
	if options.OutputDir == "" {
		options.OutputDir = filepath.Join(t.TempDir(), "native")
	}
	e := New(options)
	if err := e.Prepare(context.Background(), runSpec); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return e
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(testdataDir, "golden", name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Errorf("rendered config differs from %s (run with -update to accept):\n%s", path, got)
	}
}

func TestRenderedConfigMTLS(t *testing.T) {
	e := prepared(t, Options{
		OutputDir: "/srv/servtuner/engine",
		CertFile:  "/var/run/servtuner/tls/tls.crt",
		KeyFile:   "/var/run/servtuner/tls/tls.key",
	}, loadSpec(t, "runspec-mtls.yaml"))
	checkGolden(t, "config-mtls.yaml", e.config)
}

// TestRenderedConfigSharedPrefix: a shared prefix renders inference-perf's
// shared_prefix generator, with the question as input_tokens less the prefix
// and enough prompts per group that none repeats within the run.
func TestRenderedConfigSharedPrefix(t *testing.T) {
	runSpec := loadSpec(t, "runspec-platform.yaml")
	runSpec.Workload.Dataset.SharedPrefix = &spec.SharedPrefix{Tokens: 512, Groups: 4}
	e := prepared(t, Options{OutputDir: "/srv/servtuner/engine", NumWorkers: 2}, runSpec)
	checkGolden(t, "config-shared-prefix.yaml", e.config)
	if strings.Contains(string(e.config), "input_distribution") {
		t.Error("the shared_prefix generator takes no input_distribution: the CLI's config is strict")
	}
}

func TestRenderedConfigPlatform(t *testing.T) {
	e := prepared(t, Options{OutputDir: "/srv/servtuner/engine", NumWorkers: 2}, loadSpec(t, "runspec-platform.yaml"))
	checkGolden(t, "config-platform.yaml", e.config)
	if strings.Contains(string(e.config), "cert_path") {
		t.Error("platform config must carry no certificate settings")
	}
}

func TestPrepareRejectsWhatItCannotRun(t *testing.T) {
	tests := map[string]struct {
		change func(*spec.RunSpec)
		want   string
	}{
		"chat api":     {func(s *spec.RunSpec) { s.Workload.Api = spec.WorkloadApiChat }, "workload.api"},
		"embeddings":   {func(s *spec.RunSpec) { s.Workload.Api = spec.WorkloadApiEmbeddings }, "workload.api"},
		"linear":       {func(s *spec.RunSpec) { s.Load.Pacing = spec.PacingLinear }, "load.pacing"},
		"sine":         {func(s *spec.RunSpec) { s.Load.Pacing = spec.PacingSine }, "load.pacing"},
		"auth":         {func(s *spec.RunSpec) { s.Target.Auth = &spec.TargetAuth{} }, "target.auth"},
		"other engine": {func(s *spec.RunSpec) { s.Engine.Type = spec.EngineTypeLoadgen }, "engine.type"},
		"version":      {func(s *spec.RunSpec) { s.Engine.Version = "0.6.0" }, "engine.version"},
		"option type":  {func(s *spec.RunSpec) { s.Engine.Options = spec.EngineOptions{"tokenizer": 3} }, "must be a string"},
		"fraction":     {func(s *spec.RunSpec) { s.Load.Stages[0].Duration = "1500ms" }, "whole number of seconds"},
		"warmup":       {func(s *spec.RunSpec) { s.Load.Warmup = "2500ms" }, "whole number of seconds"},
		"fixed mean":   {func(s *spec.RunSpec) { s.Workload.Dataset.OutputTokens = spec.TokenDistribution{Mean: 10.5} }, "fixed length"},
		"no /v1":       {func(s *spec.RunSpec) { s.Target.Endpoint = "https://vllm.inference.svc:8000" }, "must end in /v1"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runSpec := loadSpec(t, "runspec-mtls.yaml")
			test.change(&runSpec)
			e := New(Options{
				OutputDir: t.TempDir(),
				CertFile:  "/tls/tls.crt",
				KeyFile:   "/tls/tls.key",
			})
			err := e.Prepare(context.Background(), runSpec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

// TestRenderedConfigTLS: tls mode renders no client certificate settings;
// the server is verified through SSL_CERT_FILE (the combined CA bundle).
func TestRenderedConfigTLS(t *testing.T) {
	runSpec := loadSpec(t, "runspec-mtls.yaml")
	runSpec.Target.Tls = spec.TlsServer{Mode: "tls"}
	e := prepared(t, Options{OutputDir: "/srv/servtuner/engine", CABundleFile: "/tmp/bench-run/ca-bundle.crt"}, runSpec)
	if strings.Contains(string(e.config), "cert_path") || strings.Contains(string(e.config), "key_path") {
		t.Errorf("tls config must carry no client certificate settings:\n%s", e.config)
	}
}

func TestPrepareEnforcesTransportMode(t *testing.T) {
	t.Run("tls takes no client certificate", func(t *testing.T) {
		runSpec := loadSpec(t, "runspec-mtls.yaml")
		runSpec.Target.Tls = spec.TlsServer{Mode: "tls"}
		err := New(Options{OutputDir: t.TempDir(), CertFile: "/tls/tls.crt", KeyFile: "/tls/tls.key"}).Prepare(context.Background(), runSpec)
		if err == nil || !strings.Contains(err.Error(), "tls") {
			t.Fatalf("Prepare error = %v, want a mixed-mode error", err)
		}
	})
	t.Run("mtls needs the client certificate", func(t *testing.T) {
		err := New(Options{OutputDir: t.TempDir()}).Prepare(context.Background(), loadSpec(t, "runspec-mtls.yaml"))
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("Prepare error = %v, want a missing-certificate error", err)
		}
	})
	t.Run("platform takes no certificate", func(t *testing.T) {
		err := New(Options{OutputDir: t.TempDir(), CertFile: "/tls/tls.crt", KeyFile: "/tls/tls.key"}).
			Prepare(context.Background(), loadSpec(t, "runspec-platform.yaml"))
		if err == nil || !strings.Contains(err.Error(), "platform") {
			t.Fatalf("Prepare error = %v, want a mixed-mode error", err)
		}
	})
}

func TestTokenizerOptionOverridesModelName(t *testing.T) {
	runSpec := loadSpec(t, "runspec-platform.yaml")
	runSpec.Engine.Options = spec.EngineOptions{"tokenizer": "/opt/tokenizer"}
	e := prepared(t, Options{}, runSpec)
	if !strings.Contains(string(e.config), "pretrained_model_name_or_path: /opt/tokenizer") {
		t.Errorf("tokenizer override missing:\n%s", e.config)
	}
}

func TestPassthroughOptionsAreMergedIntoTheConfig(t *testing.T) {
	runSpec := loadSpec(t, "runspec-platform.yaml")
	runSpec.Engine.Options = spec.EngineOptions{"circuit_breakers": []any{map[string]any{
		"name":     "errors",
		"metrics":  map[string]any{"matches": []any{"request_latency"}, "rules": []any{"> 5"}},
		"triggers": []any{map[string]any{"type": "consecutive", "threshold": 5}},
	}}}
	e := prepared(t, Options{OutputDir: "/srv/servtuner/engine", NumWorkers: 2}, runSpec)
	checkGolden(t, "config-passthrough.yaml", e.config)
}

func TestChatIsRejectedWithAPointerToLoadgen(t *testing.T) {
	runSpec := loadSpec(t, "runspec-platform.yaml")
	runSpec.Workload.Api = spec.WorkloadApiChat
	err := New(Options{OutputDir: t.TempDir()}).Prepare(context.Background(), runSpec)
	if err == nil || !strings.Contains(err.Error(), "engine.type: loadgen") {
		t.Fatalf("Prepare error = %v, want it to point at loadgen", err)
	}
}

func TestDistributionMapping(t *testing.T) {
	spread, err := renderDistribution("f", spec.TokenDistribution{Mean: 100, Stddev: 10})
	if err != nil || spread.Type != "normal" || spread.Min != 60 || spread.Max != 140 {
		t.Errorf("spread = %+v, %v", spread, err)
	}
	narrow, err := renderDistribution("f", spec.TokenDistribution{Mean: 3, Stddev: 2})
	if err != nil || narrow.Min != 1 {
		t.Errorf("minimum must stay at 1 token: %+v, %v", narrow, err)
	}
}

type collectingSink struct {
	mutex sync.Mutex
	rows  []engine.RequestRow
}

func (s *collectingSink) Write(row engine.RequestRow) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

// fakeCLI is a shell script that stands in for the pinned CLI: it records its
// arguments and the environment variables the adapter must set, then copies a
// recorded native output into the directory named by the config's storage path.
func fakeCLI(t *testing.T, nativeFixture string) (script, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record.txt")
	script = filepath.Join(dir, "inference-perf")
	body := `#!/bin/sh
{
  echo "args: $*"
  echo "ssl: ${SSL_CERT_FILE-unset}"
  echo "request-checksum: ${AWS_REQUEST_CHECKSUM_CALCULATION-unset}"
  echo "response-checksum: ${AWS_RESPONSE_CHECKSUM_VALIDATION-unset}"
} > "` + record + `"
out=$(dirname "$2")
for file in "` + nativeFixture + `"/*; do
  case "$(basename "$file")" in stdout.txt|stderr.txt) continue;; esac
  cp "$file" "$out/"
done
echo "native stdout line"
echo "native stderr line" >&2
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, record
}

func TestRunKeepsNativeOutputAndSetsEnvironment(t *testing.T) {
	fixture := filepath.Join(testdataDir, "native-platform")
	script, record := fakeCLI(t, fixture)
	outputDir := filepath.Join(t.TempDir(), "native")
	e := prepared(t, Options{
		Executable:   script,
		OutputDir:    outputDir,
		CertFile:     "/tls/tls.crt",
		KeyFile:      "/tls/tls.key",
		CABundleFile: "/tls/combined-ca.pem",
		Environment:  []string{"PATH=/usr/bin:/bin"},
	}, loadSpec(t, "runspec-mtls.yaml"))
	sink := &collectingSink{}
	// The recorded fixture has 3 stages; plan matching phases for it.
	e.phases = fixturePhases()
	result, err := e.Run(context.Background(), fixturePhases(), sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.NativeOutputDir != outputDir {
		t.Errorf("NativeOutputDir = %q, want %q", result.NativeOutputDir, outputDir)
	}
	for file, want := range map[string]string{StdoutFile: "native stdout line\n", StderrFile: "native stderr line\n"} {
		got, err := os.ReadFile(filepath.Join(outputDir, file))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	// Reports are copied through byte for byte.
	want, _ := os.ReadFile(filepath.Join(fixture, perRequestReportFile))
	got, err := os.ReadFile(filepath.Join(outputDir, perRequestReportFile))
	if err != nil || string(got) != string(want) {
		t.Errorf("per-request report was altered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, RenderedConfigFile)); err != nil {
		t.Errorf("rendered config not kept: %v", err)
	}
	recorded, _ := os.ReadFile(record)
	for _, line := range []string{
		"args: -c " + filepath.Join(outputDir, RenderedConfigFile),
		"ssl: /tls/combined-ca.pem",
		"request-checksum: when_required",
		"response-checksum: when_required",
	} {
		if !strings.Contains(string(recorded), line) {
			t.Errorf("child did not see %q; saw:\n%s", line, recorded)
		}
	}
	if len(sink.rows) != 30 || len(result.Windows) != 3 {
		t.Errorf("rows = %d, windows = %d; want 30 and 3", len(sink.rows), len(result.Windows))
	}
}

func TestRunInPlatformModeLeavesTrustAlone(t *testing.T) {
	script, record := fakeCLI(t, filepath.Join(testdataDir, "native-platform"))
	e := prepared(t, Options{Executable: script, Environment: []string{"PATH=/usr/bin:/bin"}}, loadSpec(t, "runspec-platform.yaml"))
	e.phases = fixturePhases()
	if _, err := e.Run(context.Background(), fixturePhases(), &collectingSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	recorded, _ := os.ReadFile(record)
	if !strings.Contains(string(recorded), "ssl: unset") {
		t.Errorf("platform mode must not set SSL_CERT_FILE:\n%s", recorded)
	}
}

func TestRunRejectsChangedPhases(t *testing.T) {
	e := prepared(t, Options{}, loadSpec(t, "runspec-platform.yaml"))
	if _, err := e.Run(context.Background(), fixturePhases(), &collectingSink{}); err == nil {
		t.Fatal("Run accepted phases that differ from the prepared ones")
	}
}

func TestRunReportsCLIFailureButKeepsOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "inference-perf")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(dir, "native")
	e := prepared(t, Options{Executable: script, OutputDir: outputDir}, loadSpec(t, "runspec-platform.yaml"))
	_, err := e.Run(context.Background(), e.phases, &collectingSink{})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("Run error = %v, want the CLI's exit status", err)
	}
	if stderr, _ := os.ReadFile(filepath.Join(outputDir, StderrFile)); string(stderr) != "boom\n" {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestEveryPresetListingInferencePerfPassesItsSpecCheck: a preset may list
// inference-perf only if this adapter can run its workload, or the preset
// passes validation and then fails at Prepare.
func TestEveryPresetListingInferencePerfPassesItsSpecCheck(t *testing.T) {
	presets, err := spec.WorkloadPresets()
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range presets {
		if !slices.Contains(preset.Engines, spec.EngineTypeInferencePerf) {
			continue
		}
		t.Run(preset.Name, func(t *testing.T) {
			runSpec := loadSpec(t, "runspec-platform.yaml")
			reference := spec.PresetReference(spec.FormatPresetReference(preset.Name, preset.Version))
			runSpec.Workload = spec.Workload{Preset: &reference, Streaming: true}
			expanded, err := spec.ExpandPreset(runSpec)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkSpec(expanded); err != nil {
				t.Errorf("preset lists inference-perf but the adapter refuses it: %v", err)
			}
		})
	}
}
