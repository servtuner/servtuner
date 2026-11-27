package loadgen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/loadgen/pacer"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

// TestRunNonStreamingWritesOneRowPerRequest: every scheduled hit becomes a row
// with usage, TTFB, no TTFT, the stage index and a small send lag.
func TestRunNonStreamingWritesOneRowPerRequest(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(0)), plainTLS, false, 100, time.Second)
	e := prepare(t, Options{RunID: "01TESTRUN"}, runSpec)
	sink := &collectingSink{}
	result, err := runAll(t, e, runSpec, sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := sink.snapshot()
	if len(rows) < 95 || len(rows) > 100 {
		t.Fatalf("%s, want about 99 (100 rps for 1s; the hit due at the boundary belongs to the next stage)", describe(rows))
	}
	for _, row := range rows {
		if row.Error != nil || row.Status != 200 {
			t.Fatalf("row failed: status %d error %q", row.Status, errorOf(row))
		}
		if row.Phase != engine.RowPhaseStage || row.StageIndex == nil || *row.StageIndex != 0 || row.TTFBSeconds == nil || row.TTFTSeconds != nil || len(row.ITLSeconds) != 0 {
			t.Fatalf("unexpected measurements: %+v", row)
		}
		if row.InputTokens == nil || *row.InputTokens != 20 || *row.OutputTokens != 8 {
			t.Fatalf("usage not recorded: %+v", row)
		}
		if row.RequestBytes == nil || *row.RequestBytes < 100 {
			t.Fatalf("request size not recorded: %+v", row)
		}
		if row.SentAt.Before(*row.ScheduledAt) || *row.SendLagSeconds < 0 || *row.SendLagSeconds > 0.25 {
			t.Fatalf("scheduled %v sent %v lag %v", *row.ScheduledAt, row.SentAt, *row.SendLagSeconds)
		}
		if row.ScheduledAt.Location() != time.UTC || row.SentAt.Location() != time.UTC {
			t.Fatal("times must be UTC")
		}
		if *row.TTFBSeconds > row.E2ESeconds {
			t.Fatalf("ttfb %v after e2e %v", *row.TTFBSeconds, row.E2ESeconds)
		}
	}
	if len(result.Windows) != 1 || !result.Windows[0].End.Equal(result.Windows[0].Start.Add(time.Second)) {
		t.Fatalf("windows = %+v, want one window of exactly the stage duration", result.Windows)
	}
	if e.HTTPVersion() != spec.HttpVersionA11 {
		t.Errorf("HTTPVersion = %q, want 1.1", e.HTTPVersion())
	}
	if offered := e.OfferedHits(); len(offered) != 1 || offered[0] != 100 {
		t.Errorf("OfferedHits = %v, want [100]", offered)
	}
}

// TestRunStreamingRecordsFirstTokenAndChunkGaps drives a real SSE response
// whose chunks are 20ms apart.
func TestRunStreamingRecordsFirstTokenAndChunkGaps(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // headers first: TTFB is before the first token
		time.Sleep(20 * time.Millisecond)
		for range 4 {
			_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"tok "}}]}` + "\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}` + "\n\ndata: [DONE]\n\n"))
	})
	runSpec := specFor(platformServer(t, handler), plainTLS, true, 5, time.Second)
	e := prepare(t, Options{}, runSpec)
	sink := &collectingSink{}
	if _, err := runAll(t, e, runSpec, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := sink.snapshot()
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range rows {
		if row.Error != nil {
			t.Fatalf("error %q", errorOf(row))
		}
		if row.TTFTSeconds == nil || row.TTFBSeconds == nil || *row.TTFBSeconds >= *row.TTFTSeconds {
			t.Fatalf("want ttfb before ttft: %+v", row)
		}
		if *row.TTFTSeconds < 0.015 {
			t.Errorf("ttft %v, want at least the 20ms the server waited", *row.TTFTSeconds)
		}
		if row.FirstContentSeconds == nil || *row.FirstContentSeconds != *row.TTFTSeconds {
			t.Fatalf("first content %v, ttft %v: want equal for plain content", row.FirstContentSeconds, *row.TTFTSeconds)
		}
		if len(row.ITLSeconds) != 3 {
			t.Fatalf("%d inter-token gaps, want 3", len(row.ITLSeconds))
		}
		if *row.OutputTokens != 4 {
			t.Errorf("output tokens %d", *row.OutputTokens)
		}
	}
}

