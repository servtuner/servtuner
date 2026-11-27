package events

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

const (
	testRunID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testToken = "run-token-value"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC) }

// capture records what the server received.
type capture struct {
	mutex    sync.Mutex
	paths    []string
	auth     []string
	events   []spec.Event
	respond  int
	rawBodys []string
}

func (c *capture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mutex.Lock()
		defer c.mutex.Unlock()
		c.paths = append(c.paths, r.Method+" "+r.URL.Path)
		c.auth = append(c.auth, r.Header.Get("Authorization"))
		c.rawBodys = append(c.rawBodys, string(body))
		var event spec.Event
		if err := json.Unmarshal(body, &event); err == nil {
			c.events = append(c.events, event)
		}
		status := c.respond
		if status == 0 {
			status = http.StatusAccepted
		}
		w.WriteHeader(status)
	})
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func reportThreeEvents(reporter Reporter) {
	ctx := context.Background()
	reporter.Report(ctx, spec.PipelineStepResolve, spec.ReportedStateRunning, "")
	reporter.Report(ctx, spec.PipelineStepCollect, spec.ReportedStateCollecting, "settling")
	reporter.Report(ctx, spec.PipelineStepFinalize, spec.ReportedStateSucceeded, "")
}

func checkDelivered(t *testing.T, received *capture) {
	t.Helper()
	wantPath := "POST /runs/" + testRunID + "/events"
	if len(received.events) != 3 {
		t.Fatalf("got %d events, want 3", len(received.events))
	}
	for index, event := range received.events {
		if received.paths[index] != wantPath {
			t.Errorf("path = %q, want %q", received.paths[index], wantPath)
		}
		if received.auth[index] != "Bearer "+testToken {
			t.Errorf("Authorization header = %q", received.auth[index])
		}
		if event.Sequence != index || event.RunId != testRunID {
			t.Errorf("event %d = %+v", index, event)
		}
		if err := spec.Validate(spec.SchemaEvents, []byte(received.rawBodys[index])); err != nil {
			t.Errorf("event %d does not match the schema: %v", index, err)
		}
	}
	if received.events[1].Message == nil || *received.events[1].Message != "settling" {
		t.Errorf("message not carried: %+v", received.events[1])
	}
}

func TestEventsDeliveredInPlatformMode(t *testing.T) {
	received := &capture{}
	server := tlstest.NewPlainServer(t, received.handler())
	client, err := tlsconfig.NewHTTPClient(tlsconfig.Hop{Name: "api", URL: server.URL}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reportThreeEvents(NewHTTPReporter(client, server.URL+"/", testRunID, testToken, discardLogger(), fixedNow))
	checkDelivered(t, received)
}

func TestEventsDeliveredInMTLSMode(t *testing.T) {
	received := &capture{}
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, received.handler())
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	client, err := tlsconfig.NewHTTPClient(tlsconfig.Hop{
		Name: "api", TLS: true, URL: server.URL,
		CertFile: certFile, KeyFile: keyFile, CAFile: tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM),
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reportThreeEvents(NewHTTPReporter(client, server.URL, testRunID, testToken, discardLogger(), fixedNow))
	checkDelivered(t, received)
}

// logCapture collects log output so tests can check what is, and is not, logged.
type logCapture struct {
	mutex sync.Mutex
	text  strings.Builder
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.text.Write(p)
}

func TestDeliveryFailureIsLoggedNotFatalAndNeverLogsTheToken(t *testing.T) {
	received := &capture{respond: http.StatusInternalServerError}
	server := tlstest.NewPlainServer(t, received.handler())
	client, _ := tlsconfig.NewHTTPClient(tlsconfig.Hop{Name: "api", URL: server.URL}, 5*time.Second)
	logs := &logCapture{}
	reporter := NewHTTPReporter(client, server.URL, testRunID, testToken, slog.New(slog.NewTextHandler(logs, nil)), fixedNow)
	reportThreeEvents(reporter) // must return normally
	if !strings.Contains(logs.text.String(), "not delivered") {
		t.Errorf("failure not logged: %q", logs.text.String())
	}
	if strings.Contains(logs.text.String(), testToken) {
		t.Error("the run token was logged")
	}
	if len(received.events) != 3 || received.events[2].Sequence != 2 {
		t.Errorf("sequence must advance across failures: %+v", received.events)
	}
}

func TestUnreachableApiDoesNotPanicOrBlock(t *testing.T) {
	server := tlstest.NewPlainServer(t, http.NotFoundHandler())
	url := server.URL
	server.Close()
	client, _ := tlsconfig.NewHTTPClient(tlsconfig.Hop{Name: "api", URL: url}, time.Second)
	reportThreeEvents(NewHTTPReporter(client, url, testRunID, testToken, discardLogger(), fixedNow))
}

func TestDiscardReporter(t *testing.T) {
	reportThreeEvents(Discard{})
}
