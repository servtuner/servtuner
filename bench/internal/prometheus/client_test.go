package prometheus

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

func TestQueryRangeInMTLSMode(t *testing.T) {
	fake := answerEverything()
	client := newMTLSClient(t, fake)
	matrix, _, err := client.QueryRange(context.Background(), "up", testWindow, 5*time.Second)
	if err != nil {
		t.Fatalf("mtls query failed: %v", err)
	}
	if len(matrix) != 1 || matrix[0].Metric["pod"] != "vllm-0" {
		t.Errorf("matrix = %v", matrix)
	}
}

// TestMTLSRefusesWithoutAcceptedCertificate checks both ways of having no
// usable client certificate: the hop cannot even be built without one, and a
// certificate from the wrong CA is turned away by the server before any request
// reaches the API ("Tests").
func TestMTLSRefusesWithoutAcceptedCertificate(t *testing.T) {
	fake := answerEverything()
	serverAuthority := tlstest.NewAuthority(t)
	acceptedAuthority := tlstest.NewAuthority(t)
	strangerAuthority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, serverAuthority, acceptedAuthority, fake.Handler())
	directory := t.TempDir()
	caFile := tlstest.WriteFile(t, directory, "ca.crt", serverAuthority.CertPEM)

	if _, err := NewClient(tlsconfig.Hop{Name: "prometheus", Mode: spec.TransportModeMtls, URL: server.URL, CAFile: caFile}, fastOptions); err == nil ||
		!strings.Contains(err.Error(), "certificate") {
		t.Errorf("a client with no certificate must not be built, got %v", err)
	}

	certFile, keyFile := strangerAuthority.IssueClient(t).WriteFiles(t, directory, "stranger")
	client, err := NewClient(tlsconfig.Hop{Name: "prometheus", Mode: spec.TransportModeMtls, URL: server.URL, CertFile: certFile, KeyFile: keyFile, CAFile: caFile}, fastOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.QueryRange(context.Background(), "up", testWindow, 5*time.Second); err == nil {
		t.Fatal("the server accepted a certificate from an unknown CA")
	}
	if requests := fake.Requests(); len(requests) != 0 {
		t.Errorf("a refused client reached the API: %v", requests)
	}
}

func TestPlatformModeIsPlainHTTPAndRefusesCertificates(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	if _, _, err := client.QueryRange(context.Background(), "up", testWindow, 5*time.Second); err != nil {
		t.Fatalf("platform query failed: %v", err)
	}
	mixed := tlsconfig.Hop{Name: "prometheus", Mode: spec.TransportModePlatform, URL: "http://prometheus.invalid", CertFile: "c", KeyFile: "k"}
	if _, err := NewClient(mixed, fastOptions); err == nil {
		t.Error("platform mode with certificate settings must be rejected")
	}
	if _, err := NewClient(tlsconfig.Hop{Name: "prometheus", Mode: spec.TransportModeMtls, URL: "http://prometheus.invalid", CertFile: "c", KeyFile: "k"}, fastOptions); err == nil {
		t.Error("mtls mode with an http:// URL must be rejected")
	}
}

func TestQueryRangeSendsTheWindowAndStep(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	if _, _, err := client.QueryRange(context.Background(), `up{job="servtuner-targets"}`, testWindow, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %v", requests)
	}
	got := requests[0]
	if got.Query != `up{job="servtuner-targets"}` || got.Step != "5" || got.Start != "1793188800" || got.End != "1793189400" {
		t.Errorf("request = %+v", got)
	}
	if _, _, err := client.QueryRange(context.Background(), "up", testWindow, 0); err == nil {
		t.Error("a zero step must be rejected")
	}
	if _, _, err := client.QueryRange(context.Background(), "up", Window{Start: testWindow.End, End: testWindow.Start}, time.Second); err == nil {
		t.Error("an inverted window must be rejected")
	}
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	fake := answerEverything()
	fake.FailNext(http.StatusServiceUnavailable, http.StatusInternalServerError)
	client := newPlatformClient(t, fake, nil)
	if _, _, err := client.QueryRange(context.Background(), "up", testWindow, 5*time.Second); err != nil {
		t.Fatalf("query should survive two 5xx answers: %v", err)
	}
	if got := len(fake.Requests()); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
}

func TestRetriesAreBounded(t *testing.T) {
	fake := answerEverything()
	fake.FailNext(500, 500, 500, 500, 500, 500)
	client := newPlatformClient(t, fake, nil)
	_, _, err := client.QueryRange(context.Background(), "up", testWindow, 5*time.Second)
	if err == nil {
		t.Fatal("a persistent 5xx must fail")
	}
	if got := len(fake.Requests()); got != fastOptions.MaxAttempts {
		t.Errorf("requests = %d, want %d", got, fastOptions.MaxAttempts)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusTooManyRequests} {
		fake := answerEverything()
		fake.ErrorFor = func(string) (int, string) { return status, "bad query" }
		client := newPlatformClient(t, fake, nil)
		_, _, err := client.QueryRange(context.Background(), "up{", testWindow, 5*time.Second)
		if err == nil {
			t.Errorf("status %d: the query must fail", status)
		}
		if got := len(fake.Requests()); got != 1 {
			t.Errorf("status %d: requests = %d, want 1", status, got)
		}
	}
}