// TestErrorClasses checks each class against a real failing server.
func TestErrorClasses(t *testing.T) {
	cases := []struct {
		name        string
		handler     http.Handler
		isStreaming bool
		options     string
		want        string
		wantStatus  int64
	}{
		{"http error", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }), false, "", "http_503", 503},
		{"stream error object", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"error":{"message":"boom","type":"server_error","code":"500"}}` + "\n\n"))
		}), true, "", ErrorStream, 200},
		{"truncated stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"a"}}]}` + "\n\n"))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // drops the connection mid-stream
		}), true, "", ErrorTruncated, 200},
		{"timeout", blockUntilClientGone, false, "timeout", ErrorTimeout, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSpec := specFor(platformServer(t, tc.handler), plainTLS, tc.isStreaming, 2, time.Second)
			if tc.options == "timeout" {
				runSpec.Engine.Options = spec.EngineOptions{OptionRequestTimeoutSeconds: 0.2}
			}
			rows := runRows(t, Options{}, runSpec)
			for _, row := range rows {
				if errorOf(row) != tc.want || row.Status != tc.wantStatus {
					t.Fatalf("error %q status %d, want %q %d", errorOf(row), row.Status, tc.want, tc.wantStatus)
				}
			}
		})
	}
}

func TestConnectionRefusedIsClassified(t *testing.T) {
	server := tlstest.NewPlainServer(t, usageHandler(0))
	endpoint := server.URL + "/v1"
	server.Close()
	runSpec := specFor(endpoint, plainTLS, false, 2, time.Second)
	for _, row := range runRows(t, Options{}, runSpec) {
		if errorOf(row) != ErrorConnectionRefused || row.Status != 0 {
			t.Fatalf("error %q status %d, want connection_refused with status 0", errorOf(row), row.Status)
		}
	}
}

// mtlsFixture is a server that demands a client certificate, and the files a
// client needs.
type mtlsFixture struct {
	endpoint                    string
	certFile, keyFile, caFile   string
	otherCertFile, otherKeyFile string
}

func newMTLSFixture(t *testing.T) mtlsFixture {
	t.Helper()
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, usageHandler(0))
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	otherCertFile, otherKeyFile := tlstest.NewAuthority(t).IssueClient(t).WriteFiles(t, directory, "other")
	return mtlsFixture{
		endpoint: server.URL + "/v1", certFile: certFile, keyFile: keyFile,
		caFile:        tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM),
		otherCertFile: otherCertFile, otherKeyFile: otherKeyFile,
	}
}

// TestMTLSMode: with a certificate the run works over HTTP/1.1; a certificate
// from another CA is refused by the server and recorded as a tls error; and
// Prepare refuses a spec that names a client certificate when the run has none.
func TestMTLSMode(t *testing.T) {
	fixture := newMTLSFixture(t)
	runSpec := specFor(fixture.endpoint, mtlsTLS, false, 5, time.Second)

	t.Run("with certificate", func(t *testing.T) {
		options := Options{ClientCertFile: fixture.certFile, ClientKeyFile: fixture.keyFile, CAFile: fixture.caFile}
		e := prepare(t, options, runSpec)
		for _, row := range runPrepared(t, e, runSpec) {
			if row.Error != nil {
				t.Fatalf("error %q", errorOf(row))
			}
		}
		if e.HTTPVersion() != spec.HttpVersionA11 {
			t.Errorf("HTTPVersion = %q, want 1.1 with a client certificate", e.HTTPVersion())
		}
	})

	t.Run("certificate from another CA is refused", func(t *testing.T) {
		requireAllTLSErrors(t, Options{ClientCertFile: fixture.otherCertFile, ClientKeyFile: fixture.otherKeyFile, CAFile: fixture.caFile}, runSpec)
	})

	t.Run("server not trusted", func(t *testing.T) {
		requireAllTLSErrors(t, Options{ClientCertFile: fixture.certFile, ClientKeyFile: fixture.keyFile}, runSpec) // no CAFile
	})

	t.Run("no certificate", func(t *testing.T) {
		err := New(Options{CAFile: fixture.caFile}).Prepare(context.Background(), runSpec)
		if err == nil || !strings.Contains(err.Error(), "client certificate") {
			t.Fatalf("Prepare without a certificate = %v, want a refusal", err)
		}
	})
}

