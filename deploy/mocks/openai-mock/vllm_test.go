package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func envOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func noSleep(time.Duration) {}

// The args the deployer renders.
var deployedArgs = []string{"serve", "Qwen/Qwen3-32B", "--host=0.0.0.0", "--port=8000", "--download-dir=/models/hf",
	"--max-model-len=32768", "--tensor-parallel-size=2", "--gpu-memory-utilization=0.45", "--max-num-seqs=64", "--enable-prefix-caching"}

func TestVLLMArgsBecomeTheMocksConfig(t *testing.T) {
	parsed, err := parseVLLMArgs(deployedArgs)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := vllmConfig(parsed, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != "0.0.0.0:8000" || cfg.ModelName != "Qwen/Qwen3-32B" || cfg.TLS != tlsOff {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.Faults.MaxConcurrency != 64 || cfg.Faults.OverCapacity != overCapacityQueue {
		t.Errorf("max_num_seqs caps concurrency: %+v", cfg.Faults)
	}
	// 2048 blocks x 2 GPUs x 0.45/0.8: below the memory bound, utilization sizes the cache.
	if cfg.VLLM.GPUBlocks != 2304 || !cfg.VLLM.PrefixCachingOn {
		t.Errorf("emulation = %+v", *cfg.VLLM)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config: %v", err)
	}
	tls, _ := parseVLLMArgs([]string{"serve", "m", "--served-model-name=qwen", "--ssl-certfile=/t/tls.crt", "--ssl-keyfile=/t/tls.key"})
	tlsCfg, _ := vllmConfig(tls, noEnv)
	if tlsCfg.TLS != tlsOn || tlsCfg.ClientAuth != clientAuthNone || tlsCfg.ModelName != "qwen" || tlsCfg.ClientCAFile != "" {
		t.Errorf("tls config = %+v", tlsCfg)
	}
}

func TestVLLMFailsAsVLLMDoes(t *testing.T) {
	var stderr bytes.Buffer
	code := runVLLM(deployedArgs, envOf(map[string]string{"VLLM_MOCK_UNKNOWN_FLAGS": "--max-num-seqs"}), io.Discard, &stderr, noSleep)
	if code != vllmArgparseExit || !strings.Contains(stderr.String(), "error: unrecognized arguments: --max-num-seqs") {
		t.Errorf("unknown flag: exit %d, %q", code, stderr.String())
	}
	stderr.Reset()
	env := envOf(map[string]string{"VLLM_MOCK_INFEASIBLE": "--tensor-parallel-size=1; --tensor-parallel-size=2,--max-num-seqs=64"})
	code = runVLLM(deployedArgs, env, io.Discard, &stderr, noSleep)
	log := stderr.String()
	if code != vllmFailureExit || !strings.Contains(log, "No available memory for the cache blocks") {
		t.Errorf("infeasible: exit %d, %q", code, log)
	}
	if !strings.HasSuffix(strings.TrimSpace(log), "Failed core proc(s): {}") {
		t.Errorf("the generic wrapper comes last, as in vLLM: %q", log)
	}
	if code := runVLLM([]string{"run", "m"}, noEnv, io.Discard, io.Discard, noSleep); code != vllmArgparseExit {
		t.Errorf("only serve is emulated: exit %d", code)
	}
}

func TestVLLMHealthAndMetrics(t *testing.T) {
	parsed, _ := parseVLLMArgs(deployedArgs)
	cfg, _ := vllmConfig(parsed, envOf(map[string]string{"VLLM_MOCK_GPU_BLOCKS": "1000"})) // x 2 GPUs x 0.45/0.8
	handler, _, _ := newHandlers(cfg)
	server := httptest.NewServer(handler)
	defer server.Close()
	health, err := http.Get(server.URL + "/health")
	if err != nil || health.StatusCode != http.StatusOK {
		t.Fatalf("/health: %v %v", health, err)
	}
	health.Body.Close()
	response, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	text := string(body)
	for _, want := range []string{`num_gpu_blocks="1125"`, `block_size="16"`, `engine="0"`, "vllm:num_requests_running 0", "vllm:kv_cache_usage_perc 0", "vllm:generation_tokens_total 0"} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}

// vLLM 0.34's extensible KV cache: above the bound, free memory sizes the cache, so 0.95 and the
// default 1.0 boot the same configuration.
func TestVLLMUtilizationOnlyMattersBelowTheMemoryBound(t *testing.T) {
	blocks := func(args ...string) int {
		parsed, err := parseVLLMArgs(append([]string{"serve", "m"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := vllmConfig(parsed, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.VLLM.GPUBlocks
	}
	if got := blocks(); got != 2048 {
		t.Errorf("default utilization: %d blocks", got)
	}
	if got := blocks("--gpu-memory-utilization=0.95"); got != 2048 {
		t.Errorf("0.95 is above the bound: %d blocks", got)
	}
	if got := blocks("--gpu-memory-utilization=0.6"); got != 1536 {
		t.Errorf("0.6 is below the bound: %d blocks, want 2048 x 0.6/0.8", got)
	}
}

func TestVLLMLogsItsKVSizing(t *testing.T) {
	var log strings.Builder
	logKVSizing(&log, &VLLMEmulation{GPUBlocks: 1536, GPUMemoryUtil: 0.6, MemoryBound: vllmMockMemoryBound})
	for _, want := range []string{"Free memory on device (65.00/80.00 GiB) on startup", "Memory after warmup: 55.00 GiB free", "1536 KV cache blocks (3.07 GiB) fit within the requested 48.00 GiB"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
}
