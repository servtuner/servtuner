package loadgen

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/loadgen/pacer"
	"github.com/servtuner/servtuner/bench/internal/memlimit"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
	"github.com/servtuner/servtuner/bench/internal/workload"
)

// Name and Version identify the engine in environment.json. Bump Version when a
// change alters what the engine measures or records.
const (
	Name    = "loadgen"
	Version = "1"
)

// Defaults for what the RunSpec's engine.options may override.
const (
	DefaultExpectedLatency = 10 * time.Second
	DefaultRequestTimeout  = 60 * time.Second
	DefaultDrainTimeout    = 30 * time.Second
	// minimumInFlightCap keeps a very low rate from getting a cap of a handful.
	minimumInFlightCap = 32
)

// Memory sizing of the default caps. A load client must shed load (Stats.Shed),
// never be OOM-killed: on kind a 10k RPS ladder with the old rate x latency cap
// (100k slots) killed the api pod that runs bench. Half the container's limit
// is the budget; the rest is the process itself (and, with the subprocess
// runner, the api), record buffers and the Go runtime.
const (
	memoryBudgetFraction = 0.5
	// bytesPerInFlight is an in-flight request: its goroutine, the HTTP
	// connection's two goroutines and buffers, the request body and the SSE
	// parser. An estimate (two 4 KiB connection buffers, three goroutine stacks,
	// the body and parser buffers), rounded up generously; not measured.
	bytesPerInFlight = 64 << 10
	// bytesPerWaiting is a due request parked for a slot: its goroutine and body.
	bytesPerWaiting = 16 << 10
)

// Keys of the RunSpec's engine.options (the engine-native passthrough) that
// loadgen reads. All are numbers.
const (
	OptionMaxInFlight            = "max_in_flight"
	OptionMaxWaiting             = "max_waiting"
	OptionExpectedLatencySeconds = "expected_latency_seconds"
	OptionRequestTimeoutSeconds  = "request_timeout_seconds"
	OptionDrainTimeoutSeconds    = "drain_timeout_seconds"
)

// Options configure an Engine. Everything here comes from the pipeline, which
// reads the Secrets; the engine reads no cluster state.
type Options struct {
	// RunID salts the prompts so no two runs share a prefix-cache entry
	// (workload.Options.CacheSalt).
	RunID string
	// BearerToken is the resolved target.auth secret, sent as
	// "Authorization: Bearer". Empty sends no header. Never logged or recorded.
	BearerToken string
	// ClientCertFile, ClientKeyFile and CAFile come from the mounted Secrets.
	// The first two are given exactly when target.tls names a client
	// certificate; without TLS all three are forbidden.
	ClientCertFile, ClientKeyFile, CAFile string
	// ClientSink receives the self-monitoring samples. Nil disables sampling.
	ClientSink engine.ClientSink
	// Now is the one clock of the run, shared with the phase windows. Nil is
	// time.Now. Its readings must carry a monotonic component.
	Now func() time.Time
	// SampleInterval overrides DefaultSampleInterval (for tests).
	SampleInterval time.Duration
	// ShardIndex and ShardCount make this engine one shard of a sharded run
	// (engine.shards): it offers rate / ShardCount, sends every ShardCount-th
	// request of the workload from ShardIndex, and draws its own Poisson
	// arrivals. Zero ShardCount means unsharded.
	ShardIndex, ShardCount int
	// MemoryLimit reports the container's memory limit in bytes, and false when
	// there is none. Nil reads the cgroup (memlimit.Detect).
	MemoryLimit func() (int64, bool, error)
}

// limits are the engine.options after defaults.
type limits struct {
	// isMemoryCapped says the default cap was lowered to fit the memory limit.
	isMemoryCapped bool
	maxInFlight    int
	maxWaiting     int
	requestTimeout time.Duration
	drainTimeout   time.Duration
}

// Engine implements engine.Engine.
type Engine struct {
	options Options
	now     func() time.Time

	// Set by Prepare.
	runSpec      spec.RunSpec
	limits       limits
	transport    *http.Transport
	stagePacers  []pacer.StagePacer
	generator    *workload.Generator
	calibrator   *workload.Calibrator
	endpoint     string
	httpProtocol atomic.Int64 // major version of the last response; 0 until one arrives
	counters     counters
	shedWarning  sync.Once
}

var _ engine.Engine = (*Engine)(nil)

// New returns an engine. Prepare must be called before Run.
func New(options Options) *Engine {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Engine{options: options, now: now}
}

func (*Engine) Name() string    { return Name }
func (*Engine) Version() string { return Version }