// TestTLSMode: a server that asks for no client certificate is reached with
// the private CA and no certificate; without the CA the server is not
// trusted; and Prepare refuses a client certificate the spec does not name.
func TestTLSMode(t *testing.T) {
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewTLSServer(t, authority, usageHandler(0))
	caFile := tlstest.WriteFile(t, t.TempDir(), "ca.crt", authority.CertPEM)
	runSpec := specFor(server.URL+"/v1", serverTLS, false, 5, time.Second)

	t.Run("with the CA", func(t *testing.T) {
		e := prepare(t, Options{CAFile: caFile}, runSpec)
		for _, row := range runPrepared(t, e, runSpec) {
			if row.Error != nil {
				t.Fatalf("error %q", errorOf(row))
			}
		}
	})

	t.Run("server not trusted", func(t *testing.T) {
		requireAllTLSErrors(t, Options{}, runSpec)
	})

	t.Run("client certificate refused", func(t *testing.T) {
		certFile, keyFile := authority.IssueClient(t).WriteFiles(t, t.TempDir(), "client")
		err := New(Options{ClientCertFile: certFile, ClientKeyFile: keyFile, CAFile: caFile}).Prepare(context.Background(), runSpec)
		if err == nil || !strings.Contains(err.Error(), "tls") {
			t.Fatalf("Prepare with a client certificate the spec does not name = %v, want a refusal", err)
		}
	})
}

func TestPlatformModeRejectsCertificateSettings(t *testing.T) {
	runSpec := specFor("http://192.0.2.1:8000/v1", plainTLS, false, 5, time.Second)
	err := New(Options{ClientCertFile: "tls.crt", ClientKeyFile: "tls.key"}).Prepare(context.Background(), runSpec)
	if err == nil {
		t.Fatal("tls disabled accepted certificate settings")
	}
}

func TestModeAndSchemeMustAgree(t *testing.T) {
	err := New(Options{}).Prepare(context.Background(), specFor("https://192.0.2.1/v1", plainTLS, false, 5, time.Second))
	if err == nil {
		t.Fatal("tls disabled accepted an https url")
	}
}

// TestInFlightCapDelaysButNeverDrops: with a cap of 2 and a slow target, every
// scheduled request is still sent, and the delay shows up as send lag.
func TestInFlightCapDelaysButNeverDrops(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(100*time.Millisecond)), plainTLS, false, 50, time.Second)
	runSpec.Engine.Options = spec.EngineOptions{OptionMaxInFlight: 2, OptionMaxWaiting: 1000}
	e := prepare(t, Options{}, runSpec)
	sink := &collectingSink{}
	if _, err := runAll(t, e, runSpec, sink); err != nil {
		t.Fatal(err)
	}
	rows := sink.snapshot()
	if len(rows) < 45 {
		t.Fatalf("%s: requests were dropped under the cap", describe(rows))
	}
	var maxLag float64
	for _, row := range rows {
		maxLag = max(maxLag, *row.SendLagSeconds)
	}
	if maxLag < 0.3 {
		t.Errorf("max send lag %.3fs, want the cap's queueing to show", maxLag)
	}
	if e.Stats().CapWaits == 0 {
		t.Error("CapWaits = 0 although the cap was hit")
	}
}

