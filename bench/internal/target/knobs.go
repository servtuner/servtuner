package target

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// RunSpec knob names. One definition each; the analysis's
// declared-vs-actual check compares the RunSpec's knobs against these keys.
const (
	KnobVLLMMaxNumSeqs          = "vllm.max_num_seqs"
	KnobVLLMMaxNumBatchedTokens = "vllm.max_num_batched_tokens"
	KnobVLLMGPUMemoryUtil       = "vllm.gpu_memory_utilization"
	KnobVLLMEnablePrefixCaching = "vllm.enable_prefix_caching"
	KnobVLLMDtype               = "vllm.dtype"
	KnobVLLMTensorParallelSize  = "vllm.tensor_parallel_size"
	KnobVLLMMaxModelLen         = "vllm.max_model_len"
	KnobVLLMAPIServerCount      = "vllm.api_server_count"
	KnobVLLMDataParallelSize    = "vllm.data_parallel_size"
	KnobVLLMPipelineParallel    = "vllm.pipeline_parallel_size"
	KnobVLLMExpertParallel      = "vllm.enable_expert_parallel"
	KnobVLLMKVCacheDtype        = "vllm.kv_cache_dtype"
	KnobVLLMQuantization        = "vllm.quantization"
	KnobVLLMChunkedPrefill      = "vllm.enable_chunked_prefill"
	KnobVLLMEnforceEager        = "vllm.enforce_eager"
	// Scheduling and streaming (task 5.2).
	KnobVLLMStreamInterval       = "vllm.stream_interval"
	KnobVLLMAsyncScheduling      = "vllm.async_scheduling"
	KnobVLLMPerformanceMode      = "vllm.performance_mode"
	KnobVLLMMaxNumActiveSeqs     = "vllm.max_num_active_seqs"
	KnobVLLMLongPrefillThreshold = "vllm.long_prefill_token_threshold"
	KnobVLLMLongPrefillAdaptive  = "vllm.long_prefill_token_threshold_adaptive"
	KnobVLLMPrefillScheduleEvery = "vllm.prefill_schedule_interval"
	KnobVLLMKVCacheMemoryBytes   = "vllm.kv_cache_memory_bytes"
	KnobVLLMBlockSize            = "vllm.block_size"
	KnobVLLMSpecMethod           = "vllm.spec_method"
	KnobVLLMSpecModel            = "vllm.spec_model"
	KnobVLLMSpecTokens           = "vllm.spec_tokens"
	// Admission and overload.
	KnobVLLMMaxNumQueuedReqs    = "vllm.max_num_queued_reqs"
	KnobVLLMMaxNumQueuedTokens  = "vllm.max_num_queued_tokens"
	KnobVLLMWatermark           = "vllm.watermark"
	KnobVLLMReserveFullISL      = "vllm.scheduler_reserve_full_isl"
	KnobVLLMCacheAwareWindow    = "vllm.cache_aware_admission_window"
	KnobVLLMCacheAwareThreshold = "vllm.cache_aware_admission_threshold"
	// Multi-GPU and compilation.
	KnobVLLMDisableCustomAllReduce = "vllm.disable_custom_all_reduce"
	KnobVLLMOptimizationLevel      = "vllm.optimization_level"
	KnobVLLMDecodeContextParallel  = "vllm.decode_context_parallel_size"
	// Kernels, CUDA graphs, frontend and data parallel (task 5.3).
	KnobVLLMMaxCudagraphCapture   = "vllm.max_cudagraph_capture_size"
	KnobVLLMCudagraphMode         = "vllm.cudagraph_mode"
	KnobVLLMAttentionBackend      = "vllm.attention_backend"
	KnobVLLMLinearBackend         = "vllm.linear_backend"
	KnobVLLMMoEBackend            = "vllm.moe_backend"
	KnobVLLMFlashInferAutotune    = "vllm.enable_flashinfer_autotune"
	KnobVLLMRendererNumWorkers    = "vllm.renderer_num_workers"
	KnobVLLMPrefixCachingHashAlgo = "vllm.prefix_caching_hash_algo"
	KnobVLLMDPSyncInterval        = "vllm.dp_sync_interval"
	KnobVLLMDPHybridLB            = "vllm.data_parallel_hybrid_lb"
	KnobVLLMDPExternalLB          = "vllm.data_parallel_external_lb"
	KnobVLLMNUMABind              = "vllm.numa_bind"
	// Mixture-of-experts.
	KnobVLLMEnableEPLB      = "vllm.enable_eplb"
	KnobVLLMAll2AllBackend  = "vllm.all2all_backend"
	KnobVLLMExpertPlacement = "vllm.expert_placement_strategy"
	KnobVLLMEnableDBO       = "vllm.enable_dbo"
	// Logging that costs throughput or removes the metrics (rule V12).
	KnobVLLMEnableLogRequests  = "vllm.enable_log_requests"
	KnobVLLMIterationDetails   = "vllm.enable_logging_iteration_details"
	KnobVLLMDisableLogStats    = "vllm.disable_log_stats"
	KnobBifrostConcurrency     = "bifrost.concurrency"
	KnobBifrostBufferSize      = "bifrost.buffer_size"
	KnobBifrostInitialPoolSize = "bifrost.initial_pool_size"
)

