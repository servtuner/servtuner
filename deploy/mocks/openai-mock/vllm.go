package main

// The mock as vLLM. Run as `vllm serve <model>
// --flag=value ...` (the image links /usr/local/bin/vllm to this binary), it does what the deployer and the
// tuning loop rely on, as vLLM 0.34 does:
//   - it listens only once "booted", after VLLM_MOCK_BOOT_SECONDS, as vLLM refuses connections until ready;
//   - a flag in VLLM_MOCK_UNKNOWN_FLAGS fails like vLLM's argument parser (usage line, exit 2);
//   - args matching every flag of a set in VLLM_MOCK_INFEASIBLE fail like a KV cache too small (exit 1, the
//     root cause logged before the generic last line);
//   - --ssl-certfile and --ssl-keyfile serve TLS with no client certificate;
//   - /health, and vLLM's metric names: cache_config_info whose num_gpu_blocks grows with
//     --tensor-parallel-size, and with --gpu-memory-utilization only up to VLLM_MOCK_MEMORY_BOUND (0.8), as
//     vLLM 0.34's extensible KV cache does; the two sizing log lines say which limit applied;
//   - --max-num-seqs caps requests in service (the rest queue), so the scheduler knob changes throughput.
// Every other flag is accepted and ignored: the mock emulates the interface, not the engine.

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	vllmCommand = "vllm"
	// vLLM's own defaults where the mock needs one.
	vllmDefaultPort       = "8000"
	vllmDefaultMaxNumSeqs = 256
	vllmDefaultBlockSize  = 16
	// vLLM 0.34 with its extensible KV cache (the default on CUDA): utilization 1.0, the cache sized
	// from the memory free after warmup.
	vllmDefaultGPUMemUtil = 1.0
	vllmMockDefaultBlocks = 2048 // num_gpu_blocks at one GPU once free memory sizes the cache
	// Above this utilization the memory free after warmup sizes the cache, so more utilization adds
	// no blocks (0.73 on the RTX 3070 next to the desktop); below it blocks scale with utilization.
	vllmMockMemoryBound = 0.8
	// The emulated GPU for vLLM's two sizing log lines, chosen so that the api's reading of them
	// (deployer.parse_kv_sizing) crosses from utilization to memory at vllmMockMemoryBound: an
	// 80 GiB device with bound x 80 + 1 GiB free at startup (the 1 GiB is vLLM's sizing margin) and
	// 10 GiB used by the engine besides the cache.
	vllmMockTotalGiB       = 80.0
	vllmMockMarginGiB      = 1.0
	vllmMockEngineGiB      = 10.0
	vllmMockGiBPerBlock    = 0.002 // a block of 16 tokens of a small model: about 2 MiB
	vllmMockDefaultBootSec = 3
	vllmArgparseExit       = 2
	vllmFailureExit        = 1
	// The tokens one running request is assumed to hold, for kv_cache_usage_perc.
	vllmMockTokensPerRequest = 512
)

// VLLMEmulation is what vLLM's metrics report: the KV cache and the scheduler cap.
type VLLMEmulation struct {
	GPUBlocks       int
	BlockSize       int
	GPUMemoryUtil   float64
	MemoryBound     float64 // the utilization above which free memory sizes the cache
	MaxNumSeqs      int
	PrefixCachingOn bool
}

type vllmArgs struct {
	model string
	flags map[string]string // --name -> value; a bare boolean flag has "true"
	raw   []string
}

// parseVLLMArgs reads `serve <model> --flag=value | --flag value | --flag ...`.
func parseVLLMArgs(args []string) (vllmArgs, error) {
	if len(args) == 0 || args[0] != "serve" {
		return vllmArgs{}, fmt.Errorf("the mock emulates only `vllm serve`")
	}
	parsed := vllmArgs{flags: map[string]string{}, raw: args[1:]}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if !strings.HasPrefix(arg, "--") {
			if parsed.model != "" {
				return vllmArgs{}, fmt.Errorf("unrecognized arguments: %s", arg)
			}
			parsed.model = arg
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if !hasValue {
			value = "true"
			if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "--") && parsed.model != "" {
				value = rest[i+1]
				i++
			}
		}
		parsed.flags[name] = value
	}
	if parsed.model == "" {
		return vllmArgs{}, fmt.Errorf("the following arguments are required: model_tag")
	}
	return parsed, nil
}

