package target

import (
	"reflect"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestVLLMKnobsFromArgs(t *testing.T) {
	got := vllmKnobsFromArgs([]string{
		"--model", "m", "--max-num-seqs", "256", "--max-num-batched-tokens=8192",
		"--gpu-memory-utilization", "0.85", "-tp", "2", "--dtype", "bfloat16", "--enable-prefix-caching",
		"--max-model-len", "4096",
	})
	want := Knobs{
		KnobVLLMMaxNumSeqs:          256.0,
		KnobVLLMMaxNumBatchedTokens: 8192.0,
		KnobVLLMGPUMemoryUtil:       0.85,
		KnobVLLMTensorParallelSize:  2.0,
		KnobVLLMDtype:               "bfloat16",
		KnobVLLMEnablePrefixCaching: true,
		KnobVLLMMaxModelLen:         4096.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("knobs = %v, want %v", got, want)
	}
}

// The parallelism and quantization knobs, in their long and short spellings
// (vLLM v0.34.0's aliases).
func TestVLLMKnobsFromArgsParallelismAndQuantization(t *testing.T) {
	long := vllmKnobsFromArgs([]string{
		"--data-parallel-size", "2", "--pipeline-parallel-size=2", "--api-server-count", "4",
		"--kv-cache-dtype", "fp8", "--quantization", "fp8", "--enable-chunked-prefill",
		"--enable-expert-parallel", "--enforce-eager",
	})
	short := vllmKnobsFromArgs([]string{"-dp", "2", "-pp", "2", "-asc", "4", "--kv-cache-dtype=fp8", "-q", "fp8",
		"--enable-chunked-prefill", "-ep", "--enforce-eager"})
	want := Knobs{
		KnobVLLMDataParallelSize: 2.0,
		KnobVLLMPipelineParallel: 2.0,
		KnobVLLMAPIServerCount:   4.0,
		KnobVLLMKVCacheDtype:     "fp8",
		KnobVLLMQuantization:     "fp8",
		KnobVLLMChunkedPrefill:   true,
		KnobVLLMExpertParallel:   true,
		KnobVLLMEnforceEager:     true,
	}
	for name, got := range map[string]Knobs{"long": long, "short": short} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s spellings: knobs = %v, want %v", name, got, want)
		}
	}
}

// vLLM v0.34 pairs --enable-x with --disable-x; --no-enable-x still works.
func TestVLLMKnobsFromArgsDisableSpelling(t *testing.T) {
	got := vllmKnobsFromArgs([]string{"--disable-prefix-caching", "--disable-chunked-prefill", "--no-enable-expert-parallel", "--no-enforce-eager"})
	want := Knobs{
		KnobVLLMEnablePrefixCaching: false,
		KnobVLLMChunkedPrefill:      false,
		KnobVLLMExpertParallel:      false,
		KnobVLLMEnforceEager:        false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("knobs = %v, want %v", got, want)
	}
	// --enforce-eager has no enable- prefix, so --disable-enforce-eager means nothing.
	if _, has := vllmKnobsFromArgs([]string{"--disable-enforce-eager"})[KnobVLLMEnforceEager]; has {
		t.Error("--disable- applies only to --enable- flags")
	}
	if got := vllmKnobsFromArgs([]string{"--disable-prefix-caching", "--enable-prefix-caching"}); got[KnobVLLMEnablePrefixCaching] != true {
		t.Errorf("the last spelling must win: %v", got)
	}
}

// Task 5.2's scheduling, admission and multi-GPU knobs.
func TestVLLMKnobsFromArgsTuningKnobs(t *testing.T) {
	got := vllmKnobsFromArgs([]string{
		"--stream-interval", "10", "--no-async-scheduling", "--performance-mode", "interactivity",
		"--max-num-active-seqs", "64", "--long-prefill-token-threshold", "2048", "--long-prefill-token-threshold-adaptive",
		"--prefill-schedule-interval", "2", "--kv-cache-memory-bytes", "40G", "--block-size", "32",
		"--spec-method", "eagle3", "--spec-model", "draft", "--spec-tokens", "3",
		"--max-num-queued-reqs", "512", "--max-num-queued-tokens", "64k", "--watermark", "0.05",
		"--no-scheduler-reserve-full-isl", "--cache-aware-admission-window", "8", "--cache-aware-admission-threshold", "0.3",
		"--disable-custom-all-reduce", "-O3", "-dcp", "2",
	})
	want := Knobs{
		KnobVLLMStreamInterval:         10.0,
		KnobVLLMAsyncScheduling:        false,
		KnobVLLMPerformanceMode:        "interactivity",
		KnobVLLMMaxNumActiveSeqs:       64.0,
		KnobVLLMLongPrefillThreshold:   2048.0,
		KnobVLLMLongPrefillAdaptive:    true,
		KnobVLLMPrefillScheduleEvery:   2.0,
		KnobVLLMKVCacheMemoryBytes:     42949672960.0, // 40 x 2^30
		KnobVLLMBlockSize:              32.0,
		KnobVLLMSpecMethod:             "eagle3",
		KnobVLLMSpecModel:              "draft",
		KnobVLLMSpecTokens:             3.0,
		KnobVLLMMaxNumQueuedReqs:       512.0,
		KnobVLLMMaxNumQueuedTokens:     64000.0, // k is decimal
		KnobVLLMWatermark:              0.05,
		KnobVLLMReserveFullISL:         false,
		KnobVLLMCacheAwareWindow:       8.0,
		KnobVLLMCacheAwareThreshold:    0.3,
		KnobVLLMDisableCustomAllReduce: true,
		KnobVLLMOptimizationLevel:      3.0,
		KnobVLLMDecodeContextParallel:  2.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("knobs = %v, want %v", got, want)
	}
}