// knobKind says how a captured text value is typed.
type knobKind int

const (
	knobNumber knobKind = iota
	// knobInteger is a number vLLM parses with int: 64.5 sequences is refused.
	knobInteger
	// knobSize is a count vLLM parses with human_readable_int: a plain integer
	// or one with a decimal (k, m, g, t) or binary (K, M, G, T) suffix.
	knobSize
	knobBoolean
	// knobSwitch is a boolean knob behind a store_true flag: the flag turns it
	// on and vLLM has no spelling that turns it off.
	knobSwitch
	knobText
)

// vllmFlag ties a vLLM command-line flag to its knob. The first flag is the
// long form; the rest are vLLM's aliases (checked against v0.34.0,
// vllm/engine/arg_utils.py and vllm/entrypoints/common/cli_args.py).
type vllmFlag struct {
	knob  string
	kind  knobKind
	flags []string
}

// vllmFlags is every flag bench captures and renders: the built-in knobs, then every other flag in
// vLLM's catalog that takes a single value, named vllm.<field>.
var vllmFlags = append(builtInVLLMFlags, catalogVLLMFlags()...)

// builtInVLLMFlags are the knobs servtuner knows by name: labelled, captured from /server_info too,
// and spelled with their own kinds.
var builtInVLLMFlags = []vllmFlag{
	{KnobVLLMMaxNumSeqs, knobNumber, []string{"--max-num-seqs"}},
	{KnobVLLMMaxNumBatchedTokens, knobSize, []string{"--max-num-batched-tokens"}},
	{KnobVLLMGPUMemoryUtil, knobNumber, []string{"--gpu-memory-utilization"}},
	{KnobVLLMTensorParallelSize, knobNumber, []string{"--tensor-parallel-size", "-tp"}},
	{KnobVLLMDataParallelSize, knobNumber, []string{"--data-parallel-size", "-dp"}},
	{KnobVLLMPipelineParallel, knobNumber, []string{"--pipeline-parallel-size", "-pp"}},
	{KnobVLLMAPIServerCount, knobNumber, []string{"--api-server-count", "-asc"}},
	{KnobVLLMMaxModelLen, knobSize, []string{"--max-model-len"}},
	{KnobVLLMDtype, knobText, []string{"--dtype"}},
	{KnobVLLMKVCacheDtype, knobText, []string{"--kv-cache-dtype"}},
	{KnobVLLMQuantization, knobText, []string{"--quantization", "-q"}},
	{KnobVLLMEnablePrefixCaching, knobBoolean, []string{"--enable-prefix-caching"}},
	{KnobVLLMChunkedPrefill, knobBoolean, []string{"--enable-chunked-prefill"}},
	{KnobVLLMExpertParallel, knobBoolean, []string{"--enable-expert-parallel", "-ep"}},
	{KnobVLLMEnforceEager, knobBoolean, []string{"--enforce-eager"}},
	{KnobVLLMStreamInterval, knobNumber, []string{"--stream-interval"}},
	{KnobVLLMAsyncScheduling, knobBoolean, []string{"--async-scheduling"}},
	{KnobVLLMPerformanceMode, knobText, []string{"--performance-mode"}},
	{KnobVLLMMaxNumActiveSeqs, knobNumber, []string{"--max-num-active-seqs"}},
	{KnobVLLMLongPrefillThreshold, knobNumber, []string{"--long-prefill-token-threshold"}},
	{KnobVLLMLongPrefillAdaptive, knobBoolean, []string{"--long-prefill-token-threshold-adaptive"}},
	{KnobVLLMPrefillScheduleEvery, knobNumber, []string{"--prefill-schedule-interval"}},
	{KnobVLLMKVCacheMemoryBytes, knobSize, []string{"--kv-cache-memory-bytes"}},
	{KnobVLLMBlockSize, knobNumber, []string{"--block-size"}},
	{KnobVLLMSpecMethod, knobText, []string{"--spec-method"}},
	{KnobVLLMSpecModel, knobText, []string{"--spec-model"}},
	{KnobVLLMSpecTokens, knobNumber, []string{"--spec-tokens"}},
	{KnobVLLMMaxNumQueuedReqs, knobNumber, []string{"--max-num-queued-reqs"}},
	{KnobVLLMMaxNumQueuedTokens, knobSize, []string{"--max-num-queued-tokens"}},
	{KnobVLLMWatermark, knobNumber, []string{"--watermark"}},
	{KnobVLLMReserveFullISL, knobBoolean, []string{"--scheduler-reserve-full-isl"}},
	{KnobVLLMCacheAwareWindow, knobNumber, []string{"--cache-aware-admission-window"}},
	{KnobVLLMCacheAwareThreshold, knobNumber, []string{"--cache-aware-admission-threshold"}},
	{KnobVLLMDisableCustomAllReduce, knobBoolean, []string{"--disable-custom-all-reduce"}},
	{KnobVLLMOptimizationLevel, knobNumber, []string{"--optimization-level", "-O"}},
	{KnobVLLMDecodeContextParallel, knobNumber, []string{"--decode-context-parallel-size", "-dcp"}},
	{KnobVLLMMaxCudagraphCapture, knobNumber, []string{"--max-cudagraph-capture-size"}},
	{KnobVLLMAttentionBackend, knobText, []string{"--attention-backend"}},
	{KnobVLLMLinearBackend, knobText, []string{"--linear-backend"}},
	{KnobVLLMMoEBackend, knobText, []string{"--moe-backend"}},
	{KnobVLLMFlashInferAutotune, knobBoolean, []string{"--enable-flashinfer-autotune"}},
	{KnobVLLMRendererNumWorkers, knobNumber, []string{"--renderer-num-workers"}},
	{KnobVLLMPrefixCachingHashAlgo, knobText, []string{"--prefix-caching-hash-algo"}},
	{KnobVLLMDPSyncInterval, knobNumber, []string{"--dp-sync-interval"}},
	{KnobVLLMDPHybridLB, knobBoolean, []string{"--data-parallel-hybrid-lb"}},
	{KnobVLLMDPExternalLB, knobBoolean, []string{"--data-parallel-external-lb"}},
	{KnobVLLMNUMABind, knobBoolean, []string{"--numa-bind"}},
	{KnobVLLMEnableEPLB, knobBoolean, []string{"--enable-eplb"}},
	{KnobVLLMAll2AllBackend, knobText, []string{"--all2all-backend"}},
	{KnobVLLMExpertPlacement, knobText, []string{"--expert-placement-strategy"}},
	{KnobVLLMEnableDBO, knobBoolean, []string{"--enable-dbo"}},
	{KnobVLLMEnableLogRequests, knobBoolean, []string{"--enable-log-requests"}},
	{KnobVLLMIterationDetails, knobBoolean, []string{"--enable-logging-iteration-details"}},
	{KnobVLLMDisableLogStats, knobSwitch, []string{"--disable-log-stats"}},
}

