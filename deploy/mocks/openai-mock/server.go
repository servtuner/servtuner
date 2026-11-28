package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	chatCompletionsPath = "/v1/chat/completions"
	completionsPath     = "/v1/completions"
	modelsPath          = "/v1/models"
	maxRequestBytes     = 8 << 20
	contentToken        = "tok "

	// statusClientClosedRequest is nginx's convention for "the client went
	// away"; it keeps abandoned streams out of the 2xx counts.
	statusClientClosedRequest = 499
)

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// completionPlan is what one request will return, decided before any delay.
type completionPlan struct {
	id               string
	created          int64
	promptTokens     int
	completionTokens int
	finishReason     string
}

type server struct {
	cfg      Config
	faults   Faults
	metrics  *metrics
	requests atomic.Uint64
}

// newHandlers returns the handler for the main listener and the /metrics-only handler for the metrics
// listener. With no metrics listener configured, /metrics stays on the main handler and the second is nil.
// The metrics are returned too, for the memory ramp.
func newHandlers(cfg Config) (main, metricsOnly http.Handler, m *metrics) {
	m = newMetrics()
	s := &server{cfg: cfg, faults: newFaults(cfg.Faults, m), metrics: m}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+chatCompletionsPath, s.handleInference(chatEndpoint))
	mux.HandleFunc("POST "+completionsPath, s.handleInference(completionsEndpoint))
	mux.HandleFunc("GET "+modelsPath, s.handleModels)
	health := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	mux.HandleFunc("GET /healthz", health)
	if cfg.VLLM != nil {
		mux.HandleFunc("GET /health", health) // vLLM's path, which the deployer's probes use
		registerVLLMMetrics(m, *cfg.VLLM)
	}
	if cfg.SGLang != nil {
		registerSGLang(mux, m, cfg)
	}
	if cfg.MetricsListenAddress == "" {
		mux.Handle("GET /metrics", s.metrics.handler())
		return mux, nil, m
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", s.metrics.handler())
	return mux, metricsMux, m
}

func (s *server) handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id": s.cfg.ModelName, "object": "model", "created": 0, "owned_by": "servtuner-mock",
		}},
	})
}

// inferenceRequest is what both endpoints reduce a request body to.
type inferenceRequest struct {
	promptTokens int
	// maxTokens is the requested completion length, 0 when the request sets none.
	maxTokens    int
	stream       bool
	includeUsage bool
}

// streamEventKind says which chunk the generation core wants written next.
type streamEventKind int

const (
	// openingEvent is a content-free chunk sent before the TTFT delay so that headers (TTFB) precede the
	// first token. Only sent when Config.SendRoleOnlyFirstChunk is set.
	openingEvent streamEventKind = iota
	tokenEvent
	finishEvent
	usageEvent
)

type streamEvent struct {
	kind streamEventKind
	// isFirstChunk is true when no chunk has been written yet.
	isFirstChunk bool
	usage        usage
}

// endpoint is one inference path: how to read its request and how to encode its responses. Everything else
// (faults, timing, token accounting, metrics) is the shared generation core in handleInference.
type endpoint struct {
	path     string
	idPrefix string
	decode   func(body io.Reader) (inferenceRequest, error)
	// completeBody builds the non-streaming response; text is the whole generated text.
	completeBody func(plan completionPlan, model, text string) map[string]any
	// streamChunk builds one streaming chunk.
	streamChunk func(plan completionPlan, model string, event streamEvent) map[string]any
}

func (s *server) handleInference(ep endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		release, handled := s.faults.Admit(w, r)
		if handled {
			return
		}
		defer release()
		start := time.Now()
		s.metrics.inFlight.Inc()
		defer s.metrics.inFlight.Dec()

		req, err := ep.decode(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			s.metrics.countRequest(ep.path, http.StatusBadRequest, false)
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		plan := s.plan(ep, req)
		if req.stream {
			err = s.stream(w, r.Context(), start, ep, req, plan)
		} else {
			err = s.writeComplete(w, r.Context(), start, ep, plan)
		}

		code := http.StatusOK
		if err != nil {
			code = statusClientClosedRequest
		}
		s.metrics.countRequest(ep.path, code, req.stream)
		if err == nil {
			s.metrics.requestDuration.Observe(time.Since(start).Seconds())
			s.metrics.promptTokens.Add(float64(plan.promptTokens))
			s.metrics.outputTokens.Add(float64(plan.completionTokens))
		}
	}
}

