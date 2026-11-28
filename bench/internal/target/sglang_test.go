package target

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// The /server_info fixture is shaped from the SGLang v0.5.23 source (flat
// resolved ServerArgs fields plus the scheduler's startup facts); a live
// recording replaces it once one exists.
func sglangFixtures() map[string]string {
	return map[string]string{
		"/v1/models":   "openai-models.json",
		"/server_info": "sglang-server-info.json",
	}
}

func wantSGLangKnobs() Knobs {
	return Knobs{
		KnobSGLangTPSize:                  1.0,
		KnobSGLangDPSize:                  1.0,
		KnobSGLangPPSize:                  1.0,
		KnobSGLangMemFractionStatic:       0.82,
		KnobSGLangChunkedPrefillSize:      8192.0,
		KnobSGLangMaxPrefillTokens:        16384.0,
		KnobSGLangSchedulePolicy:          "fcfs",
		KnobSGLangScheduleConservativness: 1.0,
		KnobSGLangDisableRadixCache:       false,
		KnobSGLangDtype:                   "auto",
		KnobSGLangKVCacheDtype:            "auto",
		KnobSGLangAttentionBackend:        "flashinfer",
		KnobSGLangPageSize:                1.0,
		KnobSGLangStreamInterval:          1.0,
		KnobSGLangCudaGraphMaxBSDecode:    160.0,
		KnobSGLangEnableMetrics:           true,
		// null fields (max_running_requests, context_length, quantization,
		// speculative_*) are absent: unset, never compared.
	}
}

func TestSGLangInfoInBothTransportModes(t *testing.T) {
	forEachTransportMode(t, serveFixtures(t, sglangFixtures(), ""), func(t *testing.T, endpoint Endpoint) {
		info, err := newSGLangAdapter().Info(context.Background(), endpoint)
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if info.TargetInfo.Version == nil || *info.TargetInfo.Version != "0.5.23" {
			t.Errorf("version = %v", info.TargetInfo.Version)
		}
		if !reflect.DeepEqual(info.TargetInfo.Models, []string{"test-model"}) {
			t.Errorf("models = %v", info.TargetInfo.Models)
		}
		if !reflect.DeepEqual(info.Knobs, wantSGLangKnobs()) {
			t.Errorf("knobs = %v\nwant %v", info.Knobs, wantSGLangKnobs())
		}
	})
}

func TestSGLangServerInfoIsStoredRedacted(t *testing.T) {
	forEachTransportMode(t, serveFixtures(t, sglangFixtures(), ""), func(t *testing.T, endpoint Endpoint) {
		info, err := newSGLangAdapter().Info(context.Background(), endpoint)
		if err != nil {
			t.Fatal(err)
		}
		document, ok := info.ConfigDocuments[SGLangServerInfoDocument]
		if !ok {
			t.Fatal("no server info document")
		}
		if strings.Contains(string(document), "sentinel-secret") {
			t.Fatalf("the stored server info leaks the API key:\n%s", document)
		}
		var stored map[string]any
		if err := json.Unmarshal(document, &stored); err != nil {
			t.Fatal(err)
		}
		if stored["max_total_num_tokens"] != 412345.0 {
			t.Errorf("max_total_num_tokens = %v", stored["max_total_num_tokens"])
		}
		command, _ := stored["launch_command"].(string)
		if !strings.Contains(command, "--api-key "+RedactedPlaceholder) || !strings.Contains(command, "--enable-metrics") {
			t.Errorf("launch_command = %q", command)
		}
	})
}