// Task 5.3's kernel, CUDA graph, frontend, data parallel, MoE and logging knobs.
func TestVLLMKnobsFromArgsKernelAndFrontendKnobs(t *testing.T) {
	got := vllmKnobsFromArgs([]string{
		"--max-cudagraph-capture-size", "1024", "--attention-backend", "FLASHINFER", "--linear-backend", "cutlass",
		"--moe-backend", "triton", "--no-enable-flashinfer-autotune", "--renderer-num-workers", "4",
		"--prefix-caching-hash-algo", "xxhash", "--dp-sync-interval", "32", "--data-parallel-hybrid-lb",
		"--no-data-parallel-external-lb", "--numa-bind", "--enable-eplb", "--all2all-backend", "deepep_low_latency",
		"--expert-placement-strategy", "round_robin", "--disable-dbo", "--enable-log-requests",
		"--enable-logging-iteration-details", "--disable-log-stats",
	})
	want := Knobs{
		KnobVLLMMaxCudagraphCapture:   1024.0,
		KnobVLLMAttentionBackend:      "FLASHINFER",
		KnobVLLMLinearBackend:         "cutlass",
		KnobVLLMMoEBackend:            "triton",
		KnobVLLMFlashInferAutotune:    false,
		KnobVLLMRendererNumWorkers:    4.0,
		KnobVLLMPrefixCachingHashAlgo: "xxhash",
		KnobVLLMDPSyncInterval:        32.0,
		KnobVLLMDPHybridLB:            true,
		KnobVLLMDPExternalLB:          false,
		KnobVLLMNUMABind:              true,
		KnobVLLMEnableEPLB:            true,
		KnobVLLMAll2AllBackend:        "deepep_low_latency",
		KnobVLLMExpertPlacement:       "round_robin",
		KnobVLLMEnableDBO:             false,
		KnobVLLMEnableLogRequests:     true,
		KnobVLLMIterationDetails:      true,
		KnobVLLMDisableLogStats:       true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("knobs = %v, want %v", got, want)
	}
}

// --compilation-config (-cc) as JSON, or field by field in the dotted form.
func TestVLLMKnobsFromCompilationConfig(t *testing.T) {
	want := Knobs{KnobVLLMCudagraphMode: "FULL_DECODE_ONLY", KnobVLLMMaxCudagraphCapture: 256.0}
	for _, args := range [][]string{
		{"--compilation-config", `{"cudagraph_mode": "FULL_DECODE_ONLY", "max_cudagraph_capture_size": 256}`},
		{"-cc", `{"cudagraph_mode":"FULL_DECODE_ONLY","max_cudagraph_capture_size":256}`},
		{"-cc.cudagraph_mode=FULL_DECODE_ONLY", "--compilation-config.max_cudagraph_capture_size", "256"},
	} {
		if got := vllmKnobsFromArgs(args); !reflect.DeepEqual(got, want) {
			t.Errorf("%v: knobs = %v, want %v", args, got, want)
		}
	}
	if got := vllmKnobsFromArgs([]string{"-cc", "not json"}); len(got) != 0 {
		t.Errorf("an unparsable config leaves the knobs absent: %v", got)
	}
}

// vLLM's -O spellings: attached, with "=", separate, and the long flag.
func TestVLLMKnobsFromArgsOptimizationLevelSpellings(t *testing.T) {
	for _, args := range [][]string{{"-O1"}, {"-O=1"}, {"-O", "1"}, {"--optimization-level", "1"}, {"--optimization-level=1"}} {
		if got := vllmKnobsFromArgs(args)[KnobVLLMOptimizationLevel]; got != 1.0 {
			t.Errorf("%v: optimization level = %v", args, got)
		}
	}
	if _, has := vllmKnobsFromArgs([]string{"-O"})[KnobVLLMOptimizationLevel]; has {
		t.Error("a bare -O has no level")
	}
}

// --disable-x flags are negated by --enable-x as well as --no-disable-x.
func TestVLLMKnobsFromArgsEnableNegatesADisableFlag(t *testing.T) {
	for _, argument := range []string{"--enable-custom-all-reduce", "--no-disable-custom-all-reduce"} {
		if got := vllmKnobsFromArgs([]string{argument}); got[KnobVLLMDisableCustomAllReduce] != false {
			t.Errorf("%s: %v", argument, got)
		}
	}
}

