package target

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// vllmCacheConfigMetric is the Prometheus info metric whose labels are the
// fields of vLLM's CacheConfig.
const vllmCacheConfigMetric = "vllm:cache_config_info"

// cacheConfigEngineLabel separates the per-engine rows from the extra row vLLM
// exports with an empty engine; counting both would double the engine.
const cacheConfigEngineLabel = "engine"

type vllmAdapter struct {
	kubeReader
}

func newVLLMAdapter() TargetAdapter {
	return vllmAdapter{kubeReader{spec.TargetTypeVllm}}
}

// DefaultCatalogs implements TargetAdapter.
func (vllmAdapter) DefaultCatalogs() []string { return []string{CatalogVLLM, CatalogKubernetes} }

// KnobsFromWorkload implements TargetAdapter: the flags on the container
// command line, where the pods agree. A flag the command line does not set is
// absent, because the default depends on the vLLM version and the hardware.
func (vllmAdapter) KnobsFromWorkload(workload spec.Workload_2) Knobs {
	return podKnobs(workload, vllmKnobsFromArgs)
}

// Info implements TargetAdapter. /version, /v1/models and /metrics are read
// on every run; /server_info exists only in dev mode and its absence is normal.
func (vllmAdapter) Info(ctx context.Context, endpoint Endpoint) (Info, error) {
	info := Info{Knobs: Knobs{}}
	var problems []error

	if err := fetchVLLMVersion(ctx, endpoint, &info); err != nil {
		problems = append(problems, err)
	}
	if err := fetchVLLMModels(ctx, endpoint, &info); err != nil {
		problems = append(problems, err)
	}
	if err := fetchCacheConfig(ctx, endpoint, &info); err != nil {
		problems = append(problems, err)
	}
	if err := fetchServerInfoKnobs(ctx, endpoint, &info); err != nil && !errors.Is(err, errNotFound) {
		problems = append(problems, err)
	}
	return info, errors.Join(problems...)
}

func fetchVLLMVersion(ctx context.Context, endpoint Endpoint, info *Info) error {
	document, err := endpoint.getJSON(ctx, "/version")
	if err != nil {
		return err
	}
	response, _ := document.(map[string]any)
	if version, ok := response["version"].(string); ok && version != "" {
		info.TargetInfo.Version = &version
	}
	return nil
}

// fetchVLLMModels reads /v1/models: the served models, and the context length
// vLLM serves them with (max_model_len, the same for every model of a server).
func fetchVLLMModels(ctx context.Context, endpoint Endpoint, info *Info) error {
	document, err := endpoint.getJSON(ctx, "/v1/models")
	if err != nil {
		return err
	}
	info.TargetInfo.Models = modelIDs(document)
	response, _ := document.(map[string]any)
	models, _ := response["data"].([]any)
	for _, model := range models {
		entry, _ := model.(map[string]any)
		if value, ok := knobFromJSON(entry["max_model_len"], knobNumber); ok {
			info.Knobs[KnobVLLMMaxModelLen] = value
			break
		}
	}
	return nil
}

// fetchCacheConfig reads cache_config_info from /metrics and derives the knobs
// it carries.
func fetchCacheConfig(ctx context.Context, endpoint Endpoint, info *Info) error {
	exposition, err := endpoint.get(ctx, "/metrics")
	if err != nil {
		return err
	}
	cacheConfig := cacheConfigLabels(samplesOf(exposition, vllmCacheConfigMetric))
	if cacheConfig == nil {
		return nil
	}
	info.TargetInfo.CacheConfig = cacheConfig
	if value, ok := parseKnobValue(cacheConfig["gpu_memory_utilization"], knobNumber); ok {
		info.Knobs[KnobVLLMGPUMemoryUtil] = value
	}
	// vLLM stringifies Python values, so a boolean arrives as True or False.
	if value, ok := parseKnobValue(cacheConfig["enable_prefix_caching"], knobBoolean); ok {
		info.Knobs[KnobVLLMEnablePrefixCaching] = value
	}
	if value, ok := parseKnobValue(cacheConfig["cache_dtype"], knobText); ok {
		info.Knobs[KnobVLLMKVCacheDtype] = value
	}
	if value, ok := parseKnobValue(cacheConfig["block_size"], knobNumber); ok {
		info.Knobs[KnobVLLMBlockSize] = value
	}
	return nil
}

