package input

// Signal is a quantity the analyzer needs, derived from the raw catalog
// entries that signals.Table lists for it. A signal is a time series of one
// number (a ratio between 0 and 1, a count, a cumulative total); the
// derivation, including which pods it covers and how replicas are combined,
// is in package signals and nowhere else.
type Signal string

// The signals. Each maps to real catalog entries in signals.Table.
const (
	// BenchCPUUsage is the bench pod's CPU as a fraction of its limit.
	BenchCPUUsage Signal = "bench_cpu_usage"
	// BenchCPUThrottle is the share of CFS periods in which the bench pod was throttled.
	BenchCPUThrottle Signal = "bench_cpu_throttle"
	// TargetCPUUsage is the busiest target container's CPU as a fraction of its limit (its request without a limit).
	TargetCPUUsage Signal = "target_cpu_usage"
	// TargetCPUThrottle is the largest throttle ratio among target containers.
	TargetCPUThrottle Signal = "target_cpu_throttle"
	// ReadyPods is the ready pod count of the target's namespace (a trailing minimum).
	ReadyPods Signal = "ready_pods"
	// Restarts is the restarts of the target's containers in the window, summed.
	Restarts Signal = "restarts"

	// RequestsRunning is vLLM's running requests summed across replicas.
	RequestsRunning Signal = "requests_running"
	// GenerationTokens is vLLM's cumulative output-token counter summed across replicas, corrected for resets.
	GenerationTokens Signal = "generation_tokens"
	// PrefixCacheHits and PrefixCacheQueries are vLLM's cumulative prefix-cache counters (tokens), summed across replicas, corrected for resets.
	PrefixCacheHits    Signal = "prefix_cache_hits"
	PrefixCacheQueries Signal = "prefix_cache_queries"
	// RequestsWaiting is vLLM's waiting requests summed across replicas.
	RequestsWaiting Signal = "requests_waiting"
	// Preemptions is vLLM's cumulative preemption counter summed across replicas, corrected for resets.
	Preemptions Signal = "preemptions"
	// KVCacheUsage is the fullest replica's KV cache, 0 to 1.
	KVCacheUsage Signal = "kv_cache_usage"
	// PromptTokens is vLLM's cumulative input-token counter summed across replicas, corrected for resets.
	PromptTokens Signal = "prompt_tokens"
	// VLLMQueueSecondsSum and VLLMQueueCount are vLLM's cumulative request queue-time histogram sum and count, summed across replicas.
	VLLMQueueSecondsSum Signal = "vllm_queue_seconds_sum"
	VLLMQueueCount      Signal = "vllm_queue_count"
	// VLLMPrefillSecondsSum and VLLMPrefillCount are vLLM's cumulative request prefill-time histogram sum and count.
	VLLMPrefillSecondsSum Signal = "vllm_prefill_seconds_sum"
	VLLMPrefillCount      Signal = "vllm_prefill_count"
	// VLLMDecodeSecondsSum and VLLMDecodeCount are vLLM's cumulative request decode-time histogram sum and count.
	VLLMDecodeSecondsSum Signal = "vllm_decode_seconds_sum"
	VLLMDecodeCount      Signal = "vllm_decode_count"

	// QueueSecondsSum and QueueCount are LiteLLM's cumulative queue-time histogram sum and count, summed across replicas.
	QueueSecondsSum Signal = "queue_seconds_sum"
	QueueCount      Signal = "queue_count"
	// RouterConcurrency is Bifrost's active requests as a fraction of the bifrost.concurrency knob, busiest replica.
	RouterConcurrency Signal = "router_concurrency"

	// GPUEngineActive is DCGM PROF_GR_ENGINE_UTIL_RATIO of the busiest target GPU, 0 to 1.
	GPUEngineActive Signal = "gpu_engine_active"
	// GPUUtil is DCGM GPU_UTIL_RATIO of the busiest target GPU, 0 to 1.
	GPUUtil Signal = "gpu_util"
	// GPUMemoryUsedBytes is DCGM FB_USED_BYTES of the fullest target GPU.
	GPUMemoryUsedBytes Signal = "gpu_memory_used_bytes"
	// GPUMemoryFreeFraction is FB_FREE over FB_USED + FB_FREE of the fullest target GPU, 0 to 1.
	GPUMemoryFreeFraction Signal = "gpu_memory_free_fraction"
	// GPUMemoryCapacityBytes is DCGM FB_CAPACITY_BYTES of the largest target GPU.
	GPUMemoryCapacityBytes Signal = "gpu_memory_capacity_bytes"
	// GPUMemoryControllerUtil is DCGM MEMORY_UTIL_RATIO (memory controller busy) of the busiest target GPU, 0 to 1.
	GPUMemoryControllerUtil Signal = "gpu_memory_controller_util"
	// GPUSMActive is DCGM PROF_SM_UTIL_RATIO of the busiest target GPU, 0 to 1 (profiling metric; absent on GeForce cards).
	GPUSMActive Signal = "gpu_sm_active"
	// GPUTensorActive is DCGM PROF_TENSOR_UTIL_RATIO of the busiest target GPU, 0 to 1 (profiling metric).
	GPUTensorActive Signal = "gpu_tensor_active"
	// GPUPowerWatts is DCGM BOARD_POWER_WATTS of the target GPU drawing the most.
	GPUPowerWatts Signal = "gpu_power_watts"
	// GPUSMClockHertz is DCGM SM_CLOCK_HERTZ of the slowest target GPU (throttling shows as a drop).
	GPUSMClockHertz Signal = "gpu_sm_clock_hertz"
)

// SignalSource yields derived signals. found is false when the raw entries a
// signal needs were not collected: no evidence, not zero.
type SignalSource interface {
	Signal(signal Signal) (samples []Sample, found bool, err error)
}

// MapSignals is an in-memory SignalSource.
type MapSignals map[Signal][]Sample

// Signal implements SignalSource.
func (m MapSignals) Signal(signal Signal) ([]Sample, bool, error) {
	samples, found := m[signal]
	return samples, found && len(samples) > 0, nil
}

// NoSignals is a SignalSource with nothing collected.
type NoSignals struct{}

// Signal implements SignalSource.
func (NoSignals) Signal(Signal) ([]Sample, bool, error) { return nil, false, nil }
