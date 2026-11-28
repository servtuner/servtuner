package target

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

const testBearer = "Bearer sentinel-secret-bearer"

func vllmFixtures() map[string]string {
	return map[string]string{
		"/version":     "vllm-version.json",
		"/v1/models":   "openai-models.json",
		"/metrics":     "vllm-metrics.txt",
		"/server_info": "vllm-server-info.json",
	}
}

func TestVLLMInfoInBothTransportModes(t *testing.T) {
	forEachTransportMode(t, serveFixtures(t, vllmFixtures(), ""), func(t *testing.T, endpoint Endpoint) {
		info, err := newVLLMAdapter().Info(context.Background(), endpoint)
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if info.TargetInfo.Version == nil || *info.TargetInfo.Version != "0.99.0-test" {
			t.Errorf("version = %v", info.TargetInfo.Version)
		}
		if !reflect.DeepEqual(info.TargetInfo.Models, []string{"test-model"}) {
			t.Errorf("models = %v", info.TargetInfo.Models)
		}
		if info.TargetInfo.CacheConfig["num_gpu_blocks"] != "4096" || info.TargetInfo.CacheConfig["gpu_memory_utilization"] != "0.9" {
			t.Errorf("cache_config = %v", info.TargetInfo.CacheConfig)
		}
		wantKnobs := Knobs{
			KnobVLLMMaxNumSeqs:          128.0,
			KnobVLLMMaxNumBatchedTokens: 8192.0,
			KnobVLLMTensorParallelSize:  1.0,
			KnobVLLMMaxModelLen:         4096.0, // from /v1/models
			KnobVLLMGPUMemoryUtil:       0.9,
			KnobVLLMEnablePrefixCaching: true,
			KnobVLLMDataParallelSize:    2.0,
			KnobVLLMPipelineParallel:    1.0,
			KnobVLLMExpertParallel:      false,
			// /server_info is read after cache_config_info, so its value stands.
			KnobVLLMKVCacheDtype:   "fp8_e4m3",
			KnobVLLMChunkedPrefill: true,
			// From model_config, not speculative_config's awq and true.
			KnobVLLMQuantization: "fp8",
			KnobVLLMEnforceEager: false,
			KnobVLLMBlockSize:    16.0, // cache_config_info
			// Task 5.2's /server_info fields; a null max_num_queued_reqs is absent.
			KnobVLLMStreamInterval:         1.0,
			KnobVLLMAsyncScheduling:        true,
			KnobVLLMWatermark:              0.0,
			KnobVLLMDisableCustomAllReduce: false,
			KnobVLLMSpecMethod:             "eagle3",
			KnobVLLMSpecModel:              "test-draft",
			KnobVLLMSpecTokens:             3.0,
			KnobVLLMPerformanceMode:        "throughput",
			KnobVLLMOptimizationLevel:      2.0,
			// Task 5.3; numa_bind null is absent, and the attention backend
			// and cudagraph mode are not read from /server_info.
			KnobVLLMDPSyncInterval:      16.0,
			KnobVLLMAll2AllBackend:      "allgather_reducescatter",
			KnobVLLMLinearBackend:       "auto",
			KnobVLLMMoEBackend:          "auto",
			KnobVLLMFlashInferAutotune:  true,
			KnobVLLMMaxCudagraphCapture: 512.0,
			KnobVLLMIterationDetails:    false,
		}
		if !reflect.DeepEqual(info.Knobs, wantKnobs) {
			t.Errorf("knobs = %v, want %v", info.Knobs, wantKnobs)
		}
	})
}

func TestVLLMInfoWithoutDevModeStillCapturesCacheConfig(t *testing.T) {
	fixtures := vllmFixtures()
	delete(fixtures, "/server_info")
	forEachTransportMode(t, serveFixtures(t, fixtures, ""), func(t *testing.T, endpoint Endpoint) {
		info, err := newVLLMAdapter().Info(context.Background(), endpoint)
		if err != nil {
			t.Fatalf("a missing /server_info must not be an error: %v", err)
		}
		if _, has := info.Knobs[KnobVLLMMaxNumSeqs]; has {
			t.Error("max_num_seqs is not in cache_config_info and must stay absent")
		}
		if info.Knobs[KnobVLLMGPUMemoryUtil] != 0.9 || info.Knobs[KnobVLLMKVCacheDtype] != "auto" || info.Knobs[KnobVLLMBlockSize] != 16.0 {
			t.Errorf("knobs = %v", info.Knobs)
		}
	})
}