// compilationConfigFlags are --compilation-config and its alias; its fields
// can also be set one at a time as "--compilation-config.<field> value".
var compilationConfigFlags = []string{"--compilation-config", "-cc"}

// compilationConfigKnobs are the CompilationConfig fields read as knobs.
var compilationConfigKnobs = []struct {
	field string
	knob  string
	kind  knobKind
}{
	{"cudagraph_mode", KnobVLLMCudagraphMode, knobText},
	{"max_cudagraph_capture_size", KnobVLLMMaxCudagraphCapture, knobNumber},
}

// IsVLLMKnob reports whether name is a vLLM knob this package captures and
// renders, driven by the same tables: a built-in knob or any single-value flag
// in vLLM's catalog.
func IsVLLMKnob(name string) bool {
	for _, flag := range vllmFlags {
		if flag.knob == name {
			return true
		}
	}
	for _, knob := range compilationConfigKnobs {
		if knob.knob == name {
			return true
		}
	}
	return false
}

// compilationKnobsFromArgs reads the CUDA graph knobs from --compilation-config
// (-cc), given as a JSON object or field by field in vLLM's dotted form
// ("-cc.cudagraph_mode=FULL"). The dedicated --max-cudagraph-capture-size flag
// is read by vllmFlags; when both are given, the dotted or JSON value here
// overrides it, which is a misconfiguration vLLM rejects anyway.
func compilationKnobsFromArgs(args []string) Knobs {
	knobs := Knobs{}
	if text, found := flagValue(args, compilationConfigFlags); found {
		var config map[string]any
		if json.Unmarshal([]byte(text), &config) == nil {
			for _, field := range compilationConfigKnobs {
				if value, ok := knobFromJSON(config[field.field], field.kind); ok {
					knobs[field.knob] = value
				}
			}
		}
	}
	for _, field := range compilationConfigKnobs {
		var dotted []string
		for _, flag := range compilationConfigFlags {
			dotted = append(dotted, flag+"."+field.field)
		}
		if text, found := flagValue(args, dotted); found {
			if value, ok := parseKnobValue(text, field.kind); ok {
				knobs[field.knob] = value
			}
		}
	}
	return knobs
}