func (a vllmArgs) number(name string, fallback float64) float64 {
	if value, err := strconv.ParseFloat(a.flags[name], 64); err == nil {
		return value
	}
	return fallback
}

// matchesAll is true when the args hold every --flag=value of the set, e.g.
// "--tensor-parallel-size=1,--max-model-len=32768".
func (a vllmArgs) matchesAll(set string) bool {
	for _, condition := range strings.Split(set, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(condition), "=")
		if got, ok := a.flags[name]; !ok || got != value {
			return false
		}
	}
	return true
}

// runVLLM is the mock's `vllm serve`: it returns the process exit code.
func runVLLM(args []string, environment func(string) string, stdout, stderr io.Writer, sleep func(time.Duration)) int {
	parsed, err := parseVLLMArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: vllm serve [model_tag] [options]")
		fmt.Fprintln(stderr, "vllm serve: error:", err)
		return vllmArgparseExit
	}
	for _, flag := range strings.Split(environment("VLLM_MOCK_UNKNOWN_FLAGS"), ",") {
		if flag = strings.TrimSpace(flag); flag != "" {
			if _, ok := parsed.flags[flag]; ok {
				fmt.Fprintln(stderr, "usage: vllm serve [model_tag] [options]")
				fmt.Fprintf(stderr, "vllm serve: error: unrecognized arguments: %s\n", flag)
				return vllmArgparseExit
			}
		}
	}
	bootSeconds := vllmMockDefaultBootSec
	if value, err := strconv.Atoi(environment("VLLM_MOCK_BOOT_SECONDS")); err == nil && value >= 0 {
		bootSeconds = value
	}
	fmt.Fprintf(stdout, "INFO: vllm (servtuner mock) serving %s; loading for %d s\n", parsed.model, bootSeconds)
	sleep(time.Duration(bootSeconds) * time.Second)
	for _, set := range strings.Split(environment("VLLM_MOCK_INFEASIBLE"), ";") {
		if set = strings.TrimSpace(set); set != "" && parsed.matchesAll(set) {
			// As vLLM logs it: the cause in the engine core, then the generic wrapper last.
			fmt.Fprintln(stderr, "ERROR EngineCore failed to start.")
			fmt.Fprintln(stderr, "ValueError: No available memory for the cache blocks. Try increasing `gpu_memory_utilization` when initializing the engine.")
			fmt.Fprintln(stderr, "RuntimeError: Engine core initialization failed. See root cause above. Failed core proc(s): {}")
			return vllmFailureExit
		}
	}
	cfg, err := vllmConfig(parsed, environment)
	if err != nil {
		fmt.Fprintln(stderr, "openai-mock as vllm:", err)
		return vllmFailureExit
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "openai-mock as vllm:", err)
		return vllmFailureExit
	}
	logKVSizing(stderr, cfg.VLLM)
	if err := runUntilSignal(cfg); err != nil {
		fmt.Fprintln(stderr, "openai-mock as vllm:", err)
		return vllmFailureExit
	}
	return 0
}

// vllmConfig turns vLLM's flags into the mock's config. Latency and other
// mock knobs still come from the OPENAI_MOCK_* environment.
func vllmConfig(parsed vllmArgs, environment func(string) string) (Config, error) {
	cfg, err := loadConfig(nil, func(name string) string {
		if name == envPrefix+"TLS_MODE" {
			return tlsModePlatform // the mode comes from the vLLM flags below
		}
		return environment(name)
	}, io.Discard)
	if err != nil {
		return Config{}, err
	}
	host := parsed.flags["--host"]
	port := parsed.flags["--port"]
	if port == "" {
		port = vllmDefaultPort
	}
	cfg.ListenAddress = host + ":" + port
	cfg.ModelName = parsed.model
	if name := parsed.flags["--served-model-name"]; name != "" {
		cfg.ModelName = name
	}
	cert, key := parsed.flags["--ssl-certfile"], parsed.flags["--ssl-keyfile"]
	if cert != "" || key != "" {
		cfg.TLSMode, cfg.CertFile, cfg.KeyFile = tlsModeServer, cert, key
	}
	tensorParallel := parsed.number("--tensor-parallel-size", 1)
	memoryUtil := parsed.number("--gpu-memory-utilization", vllmDefaultGPUMemUtil)
	memoryBound := vllmMockMemoryBound
	if value, err := strconv.ParseFloat(environment("VLLM_MOCK_MEMORY_BOUND"), 64); err == nil && value > 0 && value <= 1 {
		memoryBound = value
	}
	baseBlocks := float64(vllmMockDefaultBlocks)
	if value, err := strconv.Atoi(environment("VLLM_MOCK_GPU_BLOCKS")); err == nil && value > 0 {
		baseBlocks = float64(value)
	}
	maxNumSeqs := int(parsed.number("--max-num-seqs", vllmDefaultMaxNumSeqs))
	cfg.Faults.MaxConcurrency = maxNumSeqs
	cfg.Faults.OverCapacity = overCapacityQueue
	_, prefixOff := parsed.flags["--no-enable-prefix-caching"]
	if _, disabled := parsed.flags["--disable-prefix-caching"]; disabled {
		prefixOff = true
	}
	cfg.VLLM = &VLLMEmulation{
		GPUBlocks:       int(math.Round(baseBlocks * tensorParallel * math.Min(memoryUtil, memoryBound) / memoryBound)),
		MemoryBound:     memoryBound,
		BlockSize:       int(parsed.number("--block-size", vllmDefaultBlockSize)),
		GPUMemoryUtil:   memoryUtil,
		MaxNumSeqs:      maxNumSeqs,
		PrefixCachingOn: !prefixOff,
	}
	return cfg, nil
}

