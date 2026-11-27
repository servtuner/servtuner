package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/null"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

const (
	examplesRoot = "../../../schema/examples/runspec"
	testRunID    = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

var fixedTime = time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return fixedTime }

// putRecorder remembers the order of Put calls and can fail one key.
type putRecorder struct {
	storage.BundleStore
	keys   []string
	failOn string
}

func (s *putRecorder) Put(ctx context.Context, key string, content io.Reader) error {
	if key == s.failOn {
		return errors.New("injected disk failure")
	}
	s.keys = append(s.keys, key)
	return s.BundleStore.Put(ctx, key, content)
}

// stepRecorder is an events.Reporter that keeps what it was told.
type stepRecorder struct {
	mutex  sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	step    spec.PipelineStep
	state   spec.ReportedState
	message string
}

func (s *stepRecorder) Report(_ context.Context, step spec.PipelineStep, state spec.ReportedState, message string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.events = append(s.events, recordedEvent{step, state, message})
}

func nullEngine(spec.RunSpec, engine.Inputs) (engine.Engine, error) { return null.New(fixedNow), nil }

func readExample(t *testing.T, validity, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(examplesRoot, validity, name))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func newOptions(t *testing.T, document []byte) (Options, *putRecorder, *stepRecorder) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	store := &putRecorder{BundleStore: storage.NewFileStore(dir)}
	reporter := &stepRecorder{}
	return Options{
		RunID: testRunID, SpecDocument: document, Store: store, BundleDir: dir, NewEngine: nullEngine,
		Reporter: reporter, Now: fixedNow, SecretsDir: t.TempDir(),
		PrometheusOptions: prometheus.Options{MaxAttempts: 1, RequestTimeout: 2 * time.Second},
		Sleep:             func(context.Context, time.Duration) error { return nil },
		DatasheetClient:   &http.Client{Transport: cannedDatasheet{}},
	}, store, reporter
}

// testDatasheet is a small sheet in the datasheet's format; tests never fetch
// the real one.
const testDatasheet = `{"gpt-4o-mini": {"provider": "openai", "input_cost_per_token": 1.5e-07, "output_cost_per_token": 6e-07}}`

// cannedDatasheet answers every request with testDatasheet.
type cannedDatasheet struct{}

func (cannedDatasheet) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(testDatasheet)),
		Header: http.Header{"Content-Type": []string{"application/json"}}, Request: request,
	}, nil
}

