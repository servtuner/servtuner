package loadgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/loadgen/sse"
	"github.com/servtuner/servtuner/bench/internal/workload"
)

const (
	// maxNonStreamingBodyBytes bounds how much of a non-streaming response is
	// read to find usage.
	maxNonStreamingBodyBytes = 16 << 20
	// maxErrorBodyBytes bounds how much of an error response is drained so the
	// connection can be reused.
	maxErrorBodyBytes = 1048576 // 1 MiB
)

// clockFunc lets the SSE parser read the run's one clock.
type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

// outcome is what one request measured.
type outcome struct {
	scheduledAt, sentAt time.Time
	status              int
	errorClass          string
	ttfb, ttft          *time.Duration
	firstContent        *time.Duration
	interTokenLatencies []time.Duration
	e2e                 time.Duration
	usage               *sse.Usage
}

// slotOutcome says what became of a due request's wait for an in-flight slot.
type slotOutcome int

const (
	slotAcquired slotOutcome = iota
	// slotUnsent: the run was cancelled or failed before a slot freed.
	slotUnsent
	// slotShed: too many requests were already waiting.
	slotShed
)

// execute sends one due request and records it. The slot is released as soon as
// the response is read, before the row is queued for the sink.
func (r *runState) execute(phase engine.Phase, scheduledAt time.Time) {
	defer r.pending.Done()
	switch r.acquireSlot(scheduledAt) {
	case slotUnsent:
		r.engine.counters.unsent.Add(1)
		return
	case slotShed:
		r.engine.counters.shed.Add(1)
		r.engine.warnShedOnce()
		return
	}
	request := r.engine.generator.Next()
	result := r.send(request, scheduledAt)
	<-r.slots
	r.record(phase, request, result)
}

// acquireSlot takes an in-flight slot. When the cap is full the request waits
// here, not in the pacer, so the schedule is never slowed; its send lag grows
// instead. At most maxWaiting requests wait: a due request beyond that is shed,
// which bounds the goroutines against a dead target (doc.go).
func (r *runState) acquireSlot(scheduledAt time.Time) slotOutcome {
	select {
	case r.slots <- struct{}{}:
		return slotAcquired
	default:
	}
	if r.waiting.Add(1) > int64(r.engine.limits.maxWaiting) {
		r.waiting.Add(-1)
		return slotShed
	}
	defer r.waiting.Add(-1)
	r.engine.counters.capWaits.Add(1)
	stopWaiting := r.lags.startWaiting(scheduledAt)
	defer stopWaiting()
	select {
	case r.slots <- struct{}{}:
		return slotAcquired
	case <-r.pacingCtx.Done():
		return slotUnsent
	case <-r.abortCtx.Done():
		return slotUnsent
	}
}

// send performs the exchange and measures it. Times are readings of the run's
// clock; sent_at is taken immediately before RoundTrip (doc.go).
func (r *runState) send(request workload.Request, scheduledAt time.Time) outcome {
	e := r.engine
	requestCtx, cancel := context.WithTimeout(r.abortCtx, e.limits.requestTimeout)
	defer cancel()

	result := outcome{scheduledAt: scheduledAt}
	var sentAt time.Time
	var firstByteOffset atomic.Int64
	firstByteOffset.Store(-1)
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() { firstByteOffset.Store(int64(e.now().Sub(sentAt))) },
	}
	httpRequest, err := e.newHTTPRequest(httptrace.WithClientTrace(requestCtx, trace), request)
	if err != nil {
		result.sentAt = e.now()
		result.errorClass = ErrorConnection
		return result
	}

	sentAt = e.now()
	result.sentAt = sentAt
	r.lags.recordSent(sentAt.Sub(scheduledAt))
	r.inFlight.Add(1)
	defer r.inFlight.Add(-1)

	response, err := e.transport.RoundTrip(httpRequest)
	if err != nil {
		result.e2e = e.now().Sub(sentAt)
		result.errorClass = classifyTransportError(err)
		r.markIfAbandoned(&result)
		return result
	}
	defer response.Body.Close()
	e.httpProtocol.Store(int64(response.ProtoMajor))
	result.status = response.StatusCode
	if offset := firstByteOffset.Load(); offset >= 0 {
		ttfb := time.Duration(offset)
		result.ttfb = &ttfb
	}

	body := &errorTrackingReader{reader: response.Body}
	switch {
	case response.StatusCode < 200 || response.StatusCode >= 300:
		_, _ = io.Copy(io.Discard, io.LimitReader(body, maxErrorBodyBytes))
		result.errorClass = httpErrorClass(response.StatusCode)
	case request.Streaming:
		r.readStream(&result, body, sentAt)
	default:
		readNonStreaming(&result, body)
	}
	result.e2e = e.now().Sub(sentAt)
	r.markIfAbandoned(&result)
	return result
}

// markIfAbandoned turns a request cut off when the drain time ran out into an
// "abandoned" record. It was sent, so it is kept (raw first), with the timings
// it had; status is 0 because no complete response was received.
func (r *runState) markIfAbandoned(result *outcome) {
	if r.abortCtx.Err() == nil || result.errorClass == "" { // a request that finished cleanly keeps its result
		return
	}
	r.engine.counters.abandoned.Add(1)
	result.status = 0
	result.errorClass = ErrorAbandoned
}