// TestWarmupRowsAreWrittenAsWarmup: warmup stays in raw data, with phase warmup
// and no stage index; it is counted and teaches the calibrator.
func TestWarmupRowsAreWrittenAsWarmup(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(0)), plainTLS, false, 20, 500*time.Millisecond)
	runSpec.Load.Warmup = "500ms"
	e := prepare(t, Options{}, runSpec)
	sink := &collectingSink{}
	result, err := runAll(t, e, runSpec, sink)
	if err != nil {
		t.Fatal(err)
	}
	stats := e.Stats()
	if stats.WarmupRequests < 5 || stats.WarmupErrors != 0 {
		t.Errorf("stats = %+v, want warmup requests counted", stats)
	}
	var warmupRows, stageRows int64
	for _, row := range sink.snapshot() {
		switch row.Phase {
		case engine.RowPhaseWarmup:
			warmupRows++
			if row.StageIndex != nil {
				t.Fatalf("warmup row has stage_index %d", *row.StageIndex)
			}
		case engine.RowPhaseStage:
			stageRows++
			if row.StageIndex == nil || *row.StageIndex != 0 {
				t.Fatalf("stage row has stage_index %v", row.StageIndex)
			}
		default:
			t.Fatalf("row phase %q", row.Phase)
		}
	}
	if warmupRows != stats.WarmupRequests || stageRows == 0 {
		t.Errorf("%d warmup rows for %d warmup requests, %d stage rows", warmupRows, stats.WarmupRequests, stageRows)
	}
	if len(result.Windows) != 2 || !result.Windows[0].End.Equal(result.Windows[1].Start) {
		t.Fatalf("windows %+v, want warmup then stage, touching", result.Windows)
	}
	if info := e.WorkloadInfo(); info.CharsPerToken == nil {
		t.Error("chars_per_token not calibrated from warmup usage")
	}
}

func TestVLLMTargetsGetMinTokens(t *testing.T) {
	var body string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := make([]byte, 1<<16)
		count, _ := r.Body.Read(buffer)
		body = string(buffer[:count])
		_, _ = w.Write([]byte(chatUsageBody))
	})
	runSpec := specFor(platformServer(t, handler), plainTLS, false, 10, 300*time.Millisecond)
	runSpec.Target.Type = spec.TargetTypeVllm
	e := prepare(t, Options{RunID: "run-1", BearerToken: "secret"}, runSpec)
	if _, err := runAll(t, e, runSpec, &collectingSink{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "min_tokens") {
		t.Errorf("vllm request body lacks min_tokens: %.200s", body)
	}
}

func TestBearerTokenIsSent(t *testing.T) {
	var header string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(chatUsageBody))
	})
	runSpec := specFor(platformServer(t, handler), plainTLS, false, 10, 300*time.Millisecond)
	e := prepare(t, Options{BearerToken: "secret"}, runSpec)
	if _, err := runAll(t, e, runSpec, &collectingSink{}); err != nil {
		t.Fatal(err)
	}
	if header != "Bearer secret" {
		t.Errorf("Authorization = %q", header)
	}
}

// TestSelfMonitoringSamplesEachInterval.
func TestSelfMonitoringSamplesEachInterval(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(0)), plainTLS, false, 50, time.Second)
	clientSink := &collectingClientSink{}
	e := prepare(t, Options{ClientSink: clientSink, SampleInterval: 100 * time.Millisecond}, runSpec)
	if _, err := runAll(t, e, runSpec, &collectingSink{}); err != nil {
		t.Fatal(err)
	}
	clientSink.mutex.Lock()
	defer clientSink.mutex.Unlock()
	if len(clientSink.samples) < 8 {
		t.Fatalf("%d samples in a 1s run at 100ms, want about 10", len(clientSink.samples))
	}
	sawStage := false
	for _, sample := range clientSink.samples {
		if sample.Source != SampleSource || sample.Goroutines <= 0 || sample.SendLagMaxSeconds < sample.SendLagP99Seconds {
			t.Fatalf("bad sample %+v", sample)
		}
		if sample.StageIndex != nil && *sample.StageIndex == 0 {
			sawStage = true
		}
	}
	if !sawStage {
		t.Error("no sample carried stage_index 0")
	}
}