func readManifest(t *testing.T, store storage.BundleStore) spec.Manifest {
	t.Helper()
	reader, err := store.Get(context.Background(), bundle.ManifestJSON)
	if err != nil {
		t.Fatalf("no manifest: %v", err)
	}
	defer reader.Close()
	content, _ := io.ReadAll(reader)
	manifest, err := spec.Parse[spec.Manifest](spec.SchemaManifest, content)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// startFollowers runs the follower shards of a sharded RunSpec beside the
// leader, through the same store, as their own processes would.
func startFollowers(t *testing.T, document []byte, leader Options) {
	t.Helper()
	parsed, _, err := spec.LoadRunSpec(document)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < parsed.Engine.ShardCount(); index++ {
		follower := leader
		follower.ShardIndex = index
		follower.Reporter = nil
		go func() {
			if err := ExecuteShard(context.Background(), follower); err != nil {
				t.Errorf("shard %d: %v", index, err)
			}
		}()
	}
}

func TestEverySuccessfulBundleFileMatchesItsSchema(t *testing.T) {
	schemaOf := map[string]string{
		bundle.ResolvedSpecJSON:    spec.SchemaRunSpec,
		bundle.EnvironmentJSON:     spec.SchemaEnvironment,
		bundle.PhasesJSON:          spec.SchemaPhases,
		bundle.SummaryJSON:         spec.SchemaSummary,
		bundle.ManifestJSON:        spec.SchemaManifest,
		bundle.AnalysisJSON:        spec.SchemaAnalysis,
		bundle.RecommendationsJSON: spec.SchemaRecommendations,
		bundle.SnapshotPreJSON:     spec.SchemaTargetSnapshot,
		bundle.SnapshotPostJSON:    spec.SchemaTargetSnapshot,
	}
	// Written only by a RunSpec with infra_pricing; validated when present.
	optionalSchemaOf := map[string]string{bundle.InfraPricingJSON: spec.SchemaInfraPricing, bundle.TokenPricingJSON: spec.SchemaTokenPricing}
	files, err := filepath.Glob(filepath.Join(examplesRoot, "valid", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no valid examples: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			document, _ := os.ReadFile(file)
			options, store, reporter := newOptions(t, document)
			startFollowers(t, document, options)
			result := Execute(context.Background(), options)
			if result.Err != nil || result.Status != spec.RunStatusSucceeded {
				t.Fatalf("result = %+v", result)
			}
			// Every run writes these, with or without a cluster or Prometheus.
			wantFiles := []string{
				bundle.AnalysisJSON, bundle.ClientSamplesParquet, bundle.EnvironmentJSON, bundle.ManifestJSON,
				bundle.PhasesJSON, bundle.RecommendationsJSON, bundle.RequestsParquet, bundle.ResolvedSpecJSON, bundle.SpecYAML,
				bundle.SummaryJSON, bundle.SnapshotPostJSON, bundle.SnapshotPreJSON,
			}
			listed, _ := store.List(context.Background(), "")
			for _, name := range wantFiles {
				if !slices.Contains(listed, name) {
					t.Fatalf("bundle files = %v, missing %s", listed, name)
				}
			}
			expected := maps.Clone(schemaOf)
			for name, schemaName := range optionalSchemaOf {
				if slices.Contains(listed, name) {
					expected[name] = schemaName
				}
			}
			for name, schemaName := range expected {
				content := mustRead(t, store, name)
				if err := spec.Validate(schemaName, content); err != nil {
					t.Errorf("%s does not validate against %s: %v", name, schemaName, err)
				}
			}
			assertParquetMatchesSchema(t, mustRead(t, store, bundle.RequestsParquet))
			if !bytes.Equal(mustRead(t, store, bundle.SpecYAML), document) {
				t.Error("spec.yaml is not byte-for-byte the submitted document")
			}
			// manifest.json is written after every file it lists.
			if last := store.keys[len(store.keys)-1]; last != bundle.ManifestJSON {
				t.Errorf("last put = %s, want the manifest (puts: %v)", last, store.keys)
			}
			if slices.Index(store.keys, bundle.ManifestJSON) != len(store.keys)-1 {
				t.Errorf("manifest written before other files: %v", store.keys)
			}
			assertManifestChecksums(t, store)
			assertSevenStepsReported(t, reporter, spec.ReportedStateSucceeded)
		})
	}
}

func assertParquetMatchesSchema(t *testing.T, content []byte) {
	t.Helper()
	columns, err := bundle.ParquetColumnNames(content)
	if err != nil || len(columns) == 0 {
		t.Errorf("requests.parquet columns = %v, %v", columns, err)
	}
	rows, err := bundle.ReadRequests(content)
	if err != nil || len(rows) != 0 {
		t.Errorf("requests.parquet rows = %d, %v; want an empty file", len(rows), err)
	}
}

func mustRead(t *testing.T, store storage.BundleStore, name string) []byte {
	t.Helper()
	reader, err := store.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	defer reader.Close()
	content, _ := io.ReadAll(reader)
	return content
}

func assertManifestChecksums(t *testing.T, store storage.BundleStore) {
	t.Helper()
	manifest := readManifest(t, store)
	stored, err := store.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, file := range manifest.Files {
		listed = append(listed, file.Path)
	}
	stored = slices.DeleteFunc(stored, func(name string) bool { return name == bundle.ManifestJSON })
	slices.Sort(listed)
	if !slices.Equal(listed, stored) {
		t.Errorf("manifest lists %v, want every stored file but itself: %v", listed, stored)
	}
	for _, file := range manifest.Files {
		content := mustRead(t, store, file.Path)
		if file.SizeBytes != len(content) {
			t.Errorf("%s size %d != %d", file.Path, file.SizeBytes, len(content))
		}
	}
}