func (s *server) plan(ep endpoint, req inferenceRequest) completionPlan {
	plan := completionPlan{
		id:               fmt.Sprintf("%s-mock-%d", ep.idPrefix, s.requests.Add(1)),
		created:          time.Now().Unix(),
		promptTokens:     req.promptTokens,
		completionTokens: s.cfg.OutputTokens,
		finishReason:     "stop",
	}
	if s.cfg.HonorRequestMaxTokens && req.maxTokens > 0 {
		plan.completionTokens = req.maxTokens
		plan.finishReason = "length"
	}
	return plan
}

// countWords approximates a tokenizer with whitespace-separated words; the mock only needs a stable,
// input-dependent number.
func countWords(text string) int { return len(strings.Fields(text)) }

func (p completionPlan) usage() usage {
	return usage{
		PromptTokens:     p.promptTokens,
		CompletionTokens: p.completionTokens,
		TotalTokens:      p.promptTokens + p.completionTokens,
	}
}

// writeComplete serves a non-streaming response after the time a streaming one would take to finish
// generating.
func (s *server) writeComplete(w http.ResponseWriter, ctx context.Context, start time.Time, ep endpoint, plan completionPlan) error {
	generationTime := s.cfg.TimeToFirstToken + time.Duration(plan.completionTokens-1)*s.cfg.InterTokenLatency
	if err := sleep(ctx, generationTime); err != nil {
		return err
	}
	s.metrics.timeToFirstByte.Observe(time.Since(start).Seconds())
	s.metrics.timeToFirst.Observe(time.Since(start).Seconds())
	writeJSON(w, http.StatusOK, ep.completeBody(plan, s.cfg.ModelName, strings.Repeat(contentToken, plan.completionTokens)))
	return nil
}

// stream writes server-sent events. loadgen distinguishes TTFB (headers) from TTFT (first content
// token), so with SendRoleOnlyFirstChunk the headers and a content-free opening chunk go out before the TTFT
// delay.
func (s *server) stream(w http.ResponseWriter, ctx context.Context, start time.Time, ep endpoint, req inferenceRequest, plan completionPlan) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "response writer cannot stream")
		return errors.New("response writer cannot flush")
	}
	chunksSent := 0
	send := func(kind streamEventKind) {
		if chunksSent == 0 {
			header := w.Header()
			header.Set("Content-Type", "text/event-stream")
			header.Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			s.metrics.timeToFirstByte.Observe(time.Since(start).Seconds())
		}
		encoded, _ := json.Marshal(ep.streamChunk(plan, s.cfg.ModelName, streamEvent{
			kind: kind, isFirstChunk: chunksSent == 0, usage: plan.usage(),
		}))
		chunksSent++
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		flusher.Flush()
	}

	if s.cfg.SendRoleOnlyFirstChunk {
		send(openingEvent)
	}
	if err := sleep(ctx, s.cfg.TimeToFirstToken); err != nil {
		return err
	}
	for i := range plan.completionTokens {
		if i > 0 {
			if err := sleep(ctx, s.cfg.InterTokenLatency); err != nil {
				return err
			}
		}
		send(tokenEvent)
		if i == 0 {
			s.metrics.timeToFirst.Observe(time.Since(start).Seconds())
		}
	}
	send(finishEvent)
	if req.includeUsage {
		send(usageEvent)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
	return nil
}

// sleep waits for duration or until the client goes away.
func sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeJSON(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]any{
		"error": map[string]any{"message": message, "type": "invalid_request_error"},
	})
}
