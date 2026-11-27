package loadgen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

// chatUsageBody is a non-streaming chat response carrying usage.
const chatUsageBody = `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}`

// collectingSink keeps every row in memory.
type collectingSink struct {
	mutex sync.Mutex
	rows  []engine.RequestRow
	err   error
}

func (s *collectingSink) Write(row engine.RequestRow) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.err != nil {
		return s.err
	}
	s.rows = append(s.rows, row)
	return nil
}

func (s *collectingSink) snapshot() []engine.RequestRow {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append([]engine.RequestRow(nil), s.rows...)
}

type collectingClientSink struct {
	mutex   sync.Mutex
	samples []engine.ClientSampleRow
}

func (s *collectingClientSink) WriteSample(sample engine.ClientSampleRow) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.samples = append(s.samples, sample)
	return nil
}

// specFor builds a RunSpec that offers rate for duration against endpoint.
func specFor(endpoint string, tls spec.Tls, isStreaming bool, rate float64, duration time.Duration) spec.RunSpec {
	return spec.RunSpec{
		Target: spec.Target{Endpoint: endpoint, Model: "test-model", Type: spec.TargetTypeOpenaiCompatible, Tls: tls},
		Workload: spec.Workload{
			Api:       spec.WorkloadApiChat,
			Seed:      1,
			Streaming: isStreaming,
			Dataset: &spec.Dataset{
				Type:         spec.DatasetTypeSynthetic,
				InputTokens:  spec.TokenDistribution{Mean: 16},
				OutputTokens: spec.TokenDistribution{Mean: 8},
			},
		},
		Load: spec.Load{
			Pacing: spec.PacingConstant,
			Warmup: "0s",
			Stages: []spec.Stage{{Rate: rate, Duration: spec.Duration(duration.String())}},
		},
	}
}

var plainTLS = spec.TlsDisabled{}
var mtlsTLS = spec.TlsEnabled{Enabled: true, ClientCert: &spec.TlsClientCert{SecretRef: spec.SecretRef{Name: "servtuner-client"}}}
var serverTLS = spec.TlsEnabled{Enabled: true}

// usageHandler answers every request with a chat completion after delay.
func usageHandler(delay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatUsageBody)
	})
}

func platformServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	return tlstest.NewPlainServer(t, handler).URL + "/v1"
}

// prepare builds and prepares an engine, failing the test on error.
func prepare(t *testing.T, options Options, runSpec spec.RunSpec) *Engine {
	t.Helper()
	e := New(options)
	if err := e.Prepare(context.Background(), runSpec); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return e
}

func runAll(t *testing.T, e *Engine, runSpec spec.RunSpec, sink engine.Sink) (engine.Result, error) {
	t.Helper()
	phases, err := engine.PlanPhases(runSpec.Load)
	if err != nil {
		t.Fatal(err)
	}
	return e.Run(context.Background(), phases, sink)
}

func errorOf(row engine.RequestRow) string {
	if row.Error == nil {
		return ""
	}
	return *row.Error
}

func describe(rows []engine.RequestRow) string {
	return fmt.Sprintf("%d rows", len(rows))
}

// runPrepared runs a prepared engine and returns its rows, failing on a run
// error or on no rows.
func runPrepared(t *testing.T, e *Engine, runSpec spec.RunSpec) []engine.RequestRow {
	t.Helper()
	sink := &collectingSink{}
	if _, err := runAll(t, e, runSpec, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := sink.snapshot()
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	return rows
}

// runRows prepares an engine and runs it.
func runRows(t *testing.T, options Options, runSpec spec.RunSpec) []engine.RequestRow {
	t.Helper()
	return runPrepared(t, prepare(t, options, runSpec), runSpec)
}

func requireAllTLSErrors(t *testing.T, options Options, runSpec spec.RunSpec) {
	t.Helper()
	for _, row := range runRows(t, options, runSpec) {
		if errorOf(row) != ErrorTLS {
			t.Fatalf("error %q, want tls", errorOf(row))
		}
	}
}