// TestCancelStopsPacingAndKeepsRows: cancelling mid-stage returns promptly, with
// the rows already written and the window cut short.
func TestCancelStopsPacingAndKeepsRows(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(0)), plainTLS, false, 100, time.Minute)
	e := prepare(t, Options{}, runSpec)
	phases, err := engine.PlanPhases(runSpec.Load)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	sink := &collectingSink{}
	began := time.Now()
	result, err := e.Run(ctx, phases, sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(began); elapsed > 3*time.Second {
		t.Fatalf("Run took %v after cancel", elapsed)
	}
	if rows := sink.snapshot(); len(rows) < 10 || len(rows) > 40 {
		t.Errorf("%s kept, want the ~30 sent before the cancel", describe(sink.snapshot()))
	}
	if len(result.Windows) != 1 || result.Windows[0].End.Sub(result.Windows[0].Start) > 2*time.Second {
		t.Errorf("windows = %+v, want one cut-short window", result.Windows)
	}
}

// TestSinkFailureStopsTheRun: a sink that cannot persist rows fails the run.
func TestSinkFailureStopsTheRun(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(0)), plainTLS, false, 100, time.Minute)
	e := prepare(t, Options{}, runSpec)
	sink := &collectingSink{err: errors.New("disk full")}
	began := time.Now()
	_, err := runAll(t, e, runSpec, sink)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Run error = %v, want the sink failure", err)
	}
	if time.Since(began) > 5*time.Second {
		t.Error("run kept pacing after the sink failed")
	}
}

// TestDrainAbandonsRequestsThatNeverFinish: after the drain time the run ends,
// and the stuck requests, which were sent, are recorded as abandoned with the
// time they waited.
func TestDrainAbandonsRequestsThatNeverFinish(t *testing.T) {
	runSpec := specFor(platformServer(t, blockUntilClientGone), plainTLS, false, 5, 400*time.Millisecond)
	runSpec.Engine.Options = spec.EngineOptions{OptionDrainTimeoutSeconds: 0.3}
	e := prepare(t, Options{}, runSpec)
	began := time.Now()
	rows := runPrepared(t, e, runSpec)
	if time.Since(began) > 5*time.Second {
		t.Error("drain did not end the run")
	}
	if got := e.Stats().Abandoned; got != int64(len(rows)) {
		t.Errorf("abandoned %d, rows %d: every sent request needs a row", got, len(rows))
	}
	for _, row := range rows {
		if errorOf(row) != ErrorAbandoned || row.Status != 0 || row.E2ESeconds <= 0 || row.SentAt.IsZero() {
			t.Fatalf("row = %+v, want an abandoned row with its sent_at and a positive e2e", row)
		}
	}
}

// TestScheduleMatchesOfferedHits: for every pacing the number of scheduled
// hits is within one of the stage's expected hits, and none is late-adjusted.
func TestScheduleMatchesOfferedHits(t *testing.T) {
	stage := func(rate float64) spec.Stage { return spec.Stage{Rate: rate, Duration: "10s"} }
	cases := map[string]spec.Load{
		"constant": {Pacing: spec.PacingConstant, Stages: []spec.Stage{stage(100)}},
		"poisson":  {Pacing: spec.PacingPoisson, Stages: []spec.Stage{stage(100)}},
		"linear":   {Pacing: spec.PacingLinear, Stages: []spec.Stage{stage(50), stage(150)}},
		"sine":     {Pacing: spec.PacingSine, Sine: &spec.SinePacing{AmplitudeRatio: 0.5, Period: "5s"}, Stages: []spec.Stage{stage(100)}},
	}
	for name, load := range cases {
		t.Run(name, func(t *testing.T) {
			stagePacers, err := pacer.ForLoad(load, 7)
			if err != nil {
				t.Fatal(err)
			}
			last := stagePacers[len(stagePacers)-1]
			var count int
			var previous time.Duration
			for offset := range scheduleOffsets(last) {
				if offset < previous || offset >= last.Duration {
					t.Fatalf("offset %v after %v outside the stage", offset, previous)
				}
				previous = offset
				count++
			}
			offered := pacer.OfferedHits(last)
			tolerance := 3.0
			if name == "poisson" {
				tolerance = 40 // sqrt(1000) is about 32 for a Poisson count
			}
			if diff := float64(count) - offered; diff > tolerance || diff < -tolerance {
				t.Errorf("scheduled %d hits, offered %.1f", count, offered)
			}
		})
	}
}

// blockUntilClientGone never answers. The body is drained first: the server
// only notices a closed connection once the request body has been read.
var blockUntilClientGone = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	<-r.Context().Done()
})