// humanReadableSize matches vLLM's human_readable_int suffix form
// (vllm/utils/argparse_utils.py): a number and one multiplier letter.
var humanReadableSize = regexp.MustCompile(`^(\d+(?:\.\d+)?)([kKmMgGtT])$`)

// sizeMultipliers are human_readable_int's: lower case is decimal, upper case
// binary.
var sizeMultipliers = map[string]float64{
	"k": 1e3, "m": 1e6, "g": 1e9, "t": 1e12,
	"K": 1024, "M": 1048576, "G": 1073741824, "T": 1099511627776, // 2^10, 2^20, 2^30, 2^40
}

// parseSize reads a human_readable_int value. Like vLLM, it refuses a decimal
// with a binary suffix ("1.5G") and truncates a decimal one ("25.6k" is
// 25600).
func parseSize(text string) (float64, bool) {
	match := humanReadableSize.FindStringSubmatch(text)
	if match == nil {
		// Unsigned: max_model_len's -1 means auto, which is not a value.
		number, err := strconv.ParseUint(text, 10, 64)
		return float64(number), err == nil
	}
	number, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, false
	}
	isBinary := strings.ToUpper(match[2]) == match[2]
	if isBinary && strings.Contains(match[1], ".") {
		return 0, false
	}
	return math.Trunc(number * sizeMultipliers[match[2]]), true
}

// expandOptimizationFlag rewrites vLLM's attached -O spellings ("-O3",
// "-O=3") as "--optimization-level 3", as its FlexibleArgumentParser does.
// "-O 3" is left alone: flagValue reads it through the -O alias.
func expandOptimizationFlag(args []string) []string {
	expanded := make([]string, 0, len(args))
	for _, argument := range args {
		level, attached := strings.CutPrefix(argument, "-O")
		if !attached || level == "" || strings.HasPrefix(argument, "--") {
			expanded = append(expanded, argument)
			continue
		}
		expanded = append(expanded, "--optimization-level", strings.TrimPrefix(level, "="))
	}
	return expanded
}

// parseKnobValue types a captured text value. It returns false for a value that
// does not parse as its kind, which leaves the knob unverified.
func parseKnobValue(text string, kind knobKind) (spec.KnobValue, bool) {
	text = strings.TrimSpace(text)
	switch kind {
	case knobNumber:
		number, err := strconv.ParseFloat(text, 64)
		return number, err == nil
	case knobInteger:
		number, err := strconv.ParseInt(text, 10, 64)
		return float64(number), err == nil
	case knobSize:
		number, ok := parseSize(text)
		if !ok {
			return nil, false
		}
		return number, true
	case knobBoolean:
		switch strings.ToLower(text) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		return nil, false
	default:
		return text, text != ""
	}
}