// registerVLLMMetrics adds vLLM's metric names, read from the mock's own counters.
func registerVLLMMetrics(m *metrics, emulation VLLMEmulation) {
	read := func(collector prometheus.Metric) float64 {
		var metric dto.Metric
		if err := collector.Write(&metric); err != nil {
			return 0
		}
		if metric.Gauge != nil {
			return metric.Gauge.GetValue()
		}
		return metric.Counter.GetValue()
	}
	capacityTokens := float64(emulation.GPUBlocks * emulation.BlockSize)
	m.registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "vllm:cache_config_info",
			Help: "Information of the LLMEngine CacheConfig",
			ConstLabels: prometheus.Labels{
				"engine":                 "0",
				"block_size":             strconv.Itoa(emulation.BlockSize),
				"num_gpu_blocks":         strconv.Itoa(emulation.GPUBlocks),
				"gpu_memory_utilization": strconv.FormatFloat(emulation.GPUMemoryUtil, 'f', -1, 64),
				"enable_prefix_caching":  map[bool]string{true: "True", false: "False"}[emulation.PrefixCachingOn],
				"cache_dtype":            "auto",
			},
		}, func() float64 { return 1 }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "vllm:num_requests_running", Help: "Requests in model execution batches."},
			func() float64 { return read(m.inFlight) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "vllm:num_requests_waiting", Help: "Requests waiting to be processed."},
			func() float64 { return read(m.queued) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "vllm:kv_cache_usage_perc", Help: "KV-cache usage. 1 means 100 percent usage."},
			func() float64 { return math.Min(1, read(m.inFlight)*vllmMockTokensPerRequest/capacityTokens) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "vllm:prompt_tokens_total", Help: "Number of prefill tokens processed."},
			func() float64 { return read(m.promptTokens) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "vllm:generation_tokens_total", Help: "Number of generation tokens processed."},
			func() float64 { return read(m.outputTokens) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "vllm:num_preemptions_total", Help: "Cumulative number of preemptions."},
			func() float64 { return 0 }),
	)
}

// logKVSizing writes vLLM 0.34's two KV cache sizing lines, in its format, for the emulated GPU.
func logKVSizing(w io.Writer, emulation *VLLMEmulation) {
	startupFree := emulation.MemoryBound*vllmMockTotalGiB + vllmMockMarginGiB
	requested := emulation.GPUMemoryUtil * vllmMockTotalGiB
	fmt.Fprintf(w, "INFO [gpu_worker.py:1175] Free memory on device (%.2f/%.2f GiB) on startup. Desired GPU memory utilization is (%g, %.2f GiB).\n",
		startupFree, vllmMockTotalGiB, emulation.GPUMemoryUtil, requested)
	fmt.Fprintf(w, "INFO [extensible_kv_cache.py:693] Memory after warmup: %.2f GiB free, 0.00 GiB committed to the KV cache, 0.00 GiB kept for transient activations (0.00 GiB allocated at peak, 0.00 GiB of torch reservation growth); %d KV cache blocks (%.2f GiB) fit within the requested %.2f GiB.\n",
		startupFree-vllmMockEngineGiB, emulation.GPUBlocks, float64(emulation.GPUBlocks)*vllmMockGiBPerBlock, requested)
}