// assertSevenStepsReported checks one running event per step in pipeline order,
// then the terminal state of the run from finalize.
func assertSevenStepsReported(t *testing.T, reporter *stepRecorder, terminal spec.ReportedState) {
	t.Helper()
	var started []spec.PipelineStep
	for _, event := range reporter.events {
		if event.state == spec.ReportedStateRunning || event.state == spec.ReportedStateCollecting {
			started = append(started, event.step)
		}
	}
	want := []spec.PipelineStep{
		spec.PipelineStepResolve, spec.PipelineStepSnapshot, spec.PipelineStepLoad, spec.PipelineStepCollect,
		spec.PipelineStepNormalize, spec.PipelineStepSummarize, spec.PipelineStepFinalize,
	}
	if !slices.Equal(started, want) {
		t.Errorf("steps started = %v, want %v", started, want)
	}
	last := reporter.events[len(reporter.events)-1]
	if last.step != spec.PipelineStepFinalize || last.state != terminal {
		t.Errorf("last event = %+v, want finalize %s", last, terminal)
	}
}

func TestEnvironmentRecordsOnlyTheHopsUsed(t *testing.T) {
	document := readExample(t, "valid", "litellm-target-client-cert-prometheus-tls-off.json")
	options, store, _ := newOptions(t, document)
	options.APITransport = &spec.HopTransport{}
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatal(result.Err)
	}
	environment, err := spec.Parse[spec.Environment](spec.SchemaEnvironment, mustRead(t, store, bundle.EnvironmentJSON))
	if err != nil {
		t.Fatal(err)
	}
	transport := environment.Transport
	// The spec names a Prometheus without TLS, so that hop is used (and
	// recorded) even though no server answers in the test; no object store is.
	if transport.Target != (spec.HopTransport{Tls: true, ClientCert: true}) || transport.Prometheus == nil || *transport.Prometheus != (spec.HopTransport{}) ||
		transport.Api == nil || *transport.Api != (spec.HopTransport{}) || transport.ObjectStore != nil {
		t.Errorf("transport = %+v", transport)
	}
	if environment.Engine.Name != null.Name || environment.TlsRelayUsed {
		t.Errorf("environment = %+v", environment)
	}
}