// cacheConfigLabels picks the first per-engine sample (a non-empty engine
// label), falling back to the engine-less row, and returns its labels without
// the engine label itself. It returns nil when there is no sample.
func cacheConfigLabels(samples []map[string]string) map[string]string {
	var chosen map[string]string
	for _, sample := range samples {
		if sample[cacheConfigEngineLabel] != "" {
			chosen = sample
			break
		}
		if chosen == nil {
			chosen = sample
		}
	}
	if chosen == nil {
		return nil
	}
	labels := make(map[string]string, len(chosen))
	for name, value := range chosen {
		if name != cacheConfigEngineLabel {
			labels[name] = value
		}
	}
	return labels
}

// serverInfoKnobFields are the VllmConfig field names read from /server_info,
// keyed to their knob. A field without a section is unique in the config, so a
// depth-first search by name finds it without knowing the nesting; one that
// also occurs elsewhere (quantization and enforce_eager are in
// speculative_config too) is searched for only inside its section.
var serverInfoKnobFields = []struct {
	section string
	field   string
	knob    string
	kind    knobKind
}{
	{"", "max_num_seqs", KnobVLLMMaxNumSeqs, knobNumber},
	{"", "max_num_batched_tokens", KnobVLLMMaxNumBatchedTokens, knobNumber},
	{"", "tensor_parallel_size", KnobVLLMTensorParallelSize, knobNumber},
	{"", "data_parallel_size", KnobVLLMDataParallelSize, knobNumber},
	{"", "pipeline_parallel_size", KnobVLLMPipelineParallel, knobNumber},
	{"", "enable_expert_parallel", KnobVLLMExpertParallel, knobBoolean},
	{"", "gpu_memory_utilization", KnobVLLMGPUMemoryUtil, knobNumber},
	{"", "enable_prefix_caching", KnobVLLMEnablePrefixCaching, knobBoolean},
	{"", "cache_dtype", KnobVLLMKVCacheDtype, knobText},
	{"", "enable_chunked_prefill", KnobVLLMChunkedPrefill, knobBoolean},
	{"model_config", "quantization", KnobVLLMQuantization, knobText},
	{"model_config", "enforce_eager", KnobVLLMEnforceEager, knobBoolean},
	{"scheduler_config", "stream_interval", KnobVLLMStreamInterval, knobNumber},
	{"scheduler_config", "async_scheduling", KnobVLLMAsyncScheduling, knobBoolean},
	{"scheduler_config", "max_num_active_seqs", KnobVLLMMaxNumActiveSeqs, knobNumber},
	{"scheduler_config", "long_prefill_token_threshold", KnobVLLMLongPrefillThreshold, knobNumber},
	{"scheduler_config", "long_prefill_token_threshold_adaptive", KnobVLLMLongPrefillAdaptive, knobBoolean},
	{"scheduler_config", "prefill_schedule_interval", KnobVLLMPrefillScheduleEvery, knobNumber},
	{"scheduler_config", "max_num_queued_reqs", KnobVLLMMaxNumQueuedReqs, knobNumber},
	{"scheduler_config", "max_num_queued_tokens", KnobVLLMMaxNumQueuedTokens, knobNumber},
	{"scheduler_config", "watermark", KnobVLLMWatermark, knobNumber},
	{"scheduler_config", "scheduler_reserve_full_isl", KnobVLLMReserveFullISL, knobBoolean},
	{"scheduler_config", "cache_aware_admission_window", KnobVLLMCacheAwareWindow, knobNumber},
	{"scheduler_config", "cache_aware_admission_threshold", KnobVLLMCacheAwareThreshold, knobNumber},
	{"cache_config", "kv_cache_memory_bytes", KnobVLLMKVCacheMemoryBytes, knobNumber},
	{"cache_config", "block_size", KnobVLLMBlockSize, knobNumber},
	{"parallel_config", "disable_custom_all_reduce", KnobVLLMDisableCustomAllReduce, knobBoolean},
	{"parallel_config", "decode_context_parallel_size", KnobVLLMDecodeContextParallel, knobNumber},
	{"speculative_config", "method", KnobVLLMSpecMethod, knobText},
	{"speculative_config", "model", KnobVLLMSpecModel, knobText},
	{"speculative_config", "num_speculative_tokens", KnobVLLMSpecTokens, knobNumber},
	{"compilation_config", "max_cudagraph_capture_size", KnobVLLMMaxCudagraphCapture, knobNumber},
	{"kernel_config", "linear_backend", KnobVLLMLinearBackend, knobText},
	{"kernel_config", "moe_backend", KnobVLLMMoEBackend, knobText},
	{"kernel_config", "enable_flashinfer_autotune", KnobVLLMFlashInferAutotune, knobBoolean},
	{"model_config", "renderer_num_workers", KnobVLLMRendererNumWorkers, knobNumber},
	{"cache_config", "prefix_caching_hash_algo", KnobVLLMPrefixCachingHashAlgo, knobText},
	{"parallel_config", "dp_sync_interval", KnobVLLMDPSyncInterval, knobNumber},
	{"parallel_config", "data_parallel_hybrid_lb", KnobVLLMDPHybridLB, knobBoolean},
	{"parallel_config", "data_parallel_external_lb", KnobVLLMDPExternalLB, knobBoolean},
	{"parallel_config", "numa_bind", KnobVLLMNUMABind, knobBoolean},
	{"parallel_config", "enable_eplb", KnobVLLMEnableEPLB, knobBoolean},
	{"parallel_config", "all2all_backend", KnobVLLMAll2AllBackend, knobText},
	{"parallel_config", "expert_placement_strategy", KnobVLLMExpertPlacement, knobText},
	{"parallel_config", "enable_dbo", KnobVLLMEnableDBO, knobBoolean},
	{"observability_config", "enable_logging_iteration_details", KnobVLLMIterationDetails, knobBoolean},
	// Not read from /server_info: attention_config.backend and
	// compilation_config.cudagraph_mode are enums whose JSON form is a class
	// path or an internal value, not the name the flag takes; and
	// enable_log_requests and disable_log_stats are not VllmConfig fields.
	// Top-level VllmConfig fields: findField checks the top level first.
	{"", "performance_mode", KnobVLLMPerformanceMode, knobText},
	{"", "optimization_level", KnobVLLMOptimizationLevel, knobNumber},
}

