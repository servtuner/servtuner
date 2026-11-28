package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// What the deployer runs for a managed SGLang (api deployer/manifests.py).
var deployedSGLangArgs = []string{
	"-m", "sglang.launch_server", "--model-path=Qwen/Qwen3-32B", "--host=0.0.0.0", "--port=8000",
	"--download-dir=/models/hf", "--enable-metrics", "--tp-size=2", "--mem-fraction-static=0.41", "--max-running-requests=64",
}

func TestSGLangArgsBecomeTheMocksConfig(t *testing.T) {
	parsed, err := parseSGLangArgs(deployedSGLangArgs)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := sglangServeConfig(parsed, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != "0.0.0.0:8000" || cfg.ModelName != "Qwen/Qwen3-32B" || cfg.TLS != tlsOff || cfg.SGLang == nil {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Faults.MaxConcurrency != 64 || cfg.Faults.OverCapacity != overCapacityQueue || cfg.SGLang.MaxRunningRequests != 64 {
		t.Errorf("max_running_requests caps concurrency: %+v %+v", cfg.Faults, *cfg.SGLang)
	}
	// 400000 tokens x 2 GPUs x 0.41 / 0.82: the pool follows the memory fraction and tensor parallel.
	if cfg.SGLang.MaxTotalNumTokens != 400000 || cfg.SGLang.MemFractionStatic != 0.41 {
		t.Errorf("emulation = %+v", *cfg.SGLang)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config: %v", err)
	}
	tls, _ := parseSGLangArgs([]string{"-m", "sglang.launch_server", "--model-path", "m", "--served-model-name", "qwen", "--ssl-certfile=/t/tls.crt", "--ssl-keyfile=/t/tls.key", "--enable-ssl-refresh"})
	tlsCfg, _ := sglangServeConfig(tls, noEnv)
	if tlsCfg.TLS != tlsOn || tlsCfg.ClientAuth != clientAuthNone || tlsCfg.ModelName != "qwen" || tlsCfg.ListenAddress != ":30000" {
		t.Errorf("tls config = %+v", tlsCfg)
	}
}

func TestSGLangFailsAsSGLangDoes(t *testing.T) {
	var stderr bytes.Buffer
	code := runSGLang(deployedSGLangArgs, envOf(map[string]string{"SGLANG_MOCK_UNKNOWN_FLAGS": "--max-running-requests"}), io.Discard, &stderr, noSleep)
	if code != sglangArgparseExit || !strings.Contains(stderr.String(), "error: unrecognized arguments: --max-running-requests") {
		t.Errorf("unknown flag: exit %d, %q", code, stderr.String())
	}
	stderr.Reset()
	env := envOf(map[string]string{"SGLANG_MOCK_INFEASIBLE": "--tp-size=1; --tp-size=2,--mem-fraction-static=0.41"})
	code = runSGLang(deployedSGLangArgs, env, io.Discard, &stderr, noSleep)
	log := stderr.String()
	if code != sglangFailureExit || !strings.Contains(log, "Not enough memory. Please try to increase --mem-fraction-static.") ||
		!strings.HasSuffix(strings.TrimSpace(log), "Initialization failed. Please see the error messages above.") {
		t.Errorf("infeasible: exit %d, %q", code, log)
	}
	if code := runSGLang([]string{"-c", "print(1)"}, noEnv, io.Discard, io.Discard, noSleep); code != sglangArgparseExit {
		t.Errorf("only the server is emulated: exit %d", code)
	}
	if code := runSGLang([]string{"-m", "sglang.launch_server", "--port=1"}, noEnv, io.Discard, io.Discard, noSleep); code != sglangArgparseExit {
		t.Errorf("--model-path is required: exit %d", code)
	}
}
