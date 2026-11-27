package main

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyBuckets span a fast mock (milliseconds) to a stalled one (a minute).
var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

type metrics struct {
	registry        *prometheus.Registry
	requests        *prometheus.CounterVec
	inFlight        prometheus.Gauge
	timeToFirstByte prometheus.Histogram
	timeToFirst     prometheus.Histogram
	requestDuration prometheus.Histogram
	promptTokens    prometheus.Counter
	outputTokens    prometheus.Counter
	// Fault knobs (faults.go).
	queued    prometheus.Gauge
	faults    *prometheus.CounterVec
	rampBytes prometheus.Gauge
}

func newMetrics() *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openai_mock_requests_total",
			Help: "Requests served, by path, HTTP status code and whether the response streamed.",
		}, []string{"path", "code", "stream"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openai_mock_requests_in_flight",
			Help: "Inference requests currently being served.",
		}),
		timeToFirstByte: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "openai_mock_time_to_first_byte_seconds",
			Help:    "Time from request start until response headers were written.",
			Buckets: latencyBuckets,
		}),
		timeToFirst: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "openai_mock_time_to_first_token_seconds",
			Help:    "Time from request start until the first content token was written.",
			Buckets: latencyBuckets,
		}),
		requestDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "openai_mock_request_duration_seconds",
			Help:    "End-to-end time of completed inference requests.",
			Buckets: latencyBuckets,
		}),
		promptTokens: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openai_mock_prompt_tokens_total", Help: "Prompt tokens reported in usage.",
		}),
		outputTokens: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "openai_mock_completion_tokens_total", Help: "Completion tokens reported in usage.",
		}),
		queued: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openai_mock_requests_queued",
			Help: "Requests waiting for a slot under --max-concurrency with --over-capacity queue.",
		}),
		faults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "openai_mock_faults_total",
			Help: "Requests a fault knob answered instead of serving: rejected (over capacity) or error (injected).",
		}, []string{"kind"}),
		rampBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "openai_mock_memory_ramp_bytes",
			Help: "Bytes retained by the memory ramp so far.",
		}),
	}
	m.registry.MustRegister(m.requests, m.inFlight, m.timeToFirstByte, m.timeToFirst,
		m.requestDuration, m.promptTokens, m.outputTokens, m.queued, m.faults, m.rampBytes)
	return m
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *metrics) countRequest(path string, statusCode int, isStream bool) {
	m.requests.WithLabelValues(path, strconv.Itoa(statusCode), strconv.FormatBool(isStream)).Inc()
}
