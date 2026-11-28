package main

// The mock as SGLang (--emulate sglang): the endpoints and metric names servtuner reads from an SGLang
// v0.5.23 server, so the SGLang adapter, catalog and analysis run on kind without a GPU:
//   - /server_info (and the deprecated /get_server_info): flat resolved server arguments, the launch
//     command, the scheduler's max_total_num_tokens, the version and the frontend;
//   - /model_info (and /get_model_info), /health and /health_generate;
//   - sglang: gauges and counters. Scheduler series carry model_name, engine_type, tp_rank, pp_rank and
//     moe_ep_rank; tokenizer series carry model_name. The latency histograms are not emulated: the
//     mock's own openai_mock_* histograms and the client's records cover latency.
// Starting as `sglang serve` (a managed SGLang) is not emulated yet.

import (
	"math"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	emulateSGLang = "sglang"
	// The SGLang release whose interface this emulates.
	sglangEmulatedVersion = "0.5.23"
	// SGLang's own defaults where the mock reports one.
	sglangDefaultMemFraction   = 0.82
	sglangDefaultChunkedPrefil = 8192
	sglangDefaultPolicy        = "fcfs"
	// The emulated KV pool, in tokens, and its context length: a small model on one GPU.
	sglangMockMaxTotalTokens = 400000
	sglangMockContextLength  = 32768
	// SGLang's default cap on running requests when it is not set; requests beyond it queue.
	sglangMockMaxRunningRequests = 2048
	// The tokens one running request is assumed to hold, for token_usage and kv_used_tokens.
	sglangMockTokensPerRequest = 512
)

// SGLangEmulation is what the mock reports as SGLang.
type SGLangEmulation struct {
	MaxTotalNumTokens  int
	MemFractionStatic  float64
	MaxRunningRequests int
	ContextLength      int
}

func defaultSGLangEmulation() *SGLangEmulation {
	return &SGLangEmulation{
		MaxTotalNumTokens:  sglangMockMaxTotalTokens,
		MemFractionStatic:  sglangDefaultMemFraction,
		MaxRunningRequests: sglangMockMaxRunningRequests,
		ContextLength:      sglangMockContextLength,
	}
}

// registerSGLang adds SGLang's endpoints to mux and its metrics to m.
func registerSGLang(mux *http.ServeMux, m *metrics, cfg Config) {
	emulation := *cfg.SGLang
	serverInfo := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"model_path":                cfg.ModelName,
			"served_model_name":         cfg.ModelName,
			"tp_size":                   1,
			"dp_size":                   1,
			"pp_size":                   1,
			"mem_fraction_static":       emulation.MemFractionStatic,
			"max_running_requests":      nil,
			"chunked_prefill_size":      sglangDefaultChunkedPrefil,
			"schedule_policy":           sglangDefaultPolicy,
			"schedule_conservativeness": 1.0,
			"disable_radix_cache":       false,
			"context_length":            nil,
			"enable_metrics":            true,
			"api_key":                   nil,
			"launch_command":            "openai-mock --emulate sglang --model " + cfg.ModelName,
			"status":                    "ready",
			"max_total_num_tokens":      emulation.MaxTotalNumTokens,
			"max_req_input_len":         emulation.ContextLength - 1,
			"internal_states":           []map[string]any{{"effective_max_running_requests_per_dp": emulation.MaxRunningRequests}},
			"version":                   sglangEmulatedVersion,
			"frontend":                  "python",
		})
	}
	modelInfo := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"model_path":        cfg.ModelName,
			"served_model_name": cfg.ModelName,
			"is_generation":     true,
			"model_type":        "servtuner-mock",
		})
	}
	health := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("GET /server_info", serverInfo)
	mux.HandleFunc("GET /get_server_info", serverInfo)
	mux.HandleFunc("GET /model_info", modelInfo)
	mux.HandleFunc("GET /get_model_info", modelInfo)
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /health_generate", health)
	registerSGLangMetrics(m, emulation, cfg.ModelName)
}

func registerSGLangMetrics(m *metrics, emulation SGLangEmulation, model string) {
	read := func(collector prometheus.Metric) float64 {
		var metric dto.Metric
		if err := collector.Write(&metric); err != nil {
			return 0
		}
		if metric.Gauge != nil {
			return metric.Gauge.GetValue()
		}
		return metric.Counter.GetValue()
	}
	scheduler := prometheus.Labels{"model_name": model, "engine_type": "unified", "tp_rank": "0", "pp_rank": "0", "moe_ep_rank": "0"}
	tokenizer := prometheus.Labels{"model_name": model}
	streaming := prometheus.Labels{"model_name": model, "is_streaming": "true"}
	device := prometheus.Labels{"model_name": model, "cache_source": "device"}
	capacity := float64(emulation.MaxTotalNumTokens)
	used := func() float64 { return math.Min(capacity, read(m.inFlight)*sglangMockTokensPerRequest) }
	gauge := func(name, help string, labels prometheus.Labels, value func() float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: labels}, value)
	}
	counter := func(name, help string, labels prometheus.Labels, value func() float64) prometheus.Collector {
		return prometheus.NewCounterFunc(prometheus.CounterOpts{Name: name, Help: help, ConstLabels: labels}, value)
	}
	zero := func() float64 { return 0 }
	m.registry.MustRegister(
		gauge("sglang:num_running_reqs", "The number of running requests.", scheduler, func() float64 { return read(m.inFlight) }),
		gauge("sglang:num_queue_reqs", "The number of requests in the waiting queue.", scheduler, func() float64 { return read(m.queued) }),
		gauge("sglang:num_paused_reqs", "The number of paused requests.", scheduler, zero),
		gauge("sglang:num_retracted_reqs", "The number of retracted requests.", scheduler, zero),
		gauge("sglang:token_usage", "The token usage.", scheduler, func() float64 { return used() / capacity }),
		gauge("sglang:kv_used_tokens", "KV tokens in use.", scheduler, used),
		gauge("sglang:kv_available_tokens", "KV tokens available.", scheduler, func() float64 { return capacity - used() }),
		gauge("sglang:kv_evictable_tokens", "KV tokens held only by the prefix cache.", scheduler, zero),
		gauge("sglang:gen_throughput", "The generation throughput (token/s).", scheduler, zero),
		gauge("sglang:cache_hit_rate", "The prefix cache hit rate.", scheduler, zero),
		gauge("sglang:max_total_num_tokens", "The KV pool size in tokens.", scheduler, func() float64 { return capacity }),
		gauge("sglang:context_len", "The context length.", scheduler, func() float64 { return float64(emulation.ContextLength) }),
		counter("sglang:num_retracted_requests_total", "Retracted requests.", scheduler, zero),
		counter("sglang:num_retracted_input_tokens_total", "Input tokens of retracted requests.", scheduler, zero),
		counter("sglang:prompt_tokens_total", "Number of prefill tokens processed.", streaming, func() float64 { return read(m.promptTokens) }),
		counter("sglang:generation_tokens_total", "Number of generation tokens processed.", streaming, func() float64 { return read(m.outputTokens) }),
		counter("sglang:cached_tokens_total", "Number of cached prompt tokens by source.", device, zero),
		counter("sglang:num_aborted_requests_total", "Number of requests aborted.", tokenizer, zero),
	)
}