// Sizes follow vLLM's human_readable_int: decimal k/m/g/t, binary K/M/G/T, no
// decimals with a binary suffix, and decimals truncated with a decimal one.
func TestParseSize(t *testing.T) {
	cases := map[string]float64{"8192": 8192, "8k": 8000, "8K": 8192, "25.6k": 25600, "2M": 2097152, "1g": 1e9, "1T": 1099511627776}
	for text, want := range cases {
		if got, ok := parseSize(text); !ok || got != want {
			t.Errorf("parseSize(%q) = %v, %v; want %v", text, got, ok, want)
		}
	}
	for _, text := range []string{"1.5G", "8kb", "", "auto", "-1", "8 k"} {
		if got, ok := parseSize(text); ok {
			t.Errorf("parseSize(%q) = %v, want refused", text, got)
		}
	}
	if got := vllmKnobsFromArgs([]string{"--max-num-batched-tokens", "8k"}); got[KnobVLLMMaxNumBatchedTokens] != 8000.0 {
		t.Errorf("--max-num-batched-tokens 8k: %v", got)
	}
}

func TestVLLMKnobsFromArgsBooleanSpellingsAndUnparsableValues(t *testing.T) {
	if got := vllmKnobsFromArgs([]string{"--no-enable-prefix-caching"}); got[KnobVLLMEnablePrefixCaching] != false {
		t.Errorf("--no- spelling: %v", got)
	}
	if got := vllmKnobsFromArgs([]string{"--enable-prefix-caching=false"}); got[KnobVLLMEnablePrefixCaching] != false {
		t.Errorf("=false spelling: %v", got)
	}
	got := vllmKnobsFromArgs([]string{"--max-num-seqs", "lots", "--max-num-batched-tokens"})
	if len(got) != 0 {
		t.Errorf("unparsable or valueless flags must leave the knob absent, got %v", got)
	}
	if got := vllmKnobsFromArgs([]string{"--max-num-seqs", "64", "--max-num-seqs", "128"}); got[KnobVLLMMaxNumSeqs] != 128.0 {
		t.Errorf("the last occurrence must win: %v", got)
	}
}

func TestVLLMKnobsFromWorkloadRequirePodsToAgree(t *testing.T) {
	workload := snapshotVLLM(t)
	knobs := newVLLMAdapter().KnobsFromWorkload(workload)
	if knobs[KnobVLLMMaxNumSeqs] != 256.0 {
		t.Errorf("both pods set 256: %v", knobs)
	}
	if _, has := knobs[KnobVLLMEnablePrefixCaching]; has {
		t.Errorf("only one pod sets prefix caching, so it must be absent: %v", knobs)
	}
	workload.Pods[0].Containers[0].Args = []string{"--max-num-seqs", "128"}
	if _, has := newVLLMAdapter().KnobsFromWorkload(workload)[KnobVLLMMaxNumSeqs]; has {
		t.Error("pods that disagree must leave the knob absent")
	}
}

func TestAgreedKnobsDropsConflicts(t *testing.T) {
	got := agreedKnobs([]Knobs{{"a": 1.0, "b": 2.0}, {"a": 1.0, "b": 3.0}, {"c": true}})
	want := Knobs{"a": 1.0, "c": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agreedKnobs = %v, want %v", got, want)
	}
}

func TestLiteLLMWorkersFromFlagOrEnv(t *testing.T) {
	adapter, err := AdapterFor(spec.TargetTypeLitellm)
	if err != nil {
		t.Fatal(err)
	}
	two, four := "2", "4"
	pod := func(args []string, env ...spec.EnvVar) spec.Pod {
		return spec.Pod{Name: "p", Containers: []spec.Container{{Name: "litellm", Args: args, Env: env}}}
	}
	cases := []struct {
		name string
		pods []spec.Pod
		want Knobs
	}{
		{"flag", []spec.Pod{pod([]string{"--config", "c.yaml", "--num_workers", "4"})}, Knobs{"litellm.num_workers": 4.0}},
		{"env as captured", []spec.Pod{pod(nil, EnvEntry{Name: "NUM_WORKERS", Value: &two})}, Knobs{"litellm.num_workers": 2.0}},
		{"env read back from the bundle", []spec.Pod{pod(nil, map[string]any{"name": "NUM_WORKERS", "value": "2", "is_redacted": false})}, Knobs{"litellm.num_workers": 2.0}},
		{"the flag wins over the env", []spec.Pod{pod([]string{"--num_workers=4"}, EnvEntry{Name: "NUM_WORKERS", Value: &two})}, Knobs{"litellm.num_workers": 4.0}},
		{"pods disagree", []spec.Pod{pod([]string{"--num_workers", "4"}), pod(nil, EnvEntry{Name: "NUM_WORKERS", Value: &four}), pod([]string{"--num_workers", "2"})}, Knobs{}},
		{"neither: the default is not assumed", []spec.Pod{pod([]string{"--port", "4000"})}, Knobs{}},
	}
	for _, c := range cases {
		got := adapter.KnobsFromWorkload(spec.Workload_2{Pods: c.pods})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