// stallingStream sends one token and then goes silent until the client leaves.
var stallingStream = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"a"}}]}` + "\n\n"))
	w.(http.Flusher).Flush()
	<-r.Context().Done()
})

// TestSecondPhaseKeepsTheMonotonicClock: the instant the next phase starts from
// must still carry its monotonic reading; .UTC() would strip it and turn
// scheduling into wall-clock arithmetic.
func TestSecondPhaseKeepsTheMonotonicClock(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(0)), plainTLS, false, 10, 100*time.Millisecond)
	e := prepare(t, Options{}, runSpec)
	run, stop := newRunState(context.Background(), e, &collectingSink{})
	defer stop()
	phases, err := engine.PlanPhases(runSpec.Load)
	if err != nil {
		t.Fatal(err)
	}
	start := e.now()
	window, nextStart, err := run.runPhase(phases[0], start)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nextStart.String(), "m=") {
		t.Errorf("start of the second phase %q has no monotonic reading", nextStart)
	}
	if strings.Contains(window.End.String(), "m=") || window.End.Location() != time.UTC || !window.End.Equal(nextStart) {
		t.Errorf("window end %q: want UTC for the record, equal to the next start", window.End)
	}
}

// TestBlockingSinkDoesNotHangTheRun: a sink that never returns costs the run
// its drain time plus a short grace, then Run reports the stuck sink.
func TestBlockingSinkDoesNotHangTheRun(t *testing.T) {
	runSpec := specFor(platformServer(t, usageHandler(0)), plainTLS, false, 200, time.Second)
	runSpec.Engine.Options = spec.EngineOptions{OptionMaxInFlight: 20, OptionDrainTimeoutSeconds: 0.5}
	e := prepare(t, Options{}, runSpec)
	release := make(chan struct{})
	defer close(release)
	phases, _ := engine.PlanPhases(runSpec.Load)
	began := time.Now()
	_, err := e.Run(context.Background(), phases, blockingSink{release: release})
	if err == nil || !strings.Contains(err.Error(), "sink stuck") {
		t.Fatalf("Run error = %v, want the stuck sink named", err)
	}
	if elapsed := time.Since(began); elapsed > 2500*time.Millisecond {
		t.Errorf("Run took %v with a blocked sink, want about stage + drain (1.5s)", elapsed)
	}
	if e.Stats().Lost == 0 {
		t.Error("Lost = 0, want the unwritten rows counted")
	}
}

type blockingSink struct{ release chan struct{} }

func (s blockingSink) Write(engine.RequestRow) error { <-s.release; return nil }

// TestShedBoundsGoroutinesAgainstADeadTarget: against a target that never
// answers, goroutines stay bounded and the surplus due hits are shed, not
// parked.
func TestShedBoundsGoroutinesAgainstADeadTarget(t *testing.T) {
	runSpec := specFor(platformServer(t, blockUntilClientGone), plainTLS, false, 1000, time.Second)
	runSpec.Engine.Options = spec.EngineOptions{OptionMaxInFlight: 10, OptionMaxWaiting: 10, OptionDrainTimeoutSeconds: 0.3}
	e := prepare(t, Options{}, runSpec)
	baseline := runtime.NumGoroutine()
	var peak atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				peak.Store(max(peak.Load(), int64(runtime.NumGoroutine())))
			}
		}
	}()
	rows := runPrepared(t, e, runSpec)
	close(stop)
	stats := e.Stats()
	if stats.Shed == 0 {
		t.Errorf("Shed = 0 with 1000 rps against a dead target and a cap of 10: %+v", stats)
	}
	// Each request in flight also costs the transport's and the in-process
	// server's goroutines, so the bound is per slot, not per hit (1000 hits).
	if growth := peak.Load() - int64(baseline); growth > 10*6+10+30 {
		t.Errorf("goroutines grew by %d, want a bound near cap + waiting", growth)
	}
	for _, row := range rows {
		if errorOf(row) != ErrorAbandoned {
			t.Fatalf("row error %q, want abandoned (the target never answers)", errorOf(row))
		}
	}
}

// TestMidStreamTimeoutKeepsStatusAndTimings: the response was a 200 and gave a
// first token before it stalled, so the row says timeout with both facts.
func TestMidStreamTimeoutKeepsStatusAndTimings(t *testing.T) {
	runSpec := specFor(platformServer(t, stallingStream), plainTLS, true, 5, time.Second)
	runSpec.Engine.Options = spec.EngineOptions{OptionRequestTimeoutSeconds: 0.3}
	for _, row := range runRows(t, Options{}, runSpec) {
		if errorOf(row) != ErrorTimeout || row.Status != 200 || row.TTFTSeconds == nil {
			t.Fatalf("row = %+v, want timeout with status 200 and a ttft", row)
		}
	}
}

func TestDNSFailureIsClassified(t *testing.T) {
	runSpec := specFor("http://servtuner-no-such-host.invalid/v1", plainTLS, false, 5, time.Second)
	for _, row := range runRows(t, Options{}, runSpec) {
		if errorOf(row) != ErrorDNS || row.Status != 0 {
			t.Fatalf("error %q status %d, want dns with status 0", errorOf(row), row.Status)
		}
	}
}

// TestCancelMidStreamAbandonsAfterDrain: cancelling while streams are open
// stops the pacing; once the drain time passes, each open stream is recorded as
// abandoned, keeping the timings it had (raw first).
func TestCancelMidStreamAbandonsAfterDrain(t *testing.T) {
	runSpec := specFor(platformServer(t, stallingStream), plainTLS, true, 20, time.Minute)
	runSpec.Engine.Options = spec.EngineOptions{OptionDrainTimeoutSeconds: 0.3}
	e := prepare(t, Options{}, runSpec)
	phases, _ := engine.PlanPhases(runSpec.Load)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(400*time.Millisecond, cancel)
	sink := &collectingSink{}
	if _, err := e.Run(ctx, phases, sink); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	rows := sink.snapshot()
	if len(rows) == 0 || int64(len(rows)) != e.Stats().Abandoned {
		t.Fatalf("%d rows, %d abandoned: every cut-off stream needs a row", len(rows), e.Stats().Abandoned)
	}
	for _, row := range rows {
		if errorOf(row) != ErrorAbandoned || row.Status != 0 || row.TTFTSeconds == nil {
			t.Fatalf("row = %+v, want abandoned with the ttft it had", row)
		}
	}
}

func TestDefaultCapsFitTheContainerMemoryLimit(t *testing.T) {
	// 10k RPS x 10s expected latency would be 100k slots; 1 GiB affords far fewer.
	runSpec := specFor("http://192.0.2.1/v1", plainTLS, false, 10000, time.Second)
	limits, err := resolveLimits(runSpec, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	budget := float64(1<<30) * memoryBudgetFraction
	if used := float64(limits.maxInFlight*bytesPerInFlight + limits.maxWaiting*bytesPerWaiting); used > budget {
		t.Errorf("caps %d in flight + %d waiting need %.0f bytes, over the %.0f budget", limits.maxInFlight, limits.maxWaiting, used, budget)
	}
	if !limits.isMemoryCapped || limits.maxInFlight < 1000 {
		t.Errorf("limits = %+v, want a memory cap of several thousand", limits)
	}
}

func TestNoMemoryLimitKeepsTheLittleCap(t *testing.T) {
	limits, err := resolveLimits(specFor("http://192.0.2.1/v1", plainTLS, false, 10000, time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	if limits.isMemoryCapped || limits.maxInFlight != 10000*int(DefaultExpectedLatency.Seconds()) {
		t.Errorf("limits = %+v, want rate x expected latency", limits)
	}
}

func TestExplicitMaxInFlightIsNeverReducedByMemory(t *testing.T) {
	runSpec := specFor("http://192.0.2.1/v1", plainTLS, false, 10000, time.Second)
	runSpec.Engine.Options = spec.EngineOptions{OptionMaxInFlight: float64(50000)}
	limits, err := resolveLimits(runSpec, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if limits.maxInFlight != 50000 || limits.isMemoryCapped {
		t.Errorf("limits = %+v, want the explicit 50000", limits)
	}
}
