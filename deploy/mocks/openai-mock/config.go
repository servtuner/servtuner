package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// TLS is "true" or "false", never inferred. With TLS, client-auth is the
// client-certificate policy: required (refuse a client without a valid one),
// optional (verify one that is sent, accept none) or none (ask for none, as
// vLLM does with --ssl-certfile and --ssl-keyfile).
const (
	tlsOn  = "true"
	tlsOff = "false"

	clientAuthRequired = "required"
	clientAuthOptional = "optional"
	clientAuthNone     = "none"
)

// envPrefix is prepended to the upper-snake form of every flag name, e.g.
// --client-auth is also settable as OPENAI_MOCK_CLIENT_AUTH. A flag wins over its
// environment variable.
const envPrefix = "OPENAI_MOCK_"

// Config is the single place where mock behaviour is configured. The fault
// knobs (Faults below) are consumed by faults.go.
type Config struct {
	ListenAddress string
	// MetricsListenAddress, when set, serves /metrics there in plain HTTP, with or without TLS on the main
	// listener, and removes it from the main listener. Empty keeps /metrics there. The counters are not
	// sensitive, and Prometheus cannot present a client certificate per target, so this lets it scrape a mock
	// that requires client certificates.
	MetricsListenAddress string
	ModelName            string

	TLS string
	// ClientAuth is empty until loadConfig resolves it: required by default with TLS.
	ClientAuth   string
	CertFile     string
	KeyFile      string
	ClientCAFile string

	TimeToFirstToken       time.Duration
	InterTokenLatency      time.Duration
	OutputTokens           int
	HonorRequestMaxTokens  bool
	SendRoleOnlyFirstChunk bool

	Faults FaultConfig

	// VLLM, when set, makes the mock answer as vLLM does (vllm.go): /health and
	// vLLM's metric names, including cache_config_info.
	VLLM *VLLMEmulation
}

// Over-capacity behaviours when MaxConcurrency requests are already being served.
const (
	overCapacityQueue  = "queue"
	overCapacityReject = "reject"
)

// FaultConfig makes the mock misbehave the way real targets do, so each
// advisor rule can be triggered on kind. The zero value
// is a well-behaved mock.
type FaultConfig struct {
	// ExtraLatency is added before the first token of every request.
	ExtraLatency time.Duration
	// LatencyPerInFlight is added before the first token for every other
	// request being served at admission: latency rising with load, like a
	// saturating upstream.
	LatencyPerInFlight time.Duration
	// MaxConcurrency caps requests served at once; 0 means no cap. Beyond it
	// OverCapacity either queues requests (latency grows) or rejects them
	// with 429.
	MaxConcurrency int
	OverCapacity   string
	// ErrorRate is the share of requests answered with ErrorStatus instead,
	// spread evenly (every 1/rate-th request), so a run is reproducible.
	ErrorRate   float64
	ErrorStatus int
	// MemoryPerRequestBytes is allocated and touched for as long as each
	// request is served, so the working set follows in-flight requests.
	MemoryPerRequestBytes int
	// MemoryRampBytesPerSecond grows a retained allocation every second up to
	// MemoryRampMaxBytes: a leak, for the memory-slope checks.
	MemoryRampBytesPerSecond int
	MemoryRampMaxBytes       int
}

// defaultConfig is what the mock does when nothing is configured.
func defaultConfig() Config {
	return Config{
		ListenAddress:         ":8000",
		ModelName:             "mock-model",
		TLS:                   tlsOn,
		TimeToFirstToken:      200 * time.Millisecond,
		InterTokenLatency:     20 * time.Millisecond,
		OutputTokens:          32,
		HonorRequestMaxTokens: true,
		Faults:                FaultConfig{OverCapacity: overCapacityQueue, ErrorStatus: http.StatusInternalServerError},
	}
}

