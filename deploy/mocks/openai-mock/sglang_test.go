package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sglangServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg, err := loadConfig([]string{"--tls", "false", "--emulate", "sglang", "--model", "mock-model"}, envOf(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	handler, _, _ := newHandlers(cfg)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

func TestSGLangInfoEndpoints(t *testing.T) {
	server := sglangServer(t)
	for _, path := range []string{"/server_info", "/get_server_info"} {
		status, body := getBody(t, server.URL+path)
		var info map[string]any
		if status != http.StatusOK || json.Unmarshal([]byte(body), &info) != nil {
			t.Fatalf("%s: %d %s", path, status, body)
		}
		if info["version"] != sglangEmulatedVersion || info["tp_size"] != 1.0 || info["mem_fraction_static"] != sglangDefaultMemFraction ||
			info["max_total_num_tokens"] != float64(sglangMockMaxTotalTokens) || info["frontend"] != "python" {
			t.Errorf("%s = %v", path, info)
		}
	}
	for _, path := range []string{"/model_info", "/get_model_info", "/health", "/health_generate"} {
		if status, _ := getBody(t, server.URL+path); status != http.StatusOK {
			t.Errorf("%s: %d", path, status)
		}
	}
}

func TestSGLangMetricsCarrySGLangsNamesAndLabels(t *testing.T) {
	server := sglangServer(t)
	_, text := getBody(t, server.URL+"/metrics")
	for _, want := range []string{
		`sglang:num_running_reqs{engine_type="unified",model_name="mock-model",moe_ep_rank="0",pp_rank="0",tp_rank="0"} 0`,
		`sglang:num_queue_reqs{`,
		`sglang:token_usage{`,
		`sglang:max_total_num_tokens{engine_type="unified",model_name="mock-model",moe_ep_rank="0",pp_rank="0",tp_rank="0"} 400000`,
		`sglang:prompt_tokens_total{is_streaming="true",model_name="mock-model"} 0`,
		`sglang:cached_tokens_total{cache_source="device",model_name="mock-model"} 0`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
	// SGLang creates its retraction counters at the first retraction; the mock never retracts.
	if strings.Contains(text, "sglang:num_retracted_requests_total") {
		t.Error("a server that never retracted has no retraction counter")
	}
	if strings.Contains(text, "vllm:") {
		t.Error("the SGLang emulation must not export vLLM's names")
	}
}

func TestPlainMockHasNoSGLangEndpoints(t *testing.T) {
	cfg, err := loadConfig([]string{"--tls", "false"}, envOf(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	handler, _, _ := newHandlers(cfg)
	server := httptest.NewServer(handler)
	defer server.Close()
	if status, _ := getBody(t, server.URL+"/server_info"); status != http.StatusNotFound {
		t.Errorf("/server_info: %d", status)
	}
	if _, text := getBody(t, server.URL+"/metrics"); strings.Contains(text, "sglang:") {
		t.Error("a plain mock exports sglang: series")
	}
}

func TestOnlySGLangIsEmulated(t *testing.T) {
	if _, err := loadConfig([]string{"--tls", "false", "--emulate", "tgi"}, envOf(nil), io.Discard); err == nil {
		t.Error("an unknown --emulate must be refused")
	}
}