func TestPhasesAndSummaryFollowTheLoadSection(t *testing.T) {
	document := readExample(t, "valid", "full-vllm-client-cert.json") // warmup 30s, two stages
	options, store, _ := newOptions(t, document)
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatal(result.Err)
	}
	phases, err := spec.Parse[spec.Phases](spec.SchemaPhases, mustRead(t, store, bundle.PhasesJSON))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, phase := range phases.Phases {
		names = append(names, phase.Name)
	}
	if !slices.Equal(names, []string{"warmup", "stage-0", "stage-1"}) {
		t.Errorf("phases = %v", names)
	}
	summary, err := spec.Parse[spec.Summary](spec.SchemaSummary, mustRead(t, store, bundle.SummaryJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Stages) != 2 || summary.Stages[0].IsValid || summary.Stages[1].OfferedRps != 20 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestInvalidSpecStillFinalizesAFailedManifest(t *testing.T) {
	document := readExample(t, "invalid", "stage-rate-zero.json")
	options, store, reporter := newOptions(t, document)
	result := Execute(context.Background(), options)
	if !errors.Is(result.Err, validate.ErrInvalidSpec) {
		t.Fatalf("error = %v, want ErrInvalidSpec", result.Err)
	}
	manifest := readManifest(t, store)
	if manifest.Status != spec.RunStatusFailed || manifest.Error == nil || manifest.Error.Step != spec.PipelineStepResolve {
		t.Errorf("manifest = %+v", manifest)
	}
	if manifest.Name != nil {
		t.Errorf("manifest names a spec that never parsed: %v", *manifest.Name)
	}
	if !bytes.Equal(mustRead(t, store, bundle.SpecYAML), document) {
		t.Error("spec.yaml missing or altered")
	}
	last := reporter.events[len(reporter.events)-1]
	if last.step != spec.PipelineStepFinalize || last.state != spec.ReportedStateFailed {
		t.Errorf("last event = %+v", last)
	}
}

func TestTransportMixingIsAnInvalidSpec(t *testing.T) {
	document := bytes.Replace(readExample(t, "valid", "minimal-vllm-client-cert.json"), []byte("https://"), []byte("http://"), 1)
	options, _, _ := newOptions(t, document)
	result := Execute(context.Background(), options)
	if !errors.Is(result.Err, validate.ErrInvalidSpec) {
		t.Errorf("tls with http:// = %v, want ErrInvalidSpec", result.Err)
	}
}

// failingEngine fails in Run after reporting one phase window.
type failingEngine struct {
	null.Engine
	err error
}

func (f *failingEngine) Run(ctx context.Context, phases []engine.Phase, sink engine.Sink) (engine.Result, error) {
	result, _ := f.Engine.Run(ctx, phases[:1], sink)
	return result, f.err
}

func TestLoadFailureKeepsWhatWasCollectedAndMapsToTheCause(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "full-vllm-client-cert.json"))
	options.NewEngine = func(spec.RunSpec, engine.Inputs) (engine.Engine, error) {
		return &failingEngine{Engine: *null.New(fixedNow), err: engine.ErrTargetUnreachable}, nil
	}
	result := Execute(context.Background(), options)
	if !errors.Is(result.Err, engine.ErrTargetUnreachable) {
		t.Fatalf("error = %v", result.Err)
	}
	manifest := readManifest(t, store)
	if manifest.Status != spec.RunStatusFailed || manifest.Error.Step != spec.PipelineStepLoad {
		t.Errorf("manifest = %+v", manifest)
	}
	listed, _ := store.List(context.Background(), "")
	// Raw first: the rows a failed load wrote are kept, but nothing derived
	// from them is computed.
	if slices.Contains(listed, bundle.SummaryJSON) || slices.Contains(listed, bundle.AnalysisJSON) || slices.Contains(listed, bundle.RecommendationsJSON) {
		t.Errorf("later steps ran after the failure: %v", listed)
	}
	if !slices.Contains(listed, bundle.RequestsParquet) {
		t.Errorf("the failed load's request rows were not kept: %v", listed)
	}
	if !slices.Contains(listed, bundle.PhasesJSON) {
		t.Errorf("partial phases.json was not kept: %v", listed)
	}
}

func TestBundleWriteFailureIsReportedAndStillTriesTheManifest(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "minimal-vllm-client-cert.json"))
	store.failOn = bundle.SummaryJSON
	result := Execute(context.Background(), options)
	if !errors.Is(result.Err, bundle.ErrWrite) {
		t.Fatalf("error = %v, want ErrWrite", result.Err)
	}
	manifest := readManifest(t, store)
	if manifest.Status != spec.RunStatusFailed || manifest.Error.Step != spec.PipelineStepSummarize {
		t.Errorf("manifest = %+v", manifest)
	}
}

func TestManifestFailureIsReported(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "minimal-vllm-client-cert.json"))
	store.failOn = bundle.ManifestJSON
	result := Execute(context.Background(), options)
	if !errors.Is(result.Err, bundle.ErrWrite) || result.Status != spec.RunStatusFailed || result.Failure == nil {
		t.Errorf("result = %+v", result)
	}
}

func TestCancelledRunStillWritesAFailedManifest(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "minimal-vllm-client-cert.json"))
	ctx, cancel := context.WithCancel(context.Background())
	options.NewEngine = func(spec.RunSpec, engine.Inputs) (engine.Engine, error) {
		cancel() // the signal arrives while the run is starting
		return null.New(fixedNow), nil
	}
	result := Execute(ctx, options)
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", result.Err)
	}
	if manifest := readManifest(t, store); manifest.Status != spec.RunStatusFailed {
		t.Errorf("manifest = %+v", manifest)
	}
}