// Prepare validates the spec, builds the transport, pacers and workload, and
// sends nothing.
func (e *Engine) Prepare(_ context.Context, runSpec spec.RunSpec) error {
	hop, err := tlsconfig.SpecHop("target", runSpec.Target.Tls, runSpec.Target.Endpoint, e.options.ClientCertFile, e.options.ClientKeyFile, e.options.CAFile)
	if err != nil {
		return err
	}
	tlsConfig, err := tlsconfig.ClientConfig(hop)
	if err != nil {
		return err
	}
	if e.options.ShardCount > 1 {
		// Everything below sees this shard's load: pacers, the in-flight cap
		// and the warmup rate.
		runSpec.Load = spec.ShardLoad(runSpec.Load, e.options.ShardCount)
	}
	stagePacers, err := pacer.ForLoad(runSpec.Load, shardSeed(runSpec.Workload.Seed, e.options.ShardIndex))
	if err != nil {
		return fmt.Errorf("load pacing: %w", err)
	}
	memoryLimit := e.options.MemoryLimit
	if memoryLimit == nil {
		memoryLimit = func() (int64, bool, error) { return memlimit.Detect(memlimit.DefaultCgroupRoot) }
	}
	limitBytes, isLimited, err := memoryLimit()
	if err != nil {
		return fmt.Errorf("read the container memory limit: %w", err)
	}
	if !isLimited {
		limitBytes = 0
	}
	limits, err := resolveLimits(runSpec, limitBytes)
	if err != nil {
		return err
	}
	slog.Info("loadgen limits", "max_in_flight", limits.maxInFlight, "max_waiting", limits.maxWaiting,
		"memory_limit_bytes", limitBytes, "memory_capped", limits.isMemoryCapped)
	calibrator := workload.NewCalibrator(0)
	generator, err := workload.New(runSpec.Workload, runSpec.Target.Model, calibrator, workload.Options{
		CacheSalt:    e.options.RunID,
		SetMinTokens: runSpec.Target.Type == spec.TargetTypeVllm,
		ShardIndex:   e.options.ShardIndex,
		ShardCount:   e.options.ShardCount,
	})
	if err != nil {
		return fmt.Errorf("workload: %w", err)
	}
	e.runSpec = runSpec
	e.limits = limits
	e.stagePacers = stagePacers
	e.calibrator = calibrator
	e.generator = generator
	e.endpoint = runSpec.Target.Endpoint
	// Requests go straight to the transport, so a redirect is never followed:
	// it could carry the bearer token across the boundary the hop was
	// configured for (tlsconfig.NewHTTPClient makes the same choice).
	e.transport = newTransport(tlsConfig, limits.maxInFlight)
	return nil
}

// shardSeedStride separates the shards' Poisson streams: the union of N
// independent Poisson(rate / N) arrival processes is Poisson(rate), while N
// copies of one stream would arrive in lockstep, N at a time. Shard 0 keeps
// the RunSpec's seed, so an unsharded run is unchanged. A large prime.
const shardSeedStride = 1_000_003

func shardSeed(seed, shardIndex int) int {
	return seed + shardIndex*shardSeedStride
}

// newTransport is tuned for many concurrent requests to one host.
//
// MaxIdleConnsPerHost equals the in-flight cap: the default of 2 would close
// and redial a connection for almost every request at high rate, and the
// handshakes would land in TTFB. HTTP/2 is off whenever a TLS config is
// present, which includes every client certificate: Go's HTTP/2 client has its
// own connection and stream limits and is not what a router under test is
// usually driven with, so the version is HTTP/1.1 in both modes and
// is recorded from the responses (HTTPVersion). Compression is off so response
// bytes are the target's own and the client spends no CPU inflating them. No
// proxy is used: the target is addressed directly.
func newTransport(tlsConfig *tls.Config, maxConnections int) *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        maxConnections,
		MaxIdleConnsPerHost: maxConnections,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