func TestRetriesATimeout(t *testing.T) {
	var calls atomic.Int32
	hangFirst := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm() // reading the body lets the server notice that the client gave up
			if calls.Add(1) == 1 {
				<-r.Context().Done()
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	server := tlstest.NewPlatformServer(t, hangFirst(answerEverything().Handler()))
	client, err := NewClient(tlsconfig.Hop{Name: "prometheus", Mode: spec.TransportModePlatform, URL: server.URL},
		Options{RequestTimeout: 100 * time.Millisecond, MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.QueryRange(context.Background(), "up", testWindow, 5*time.Second); err != nil {
		t.Fatalf("query should survive one timeout: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestACancelledContextStopsRetrying(t *testing.T) {
	fake := answerEverything()
	fake.FailNext(500, 500, 500, 500)
	client := newPlatformClient(t, fake, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := client.QueryRange(ctx, "up", testWindow, 5*time.Second); err == nil {
		t.Fatal("a cancelled context must fail the query")
	}
	if got := len(fake.Requests()); got > 1 {
		t.Errorf("requests = %d after cancellation", got)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func TestShouldRetry(t *testing.T) {
	background := context.Background()
	cancelled, cancel := context.WithCancel(background)
	cancel()
	cases := []struct {
		name     string
		ctx      context.Context
		response *http.Response
		err      error
		want     bool
	}{
		{"500", background, &http.Response{StatusCode: 500}, nil, true},
		{"503", background, &http.Response{StatusCode: 503}, nil, true},
		{"200", background, &http.Response{StatusCode: 200}, nil, false},
		{"400", background, &http.Response{StatusCode: 400}, nil, false},
		{"422", background, &http.Response{StatusCode: 422}, nil, false},
		{"429", background, &http.Response{StatusCode: 429}, nil, false},
		{"timeout", background, nil, timeoutError{}, true},
		{"other error", background, nil, errors.New("tls: bad certificate"), false},
		{"cancelled", cancelled, &http.Response{StatusCode: 503}, nil, false},
	}
	for _, c := range cases {
		if got := shouldRetry(c.ctx, c.response, c.err); got != c.want {
			t.Errorf("%s: shouldRetry = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	client := retryingClient{initialBackoff: 500 * time.Millisecond, maxBackoff: 4 * time.Second}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	for i, expected := range want {
		if got := client.backoff(i + 1); got != expected {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, expected)
		}
	}
}

func TestOptionsDefaults(t *testing.T) {
	got := Options{}.withDefaults()
	if got.RequestTimeout != defaultRequestTimeout || got.MaxAttempts != defaultMaxAttempts ||
		got.InitialBackoff != defaultInitialBackoff || got.MaxBackoff != defaultMaxBackoff || got.Now == nil {
		t.Errorf("defaults = %+v", got)
	}
}

func TestLongWindowsAreSplitUnderThePointLimit(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	window := Window{Start: testWindow.Start, End: testWindow.Start.Add(25000 * time.Second)}
	matrix, _, err := client.QueryRange(context.Background(), "up", window, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	startUnix := testWindow.Start.Unix()
	wantRanges := [][2]int64{{startUnix, startUnix + 10000}, {startUnix + 10001, startUnix + 20001}, {startUnix + 20002, startUnix + 25000}}
	requests := fake.Requests()
	if len(requests) != len(wantRanges) {
		t.Fatalf("requests = %d, want %d", len(requests), len(wantRanges))
	}
	for i, want := range wantRanges {
		if requests[i].Start != strconv.FormatInt(want[0], 10) || requests[i].End != strconv.FormatInt(want[1], 10) {
			t.Errorf("chunk %d = %s..%s, want %d..%d", i, requests[i].Start, requests[i].End, want[0], want[1])
		}
	}
	// The fake answers every chunk with the same series, so they must be joined
	// into one series holding one sample per chunk.
	if len(matrix) != 1 || len(matrix[0].Values) != len(wantRanges) {
		t.Errorf("matrix = %v", matrix)
	}
}

func TestAWindowOfExactlyOneChunkIsOneRequest(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	window := Window{Start: testWindow.Start, End: testWindow.Start.Add(maxPointsPerChunk * time.Second)}
	if _, _, err := client.QueryRange(context.Background(), "up", window, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.Requests()); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}