// fetchServerInfoKnobs reads the effective scheduler settings from
// /server_info, which only a server started with VLLM_SERVER_DEV_MODE=1
// exposes. Only knob values are kept: the page also carries environment dumps
// that are never stored.
func fetchServerInfoKnobs(ctx context.Context, endpoint Endpoint, info *Info) error {
	document, err := endpoint.getJSON(ctx, "/server_info?"+url.Values{"config_format": {"json"}}.Encode())
	if err != nil {
		return err
	}
	response, _ := document.(map[string]any)
	configuration := response["vllm_config"]
	if text, isText := configuration.(string); isText {
		// Some versions return the config as a JSON string.
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) != nil {
			return nil
		}
		configuration = decoded
	}
	for _, field := range serverInfoKnobFields {
		scope := configuration
		if field.section != "" {
			sections, _ := configuration.(map[string]any)
			scope = sections[field.section]
		}
		found, ok := findField(scope, field.field)
		if !ok {
			continue
		}
		if value, ok := knobFromJSON(found, field.kind); ok {
			info.Knobs[field.knob] = value
		}
	}
	return nil
}

// findField searches a decoded JSON document depth-first, in key order, for the
// first field with the given name.
func findField(document any, name string) (any, bool) {
	switch typed := document.(type) {
	case map[string]any:
		if value, ok := typed[name]; ok {
			return value, true
		}
		for _, key := range sortedKeys(typed) {
			if value, ok := findField(typed[key], name); ok {
				return value, true
			}
		}
	case []any:
		for _, element := range typed {
			if value, ok := findField(element, name); ok {
				return value, true
			}
		}
	}
	return nil, false
}

// knobFromJSON accepts a JSON number or boolean (or its string spelling) as a
// knob of the given kind.
func knobFromJSON(value any, kind knobKind) (spec.KnobValue, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, kind == knobNumber || kind == knobSize || (kind == knobInteger && typed == math.Trunc(typed))
	case bool:
		return typed, kind == knobBoolean
	case string:
		return parseKnobValue(typed, kind)
	default:
		return nil, false
	}
}