// resolveLimits applies the engine.options over the defaults. The in-flight cap
// defaults to the stage peak rate times the expected latency (Little's law), so
// a healthy target never reaches it; pass expected_latency_seconds from the SLO
// to size it for slow models. With a container memory limit (memoryLimitBytes >
// 0), the default caps are also bounded so that every in-flight and waiting
// request fits in the memory budget. Explicit engine.options are never reduced.
func resolveLimits(runSpec spec.RunSpec, memoryLimitBytes int64) (limits, error) {
	expectedLatency, err := durationOption(runSpec.Engine.Options, OptionExpectedLatencySeconds, DefaultExpectedLatency)
	if err != nil {
		return limits{}, err
	}
	requestTimeout, err := durationOption(runSpec.Engine.Options, OptionRequestTimeoutSeconds, DefaultRequestTimeout)
	if err != nil {
		return limits{}, err
	}
	drainTimeout, err := durationOption(runSpec.Engine.Options, OptionDrainTimeoutSeconds, DefaultDrainTimeout)
	if err != nil {
		return limits{}, err
	}
	maxInFlight := max(minimumInFlightCap, int(math.Ceil(peakRate(runSpec.Load)*expectedLatency.Seconds())))
	isMemoryCapped := false
	if memoryLimitBytes > 0 {
		// Each slot can hold one in-flight and one waiting request (max_waiting
		// defaults to max_in_flight).
		affordable := int(float64(memoryLimitBytes) * memoryBudgetFraction / (bytesPerInFlight + bytesPerWaiting))
		if affordable < maxInFlight {
			maxInFlight, isMemoryCapped = max(minimumInFlightCap, affordable), true
		}
	}
	if value, isSet := runSpec.Engine.Options[OptionMaxInFlight]; isSet {
		number, ok := asNumber(value)
		if !ok || number < 1 {
			return limits{}, fmt.Errorf("engine.options.%s: want a positive number, got %v", OptionMaxInFlight, value)
		}
		maxInFlight, isMemoryCapped = int(number), false
	}
	maxWaiting := maxInFlight
	if value, isSet := runSpec.Engine.Options[OptionMaxWaiting]; isSet {
		number, ok := asNumber(value)
		if !ok || number < 0 {
			return limits{}, fmt.Errorf("engine.options.%s: want a number of at least 0, got %v", OptionMaxWaiting, value)
		}
		maxWaiting = int(number)
	}
	return limits{
		maxInFlight: maxInFlight, maxWaiting: maxWaiting, requestTimeout: requestTimeout, drainTimeout: drainTimeout,
		isMemoryCapped: isMemoryCapped,
	}, nil
}

func durationOption(options spec.EngineOptions, key string, fallback time.Duration) (time.Duration, error) {
	value, isSet := options[key]
	if !isSet {
		return fallback, nil
	}
	seconds, ok := asNumber(value)
	if !ok || seconds <= 0 {
		return 0, fmt.Errorf("engine.options.%s: want a positive number of seconds, got %v", key, value)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func asNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	}
	return 0, false
}

// peakRate is the highest instantaneous rate any stage can offer.
func peakRate(load spec.Load) float64 {
	var peak float64
	for index, stage := range load.Stages {
		rate := stage.Rate
		switch load.Pacing {
		case spec.PacingSine:
			if load.Sine != nil {
				rate *= 1 + load.Sine.AmplitudeRatio
			}
		case spec.PacingLinear:
			if index > 0 {
				rate = max(rate, load.Stages[index-1].Rate)
			}
		}
		peak = max(peak, rate)
	}
	return peak
}

// OfferedHits returns, for each stage in order, how many requests the stage is
// expected to offer (the integral of its rate). The offered-versus-achieved
// check uses it.
func (e *Engine) OfferedHits() []float64 {
	offered := make([]float64, len(e.stagePacers))
	for index, stagePacer := range e.stagePacers {
		offered[index] = pacer.OfferedHits(stagePacer)
	}
	return offered
}

// HTTPVersion is the HTTP version of the connection to the target, as observed
// on the responses; before any response it is the version the transport is
// configured for, HTTP/1.1.
func (e *Engine) HTTPVersion() spec.HttpVersion {
	if e.httpProtocol.Load() == 2 {
		return spec.HttpVersionA2
	}
	return spec.HttpVersionA11
}

// WorkloadInfo is the workload block of environment.json. Call it after Prepare, and after Run:
// the chars-per-token ratio is calibrated during warmup.
func (e *Engine) WorkloadInfo() spec.WorkloadInfo {
	return e.generator.Info()
}

// MaxInFlight is the hard in-flight cap in force, for environment.json.
func (e *Engine) MaxInFlight() int { return e.limits.maxInFlight }

// warnShedOnce logs the first shed request of the run. The counts are in Stats.
func (e *Engine) warnShedOnce() {
	e.shedWarning.Do(func() {
		slog.Warn("loadgen is shedding due requests: the in-flight cap and the waiting queue are full, so the client or the target cannot keep the offered rate",
			"max_in_flight", e.limits.maxInFlight, "max_waiting", e.limits.maxWaiting)
	})
}