// flagValue finds a flag's value in container args, in either "--flag value" or
// "--flag=value" form. The last occurrence wins, as it does for the server's
// argument parser.
func flagValue(args []string, names []string) (string, bool) {
	var value string
	var found bool
	for index, argument := range args {
		for _, name := range names {
			switch {
			case argument == name && index+1 < len(args):
				value, found = args[index+1], true
			case strings.HasPrefix(argument, name+"="):
				value, found = strings.TrimPrefix(argument, name+"="), true
			}
		}
	}
	return value, found
}

// negatedFlags lists the spellings that turn a long boolean flag off: vLLM's
// argparse adds "--no-<flag>", and since v0.34 its EnableDisableAction also
// pairs "--enable-x" with "--disable-x", both ways (vllm/utils/argparse_utils.py).
func negatedFlags(name string) []string {
	bare := strings.TrimPrefix(name, "--")
	negated := []string{"--no-" + bare}
	if rest, ok := strings.CutPrefix(bare, "enable-"); ok {
		negated = append(negated, "--disable-"+rest)
	}
	if rest, ok := strings.CutPrefix(bare, "disable-"); ok {
		negated = append(negated, "--enable-"+rest)
	}
	return negated
}

// booleanFlagValue reads a boolean flag in each of vLLM's spellings: the flag
// or an alias, a negated form (negatedFlags) and "--flag=false". The last
// occurrence wins.
func booleanFlagValue(args []string, names []string) (bool, bool) {
	negated := negatedFlags(names[0])
	var value, found bool
	for _, argument := range args {
		switch {
		case slices.Contains(names, argument):
			value, found = true, true
		case slices.Contains(negated, argument):
			value, found = false, true
		case strings.HasPrefix(argument, names[0]+"="):
			parsed, ok := parseKnobValue(strings.TrimPrefix(argument, names[0]+"="), knobBoolean)
			if ok {
				value, found = parsed.(bool), true
			}
		}
	}
	return value, found
}

// vllmKnobsFromArgs reads the knobs set on one container's command line.
func vllmKnobsFromArgs(args []string) Knobs {
	args = expandOptimizationFlag(args)
	knobs := Knobs{}
	for _, flag := range vllmFlags {
		switch flag.kind {
		case knobBoolean:
			if value, found := booleanFlagValue(args, flag.flags); found {
				knobs[flag.knob] = value
			}
			continue
		case knobSwitch:
			if slices.ContainsFunc(args, func(argument string) bool { return slices.Contains(flag.flags, argument) }) {
				knobs[flag.knob] = true
			}
			continue
		}
		text, found := flagValue(args, flag.flags)
		if !found {
			continue
		}
		if value, ok := parseKnobValue(text, flag.kind); ok {
			knobs[flag.knob] = value
		}
	}
	return overlayKnobs(knobs, compilationKnobsFromArgs(args))
}

// agreedKnobs merges knob sets from several sources of equal standing (the
// pods of a workload, or the workloads of a run). A knob the sources disagree
// on is dropped: it has no single actual value, so it stays unverified rather
// than being compared against an arbitrary pod.
func agreedKnobs(sets []Knobs) Knobs {
	merged := Knobs{}
	disagreed := map[string]bool{}
	for _, set := range sets {
		for name, value := range set {
			previous, seen := merged[name]
			if seen && previous != value {
				disagreed[name] = true
			}
			merged[name] = value
		}
	}
	for name := range disagreed {
		delete(merged, name)
	}
	return merged
}

// overlayKnobs returns base with the knobs of stronger set over it.
func overlayKnobs(base, stronger Knobs) Knobs {
	merged := Knobs{}
	for name, value := range base {
		merged[name] = value
	}
	for name, value := range stronger {
		merged[name] = value
	}
	return merged
}

// podKnobs extracts knobs per pod and requires the pods to agree.
func podKnobs(workload spec.Workload_2, extract func(args []string) Knobs) Knobs {
	var perContainer []Knobs
	for _, pod := range workload.Pods {
		podSet := Knobs{}
		for _, container := range pod.Containers {
			podSet = overlayKnobs(podSet, extract(container.Args))
		}
		perContainer = append(perContainer, podSet)
	}
	// A knob missing on one pod and present on another is a disagreement too.
	return requireOnEvery(perContainer)
}

func requireOnEvery(sets []Knobs) Knobs {
	merged := agreedKnobs(sets)
	for name := range merged {
		for _, set := range sets {
			if _, ok := set[name]; !ok {
				delete(merged, name)
			}
		}
	}
	return merged
}