// A field that also occurs outside its section is read only from the section:
// an unquantized model with a quantized draft model has no quantization knob.
func TestVLLMServerInfoReadsSectionedFieldsOnlyFromTheirSection(t *testing.T) {
	document := `{"vllm_config": {"model_config": {"quantization": null},
		"aaa_config": {"quantization": "awq", "enforce_eager": true}}}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(document))
	})
	forEachTransportMode(t, handler, func(t *testing.T, endpoint Endpoint) {
		info := Info{Knobs: Knobs{}}
		if err := fetchServerInfoKnobs(context.Background(), endpoint, &info); err != nil {
			t.Fatal(err)
		}
		if len(info.Knobs) != 0 {
			t.Errorf("knobs outside model_config must not be read: %v", info.Knobs)
		}
	})
}

func TestVLLMInfoSendsTheAuthorizationHeaderOnlyWhereGiven(t *testing.T) {
	forEachTransportMode(t, serveFixtures(t, vllmFixtures(), testBearer), func(t *testing.T, endpoint Endpoint) {
		_, err := newVLLMAdapter().Info(context.Background(), endpoint)
		if err == nil || !strings.Contains(err.Error(), "status 401") {
			t.Fatalf("want a 401 error without credentials, got %v", err)
		}
		endpoint.Authorization = testBearer
		if _, err := newVLLMAdapter().Info(context.Background(), endpoint); err != nil {
			t.Errorf("Info with credentials: %v", err)
		}
	})
}

func TestInfoErrorsNeverContainSecrets(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "echo "+r.Header.Get("Authorization"), http.StatusInternalServerError)
	})
	forEachTransportMode(t, handler, func(t *testing.T, endpoint Endpoint) {
		endpoint.Authorization = testBearer
		_, err := newVLLMAdapter().Info(context.Background(), endpoint)
		if err == nil {
			t.Fatal("want an error")
		}
		if strings.Contains(err.Error(), "sentinel") {
			t.Errorf("error leaks the credential: %v", err)
		}
	})
}

func TestMTLSInfoIsRefusedWithoutAClientCertificate(t *testing.T) {
	endpoint, hop := newMTLSEndpoint(t, serveFixtures(t, vllmFixtures(), ""))
	tlsConfig, err := tlsconfig.ClientConfig(hop)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig.Certificates = nil
	endpoint.Client = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}, Timeout: testClientTimeout}
	if _, err := newVLLMAdapter().Info(context.Background(), endpoint); err == nil {
		t.Fatal("the server accepted a client with no certificate")
	}
}

func bifrostFixtures() map[string]string {
	return map[string]string{
		"/api/version":   "bifrost-version.json",
		"/api/config":    "bifrost-config.json",
		"/api/providers": "bifrost-providers.json",
	}
}

func TestBifrostInfoCapturesRedactedConfigAndKnobsInBothModes(t *testing.T) {
	forEachTransportMode(t, serveFixtures(t, bifrostFixtures(), ""), func(t *testing.T, endpoint Endpoint) {
		info, err := newBifrostAdapter().Info(context.Background(), endpoint)
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if info.TargetInfo.Version == nil || *info.TargetInfo.Version != "v2.3.2-test" {
			t.Errorf("version = %v", info.TargetInfo.Version)
		}
		wantKnobs := Knobs{KnobBifrostConcurrency: 1000.0, KnobBifrostBufferSize: 5000.0, KnobBifrostInitialPoolSize: 5000.0}
		if !reflect.DeepEqual(info.Knobs, wantKnobs) {
			t.Errorf("knobs = %v, want %v", info.Knobs, wantKnobs)
		}
		for _, name := range []string{BifrostConfigDocument, BifrostProvidersDocument} {
			document, ok := info.ConfigDocuments[name]
			if !ok {
				t.Fatalf("missing config document %s", name)
			}
			if !json.Valid(document) {
				t.Errorf("%s is not valid JSON", name)
			}
			for _, secret := range sentinelSecrets {
				if strings.Contains(string(document), secret) {
					t.Errorf("%s contains %q", name, secret)
				}
			}
		}
	})
}

func TestBifrostConcurrencyKnobIsAbsentWhenProvidersDisagree(t *testing.T) {
	knobs := Knobs{}
	var providers any
	_ = json.Unmarshal([]byte(`{"providers":[
		{"name":"a","concurrency_and_buffer_size":{"concurrency":10,"buffer_size":50}},
		{"name":"b","concurrency_and_buffer_size":{"concurrency":20,"buffer_size":50}}]}`), &providers)
	readBifrostProviderKnobs(providers, knobs)
	if _, has := knobs[KnobBifrostConcurrency]; has {
		t.Errorf("concurrency must be absent when providers differ: %v", knobs)
	}
	if knobs[KnobBifrostBufferSize] != 50.0 {
		t.Errorf("buffer_size = %v", knobs[KnobBifrostBufferSize])
	}
}

func TestBifrostVersionAcceptsPlainTextAndObjects(t *testing.T) {
	for name, body := range map[string]string{"plain text": "v9.9.9\n", "object": `{"version":"v9.9.9"}`, "string": `"v9.9.9"`} {
		t.Run(name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			forEachTransportMode(t, handler, func(t *testing.T, endpoint Endpoint) {
				var info Info
				if err := fetchBifrostVersion(context.Background(), endpoint, &info); err != nil {
					t.Fatal(err)
				}
				if info.TargetInfo.Version == nil || *info.TargetInfo.Version != "v9.9.9" {
					t.Errorf("version = %v", info.TargetInfo.Version)
				}
			})
		})
	}
}

func TestBifrostInfoReportsAuthFailuresButKeepsTheVersion(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte(`"v2.3.2-test"`))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	forEachTransportMode(t, handler, func(t *testing.T, endpoint Endpoint) {
		info, err := newBifrostAdapter().Info(context.Background(), endpoint)
		if err == nil || !strings.Contains(err.Error(), "status 401") {
			t.Fatalf("want a 401 error, got %v", err)
		}
		if info.TargetInfo.Version == nil {
			t.Error("the unauthenticated version was lost")
		}
	})
}

func TestModelsAdaptersReadTheModelList(t *testing.T) {
	for _, adapter := range []TargetAdapter{newLiteLLMAdapter(), newOpenAICompatibleAdapter(), newSGLangAdapter()} {
		t.Run(string(adapter.Type()), func(t *testing.T) {
			forEachTransportMode(t, serveFixtures(t, map[string]string{"/v1/models": "openai-models.json"}, ""), func(t *testing.T, endpoint Endpoint) {
				info, err := adapter.Info(context.Background(), endpoint)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(info.TargetInfo.Models, []string{"test-model"}) {
					t.Errorf("models = %v", info.TargetInfo.Models)
				}
			})
		})
	}
}

func TestInfoSurfacesAnUnavailableEndpoint(t *testing.T) {
	forEachTransportMode(t, http.NotFoundHandler(), func(t *testing.T, endpoint Endpoint) {
		_, err := newOpenAICompatibleAdapter().Info(context.Background(), endpoint)
		if !errors.Is(err, errNotFound) {
			t.Errorf("want errNotFound, got %v", err)
		}
	})
}

func TestAdapterForCoversEveryTargetTypeWithItsCatalogs(t *testing.T) {
	want := map[spec.TargetType][]string{
		spec.TargetTypeVllm:             {CatalogVLLM, CatalogKubernetes},
		spec.TargetTypeBifrost:          {CatalogBifrost, CatalogKubernetes},
		spec.TargetTypeLitellm:          {CatalogLiteLLM, CatalogKubernetes},
		spec.TargetTypeOpenaiCompatible: {CatalogKubernetes},
		spec.TargetTypeSglang:           {CatalogSGLang, CatalogKubernetes},
	}
	for targetType, catalogs := range want {
		adapter, err := AdapterFor(targetType)
		if err != nil {
			t.Fatal(err)
		}
		if adapter.Type() != targetType || !reflect.DeepEqual(adapter.DefaultCatalogs(), catalogs) {
			t.Errorf("%s: type %s catalogs %v", targetType, adapter.Type(), adapter.DefaultCatalogs())
		}
	}
	if _, err := AdapterFor("nonsense"); err == nil {
		t.Error("an unknown type must be an error")
	}
}