// Validate rejects contradictory settings so a misconfigured mock fails at
// startup instead of silently serving the wrong transport.
func (c Config) Validate() error {
	switch c.TLS {
	case tlsOff:
		if c.CertFile != "" || c.KeyFile != "" || c.ClientCAFile != "" || c.ClientAuth != "" {
			return fmt.Errorf("--tls false serves plain HTTP and must not be combined with --client-auth or certificate files")
		}
	case tlsOn:
		if c.CertFile == "" || c.KeyFile == "" {
			return fmt.Errorf("--tls true needs --tls-cert-file and --tls-key-file")
		}
		switch c.ClientAuth {
		case clientAuthRequired, clientAuthOptional:
			if c.ClientCAFile == "" {
				return fmt.Errorf("--client-auth %s needs --tls-client-ca-file", c.ClientAuth)
			}
		case clientAuthNone:
			if c.ClientCAFile != "" {
				return fmt.Errorf("--client-auth none verifies no client certificates and takes no --tls-client-ca-file")
			}
		default:
			return fmt.Errorf("--client-auth %q is not one of %q, %q, %q", c.ClientAuth, clientAuthRequired, clientAuthOptional, clientAuthNone)
		}
	case "mtls", "tls", "platform":
		return fmt.Errorf("--tls %q: servtuner 0.2 replaced transport modes with --tls true|false and --client-auth", c.TLS)
	default:
		return fmt.Errorf("--tls %q is not true or false", c.TLS)
	}
	if c.ListenAddress == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	if c.MetricsListenAddress != "" && c.MetricsListenAddress == c.ListenAddress {
		return fmt.Errorf("metrics-listen %q must differ from listen", c.MetricsListenAddress)
	}
	if c.ModelName == "" {
		return fmt.Errorf("model name must not be empty")
	}
	if c.TimeToFirstToken < 0 || c.InterTokenLatency < 0 {
		return fmt.Errorf("ttft (%s) and inter-token latency (%s) must not be negative", c.TimeToFirstToken, c.InterTokenLatency)
	}
	if c.OutputTokens < 1 {
		return fmt.Errorf("output-tokens %d must be at least 1", c.OutputTokens)
	}
	return c.Faults.validate()
}

func (f FaultConfig) validate() error {
	if f.ExtraLatency < 0 || f.LatencyPerInFlight < 0 {
		return fmt.Errorf("extra-latency (%s) and latency-per-in-flight (%s) must not be negative", f.ExtraLatency, f.LatencyPerInFlight)
	}
	if f.MaxConcurrency < 0 {
		return fmt.Errorf("max-concurrency %d must not be negative (0 means no cap)", f.MaxConcurrency)
	}
	if f.OverCapacity != overCapacityQueue && f.OverCapacity != overCapacityReject {
		return fmt.Errorf("over-capacity %q is not one of %q, %q", f.OverCapacity, overCapacityQueue, overCapacityReject)
	}
	if f.ErrorRate < 0 || f.ErrorRate > 1 {
		return fmt.Errorf("error-rate %v must be between 0 and 1", f.ErrorRate)
	}
	if f.ErrorStatus < 400 || f.ErrorStatus > 599 {
		return fmt.Errorf("error-status %d must be a 4xx or 5xx code", f.ErrorStatus)
	}
	if f.MemoryPerRequestBytes < 0 || f.MemoryRampBytesPerSecond < 0 || f.MemoryRampMaxBytes < 0 {
		return fmt.Errorf("memory knobs must not be negative")
	}
	if f.MemoryRampBytesPerSecond > 0 && f.MemoryRampMaxBytes == 0 {
		return fmt.Errorf("memory-ramp-bytes-per-second needs memory-ramp-max-bytes, so the ramp stops")
	}
	return nil
}

