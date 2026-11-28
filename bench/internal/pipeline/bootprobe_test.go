package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

// fakeBootedVLLM serves /metrics with a cache_config_info row; /version and
// /v1/models are missing, which must not hide the KV capacity.
func fakeBootedVLLM(cacheConfig string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("# TYPE vllm:cache_config_info gauge\nvllm:cache_config_info{" + cacheConfig + "} 1.0\n"))
	})
}

func bootProbeRequest(t *testing.T, endpoint, tls string) spec.BootProbeRequest {
	t.Helper()
	var request spec.BootProbeRequest
	document := `{"target": {"type": "vllm", "endpoint": "` + endpoint + `", "model": "m", "tls": ` + tls + `},
		"knobs": {"vllm.tensor_parallel_size": 2}, "tokens_per_request": 4096}`
	if err := json.Unmarshal([]byte(document), &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestBootProbeComputesCKVFromTheCacheConfig(t *testing.T) {
	server := tlstest.NewPlainServer(t, fakeBootedVLLM(`block_size="16",engine="0",num_gpu_blocks="8192"`))
	result, err := BootProbe(context.Background(), t.TempDir(), bootProbeRequest(t, server.URL+"/v1", `{"enabled": false}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != spec.TuningBootOutcomeFeasible || result.NumGpuBlocks == nil || *result.NumGpuBlocks != 8192 {
		t.Fatalf("result = %+v", result)
	}
	if result.CKv == nil || *result.CKv != 32 { // 8192 blocks x 16 tokens / 4096 tokens per request
		t.Errorf("c_kv = %v, want 32", result.CKv)
	}
	if result.Knobs["vllm.tensor_parallel_size"] != float64(2) {
		t.Errorf("knobs not copied: %v", result.Knobs)
	}
	if result.VllmVersion != nil {
		t.Errorf("no /version, no version: got %q", *result.VllmVersion)
	}
	encoded, _ := json.Marshal(result)
	if err := spec.Validate(spec.SchemaBootResults, []byte(`{"results": [`+string(encoded)+`]}`)); err != nil {
		t.Errorf("the result breaks boot-results.schema.json: %v", err)
	}
}

// TestBootProbeRecordsTheVLLMVersion: the version that measured the capacity travels with it, so a
// later session reuses the boot only on the same vLLM.
func TestBootProbeRecordsTheVLLMVersion(t *testing.T) {
	booted := fakeBootedVLLM(`block_size="16",engine="0",num_gpu_blocks="8192"`)
	server := tlstest.NewPlainServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"version": "0.34.0"}`))
			return
		}
		booted.ServeHTTP(w, r)
	}))
	result, err := BootProbe(context.Background(), t.TempDir(), bootProbeRequest(t, server.URL+"/v1", `{"enabled": false}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.VllmVersion == nil || *result.VllmVersion != "0.34.0" {
		t.Errorf("vllm_version = %v, want 0.34.0", result.VllmVersion)
	}
}

func TestBootProbeNeedsAllocatedBlocks(t *testing.T) {
	server := tlstest.NewPlainServer(t, fakeBootedVLLM(`block_size="16",engine="0",num_gpu_blocks="None"`))
	_, err := BootProbe(context.Background(), t.TempDir(), bootProbeRequest(t, server.URL+"/v1", `{"enabled": false}`))
	if err == nil || !strings.Contains(err.Error(), "num_gpu_blocks") {
		t.Fatalf("err = %v, want the missing cache config named", err)
	}
}

// TestBootProbeOverTLS: the probe uses the target's own hop, so a tls target
// is read with its CA Secret and no client certificate.
func TestBootProbeOverTLS(t *testing.T) {
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewTLSServer(t, authority, fakeBootedVLLM(`block_size="16",engine="0",num_gpu_blocks="1024"`))
	secretsDir := t.TempDir()
	writeSecret(t, secretsDir, "vllm-ca", "ca.crt", authority.CertPEM)
	result, err := BootProbe(context.Background(), secretsDir,
		bootProbeRequest(t, server.URL+"/v1", `{"enabled": true, "caSecretRef": {"name": "vllm-ca"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if *result.CKv != 4 {
		t.Errorf("c_kv = %v, want 4", *result.CKv)
	}
}

// fakeBootedSGLang serves /server_info with SGLang's KV pool.
func fakeBootedSGLang(serverInfo string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/server_info" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(serverInfo))
	})
}

func sglangProbeRequest(t *testing.T, endpoint string) spec.BootProbeRequest {
	t.Helper()
	var request spec.BootProbeRequest
	document := `{"target": {"type": "sglang", "endpoint": "` + endpoint + `", "model": "m", "tls": {"enabled": false}},
		"knobs": {"sglang.mem_fraction_static": 0.8}, "tokens_per_request": 4096}`
	if err := json.Unmarshal([]byte(document), &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestBootProbeReadsSGLangsKVPool(t *testing.T) {
	server := tlstest.NewPlainServer(t, fakeBootedSGLang(`{"max_total_num_tokens": 409600, "page_size": 16, "version": "0.5.23"}`))
	result, err := BootProbe(context.Background(), t.TempDir(), sglangProbeRequest(t, server.URL+"/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != spec.TuningBootOutcomeFeasible || result.CKv == nil || *result.CKv != 100 {
		t.Errorf("c_kv = %v, want 409600 / 4096 = 100", result.CKv)
	}
	if result.NumGpuBlocks == nil || *result.NumGpuBlocks != 25600 {
		t.Errorf("pages = %v, want 409600 / 16", result.NumGpuBlocks)
	}
	if result.Knobs["sglang.mem_fraction_static"] != 0.8 {
		t.Errorf("knobs = %v", result.Knobs)
	}
}

func TestBootProbeNeedsSGLangsKVPool(t *testing.T) {
	server := tlstest.NewPlainServer(t, fakeBootedSGLang(`{"version": "0.5.23"}`))
	_, err := BootProbe(context.Background(), t.TempDir(), sglangProbeRequest(t, server.URL+"/v1"))
	if err == nil || !strings.Contains(err.Error(), "max_total_num_tokens") {
		t.Errorf("err = %v", err)
	}
}