func TestSGLangFallsBackToTheDeprecatedServerInfoPath(t *testing.T) {
	fixtures := map[string]string{"/v1/models": "openai-models.json", "/get_server_info": "sglang-server-info.json"}
	forEachTransportMode(t, serveFixtures(t, fixtures, ""), func(t *testing.T, endpoint Endpoint) {
		info, err := newSGLangAdapter().Info(context.Background(), endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if info.Knobs[KnobSGLangMemFractionStatic] != 0.82 {
			t.Errorf("knobs = %v", info.Knobs)
		}
	})
}

// With --api-key, SGLang refuses /server_info without the key: the run keeps
// what it has (none of the knobs) and reports the refusal, never the key.
func TestSGLangReportsARefusedServerInfo(t *testing.T) {
	forEachTransportMode(t, serveFixtures(t, sglangFixtures(), testBearer), func(t *testing.T, endpoint Endpoint) {
		info, err := newSGLangAdapter().Info(context.Background(), endpoint)
		if err == nil {
			t.Fatal("want an error for the refused endpoints")
		}
		if len(info.Knobs) != 0 {
			t.Errorf("knobs = %v", info.Knobs)
		}
		if strings.Contains(err.Error(), "sentinel") {
			t.Errorf("error leaks a secret: %v", err)
		}
		endpoint.Authorization = testBearer
		info, err = newSGLangAdapter().Info(context.Background(), endpoint)
		if err != nil || info.Knobs[KnobSGLangTPSize] != 1.0 {
			t.Errorf("with the key: knobs %v, err %v", info.Knobs, err)
		}
	})
}

func TestSGLangServerInfoMissingEverywhereIsReported(t *testing.T) {
	forEachTransportMode(t, serveFixtures(t, map[string]string{"/v1/models": "openai-models.json"}, ""), func(t *testing.T, endpoint Endpoint) {
		info, err := newSGLangAdapter().Info(context.Background(), endpoint)
		if !errors.Is(err, errNotFound) {
			t.Errorf("want errNotFound, got %v", err)
		}
		if !reflect.DeepEqual(info.TargetInfo.Models, []string{"test-model"}) {
			t.Errorf("models = %v", info.TargetInfo.Models)
		}
	})
}

func TestSGLangKnobsFromEitherEntryPoint(t *testing.T) {
	flags := []string{"--model-path", "m", "--tp-size", "2", "--mem-fraction-static=0.8", "--schedule-policy", "lpm", "--disable-radix-cache", "--chunked-prefill-size", "-1"}
	want := Knobs{
		KnobSGLangTPSize:             2.0,
		KnobSGLangMemFractionStatic:  0.8,
		KnobSGLangSchedulePolicy:     "lpm",
		KnobSGLangDisableRadixCache:  true,
		KnobSGLangChunkedPrefillSize: -1.0,
	}
	for name, args := range map[string][]string{
		"launch_server": append([]string{"python3", "-m", "sglang.launch_server"}, flags...),
		"sglang serve":  append([]string{"sglang", "serve"}, flags...),
	} {
		if got := sglangKnobsFromArgs(args); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: knobs = %v, want %v", name, got, want)
		}
	}
	if got := sglangKnobsFromArgs([]string{"sglang", "serve", "--tensor-parallel-size", "4"}); got[KnobSGLangTPSize] != 4.0 {
		t.Errorf("alias: knobs = %v", got)
	}
}

func TestSGLangKnobsFromWorkloadNeedEveryPodToAgree(t *testing.T) {
	pod := func(args ...string) spec.Pod {
		return spec.Pod{Containers: []spec.Container{{Args: append([]string{"python3", "-m", "sglang.launch_server"}, args...)}}}
	}
	workload := spec.Workload_2{Pods: []spec.Pod{pod("--tp-size", "2", "--schedule-policy", "lpm"), pod("--tp-size", "2", "--schedule-policy", "fcfs")}}
	got := newSGLangAdapter().KnobsFromWorkload(workload)
	if got[KnobSGLangTPSize] != 2.0 {
		t.Errorf("tp_size = %v", got[KnobSGLangTPSize])
	}
	if _, has := got[KnobSGLangSchedulePolicy]; has {
		t.Error("a knob the pods disagree on must be absent")
	}
}

// Every flag's long form is its field name with dashes, as SGLang derives it.
func TestSGLangFlagsAreTheirFieldNames(t *testing.T) {
	for _, flag := range sglangFlags {
		if flag.flags[0] != "--"+strings.ReplaceAll(flag.field, "_", "-") || flag.knob != "sglang."+flag.field {
			t.Errorf("%s: field %s, flags %v", flag.knob, flag.field, flag.flags)
		}
	}
}