// loadConfig parses flags over environment defaults over built-in defaults,
// then validates the result.
func loadConfig(args []string, environment func(string) string, usageOutput io.Writer) (Config, error) {
	cfg := defaultConfig()
	env := envReader{lookup: environment}

	flags := flag.NewFlagSet("openai-mock", flag.ContinueOnError)
	flags.SetOutput(usageOutput)
	flags.StringVar(&cfg.ListenAddress, "listen", env.String("LISTEN", cfg.ListenAddress), "listen address")
	flags.StringVar(&cfg.MetricsListenAddress, "metrics-listen", env.String("METRICS_LISTEN", cfg.MetricsListenAddress), "plain-HTTP address serving only /metrics, with or without --tls (the counters are not sensitive); empty serves /metrics on --listen")
	flags.StringVar(&cfg.ModelName, "model", env.String("MODEL", cfg.ModelName), "model name served and reported")
	flags.StringVar(&cfg.TLS, "tls", env.String("TLS", cfg.TLS), "serve TLS: true or false (plain HTTP)")
	flags.StringVar(&cfg.ClientAuth, "client-auth", env.String("CLIENT_AUTH", cfg.ClientAuth), "client certificates with --tls true: required (the default), optional or none")
	flags.StringVar(&cfg.CertFile, "tls-cert-file", env.String("TLS_CERT_FILE", cfg.CertFile), "server certificate (--tls true)")
	flags.StringVar(&cfg.KeyFile, "tls-key-file", env.String("TLS_KEY_FILE", cfg.KeyFile), "server private key (--tls true)")
	flags.StringVar(&cfg.ClientCAFile, "tls-client-ca-file", env.String("TLS_CLIENT_CA_FILE", cfg.ClientCAFile), "CA that signs accepted client certificates (--client-auth required or optional)")
	flags.DurationVar(&cfg.TimeToFirstToken, "ttft", env.Duration("TTFT", cfg.TimeToFirstToken), "delay before the first content token")
	flags.DurationVar(&cfg.InterTokenLatency, "itl", env.Duration("ITL", cfg.InterTokenLatency), "delay between content tokens")
	flags.IntVar(&cfg.OutputTokens, "output-tokens", env.Int("OUTPUT_TOKENS", cfg.OutputTokens), "completion tokens when the request gives no max_tokens")
	flags.BoolVar(&cfg.HonorRequestMaxTokens, "honor-max-tokens", env.Bool("HONOR_MAX_TOKENS", cfg.HonorRequestMaxTokens), "use the request's max_tokens as the completion length when present")
	faults := &cfg.Faults
	flags.DurationVar(&faults.ExtraLatency, "extra-latency", env.Duration("EXTRA_LATENCY", faults.ExtraLatency), "fault: delay added before the first token of every request")
	flags.DurationVar(&faults.LatencyPerInFlight, "latency-per-in-flight", env.Duration("LATENCY_PER_IN_FLIGHT", faults.LatencyPerInFlight), "fault: delay added before the first token per other request being served (latency rises with load)")
	flags.IntVar(&faults.MaxConcurrency, "max-concurrency", env.Int("MAX_CONCURRENCY", faults.MaxConcurrency), "fault: requests served at once; 0 is no cap")
	flags.StringVar(&faults.OverCapacity, "over-capacity", env.String("OVER_CAPACITY", faults.OverCapacity), "fault: beyond max-concurrency, queue (wait for a slot) or reject (429)")
	flags.Float64Var(&faults.ErrorRate, "error-rate", env.Float("ERROR_RATE", faults.ErrorRate), "fault: share of requests answered with error-status, spread evenly")
	flags.IntVar(&faults.ErrorStatus, "error-status", env.Int("ERROR_STATUS", faults.ErrorStatus), "fault: HTTP status of injected errors")
	flags.IntVar(&faults.MemoryPerRequestBytes, "memory-per-request-bytes", env.Int("MEMORY_PER_REQUEST_BYTES", faults.MemoryPerRequestBytes), "fault: bytes held while each request is served")
	flags.IntVar(&faults.MemoryRampBytesPerSecond, "memory-ramp-bytes-per-second", env.Int("MEMORY_RAMP_BYTES_PER_SECOND", faults.MemoryRampBytesPerSecond), "fault: retained bytes added every second (a leak)")
	flags.IntVar(&faults.MemoryRampMaxBytes, "memory-ramp-max-bytes", env.Int("MEMORY_RAMP_MAX_BYTES", faults.MemoryRampMaxBytes), "fault: where the memory ramp stops")
	flags.BoolVar(&cfg.SendRoleOnlyFirstChunk, "role-only-first-chunk", env.Bool("ROLE_ONLY_FIRST_CHUNK", cfg.SendRoleOnlyFirstChunk), "send headers and a role-only chunk immediately, so TTFB differs from TTFT")

	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.TLS == tlsOn && cfg.ClientAuth == "" {
		cfg.ClientAuth = clientAuthRequired
	}
	if err := env.Err(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// envReader reads prefixed environment variables and remembers the first
// unparsable value, so loadConfig reports it instead of ignoring it.
type envReader struct {
	lookup func(string) string
	err    error
}

func (e *envReader) raw(name string) (string, string, bool) {
	key := envPrefix + name
	value := strings.TrimSpace(e.lookup(key))
	return key, value, value != ""
}

func (e *envReader) fail(key, value string, cause error) {
	if e.err == nil {
		e.err = fmt.Errorf("environment variable %s=%q: %w", key, value, cause)
	}
}

func (e *envReader) String(name, fallback string) string {
	if _, value, ok := e.raw(name); ok {
		return value
	}
	return fallback
}

func (e *envReader) Duration(name string, fallback time.Duration) time.Duration {
	key, value, ok := e.raw(name)
	if !ok {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		e.fail(key, value, err)
		return fallback
	}
	return parsed
}

func (e *envReader) Int(name string, fallback int) int {
	key, value, ok := e.raw(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		e.fail(key, value, err)
		return fallback
	}
	return parsed
}

func (e *envReader) Float(name string, fallback float64) float64 {
	key, value, ok := e.raw(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		e.fail(key, value, err)
		return fallback
	}
	return parsed
}

func (e *envReader) Bool(name string, fallback bool) bool {
	key, value, ok := e.raw(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		e.fail(key, value, err)
		return fallback
	}
	return parsed
}

func osEnvironment(key string) string { return os.Getenv(key) }

// Err returns the first environment variable that failed to parse.
func (e *envReader) Err() error { return e.err }
