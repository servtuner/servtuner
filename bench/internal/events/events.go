// Package events sends status events to the api. Delivery is best
// effort: a failure is logged and never fails the run, because the exit
// status of the bench process or Job is the final authority.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Header and scheme of the per-run token (required in both modes).
const (
	authorizationHeader = "Authorization"
	bearerPrefix        = "Bearer "
)

// Reporter receives one call per status event.
type Reporter interface {
	Report(ctx context.Context, step spec.PipelineStep, state spec.ReportedState, message string)
}

// Discard is the Reporter used when no api URL is given.
type Discard struct{}

func (Discard) Report(context.Context, spec.PipelineStep, spec.ReportedState, string) {}

// HTTPReporter POSTs events to {apiURL}/runs/{runID}/events.
type HTTPReporter struct {
	client   *http.Client
	endpoint string
	runID    string
	token    string
	logger   *slog.Logger
	now      func() time.Time

	mutex    sync.Mutex
	sequence int
}

// NewHTTPReporter returns a reporter. The client carries the hop's TLS mode
// (tlsconfig.NewHTTPClient). The token is sent as a bearer token and never
// logged.
func NewHTTPReporter(client *http.Client, apiURL, runID, token string, logger *slog.Logger, now func() time.Time) *HTTPReporter {
	return &HTTPReporter{
		client:   client,
		endpoint: strings.TrimRight(apiURL, "/") + "/runs/" + url.PathEscape(runID) + "/events",
		runID:    runID,
		token:    token,
		logger:   logger,
		now:      now,
	}
}

var _ Reporter = (*HTTPReporter)(nil)

// Report sends one event. Sequence numbers start at 0 and grow by one per
// event, so the api can order and de-duplicate them.
func (r *HTTPReporter) Report(ctx context.Context, step spec.PipelineStep, state spec.ReportedState, message string) {
	r.mutex.Lock()
	sequence := r.sequence
	r.sequence++
	r.mutex.Unlock()

	event := spec.Event{
		RunId:    r.runID,
		Sequence: sequence,
		Time:     spec.NewUTCTimestamp(r.now()),
		Step:     step,
		State:    state,
	}
	if message != "" {
		event.Message = &message
	}
	if err := r.send(ctx, event); err != nil {
		r.logger.Warn("status event not delivered; the run continues",
			"step", step, "state", state, "sequence", sequence, "error", err)
	}
}

func (r *HTTPReporter) send(ctx context.Context, event spec.Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if err := spec.Validate(spec.SchemaEvents, body); err != nil {
		return fmt.Errorf("event does not match its schema (bench bug): %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(authorizationHeader, bearerPrefix+r.token)
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("post to %s: %w", r.endpoint, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("post to %s: status %s", r.endpoint, response.Status)
	}
	return nil
}