func (e *Engine) newHTTPRequest(ctx context.Context, request workload.Request) (*http.Request, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.endpoint, "/")+request.Path, bytes.NewReader(request.Body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if request.Streaming {
		httpRequest.Header.Set("Accept", "text/event-stream")
	}
	if e.options.BearerToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+e.options.BearerToken)
	}
	return httpRequest, nil
}

// readStream parses an SSE body and fills the streaming measurements.
func (r *runState) readStream(result *outcome, body *errorTrackingReader, sentAt time.Time) {
	parsed, parseErr := sse.Parse(body, clockFunc(r.engine.now))
	if ttft, ok := parsed.TTFT(sentAt); ok {
		result.ttft = &ttft
	}
	if !parsed.FirstContentAt.IsZero() {
		content := parsed.FirstContentAt.Sub(sentAt)
		result.firstContent = &content
	}
	result.interTokenLatencies = parsed.InterTokenLatencies()
	result.usage = parsed.Usage
	result.errorClass = classifyStream(parsed, parseErr, body.err)
}

// classifyStream names how a stream that answered 200 went wrong, or returns ""
// for a stream that finished. A read timeout is a timeout; an error object the
// server sent, or a payload that is not valid, is a stream_error; a stream that
// ended without [DONE] is truncated.
func classifyStream(parsed sse.Result, parseErr, readErr error) string {
	switch {
	case readErr != nil && classifyTransportError(readErr) == ErrorTimeout:
		return ErrorTimeout
	case parsed.Error != nil, parseErr != nil && readErr == nil:
		return ErrorStream
	case readErr != nil, parsed.Truncated:
		return ErrorTruncated
	}
	return ""
}

// readNonStreaming reads the whole body and takes usage from it. A body that is
// not JSON on a 200 is not an error of its own: it simply has no usage.
func readNonStreaming(result *outcome, body *errorTrackingReader) {
	content, _ := io.ReadAll(io.LimitReader(body, maxNonStreamingBodyBytes))
	if body.err != nil {
		result.errorClass = ErrorTruncated
		if classifyTransportError(body.err) == ErrorTimeout {
			result.errorClass = ErrorTimeout
		}
		return
	}
	var payload struct {
		Usage *sse.Usage `json:"usage"`
	}
	if json.Unmarshal(content, &payload) == nil {
		result.usage = payload.Usage
	}
}

// errorTrackingReader remembers the read error of the body, so a failure of the
// connection can be told from a payload the parser rejected.
type errorTrackingReader struct {
	reader io.Reader
	err    error
}

func (t *errorTrackingReader) Read(buffer []byte) (int, error) {
	count, err := t.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		t.err = err
	}
	return count, err
}

// record turns an outcome into a row for the sink. A warmup row is written too
// (SPEC: warmup stays in raw data) with phase warmup and no stage index, and
// the warmup request is also counted and fed to the calibrator.
func (r *runState) record(phase engine.Phase, request workload.Request, result outcome) {
	if phase.StageIndex == nil {
		r.noteWarmup(request, result)
	}
	r.queueRow(requestRow(phase, request, result))
}

// noteWarmup counts a warmup request and lets the calibrator learn the
// chars-per-token ratio from the usage the server reported. Only warmup feeds
// the calibrator: learning during stages would change prompt lengths mid-run.
func (r *runState) noteWarmup(request workload.Request, result outcome) {
	r.engine.counters.warmupRequests.Add(1)
	if result.errorClass != "" {
		r.engine.counters.warmupErrors.Add(1)
		return
	}
	if result.usage != nil {
		r.engine.calibrator.Observe(r.engine.runSpec.Target.Model, request.PromptChars, result.usage.PromptTokens)
	}
}

func requestRow(phase engine.Phase, request workload.Request, result outcome) engine.RequestRow {
	scheduledAt := result.scheduledAt.UTC()
	sendLag := max(result.sentAt.Sub(result.scheduledAt), 0).Seconds()
	requestBytes := int64(len(request.Body))
	row := engine.RequestRow{
		RequestBytes:        &requestBytes,
		Phase:               engine.RowPhaseWarmup,
		ScheduledAt:         &scheduledAt,
		SentAt:              result.sentAt.UTC(),
		SendLagSeconds:      &sendLag,
		TTFBSeconds:         seconds(result.ttfb),
		TTFTSeconds:         seconds(result.ttft),
		FirstContentSeconds: seconds(result.firstContent),
		E2ESeconds:          result.e2e.Seconds(),
		Status:              int64(result.status),
	}
	if phase.StageIndex != nil {
		stageIndex := int64(*phase.StageIndex)
		row.Phase, row.StageIndex = engine.RowPhaseStage, &stageIndex
	}
	for _, gap := range result.interTokenLatencies {
		row.ITLSeconds = append(row.ITLSeconds, gap.Seconds())
	}
	if result.usage != nil {
		input, output := int64(result.usage.PromptTokens), int64(result.usage.CompletionTokens)
		row.InputTokens, row.OutputTokens = &input, &output
	}
	if result.errorClass != "" {
		row.Error = &result.errorClass
	}
	return row
}

func seconds(duration *time.Duration) *float64 {
	if duration == nil {
		return nil
	}
	value := duration.Seconds()
	return &value
}
