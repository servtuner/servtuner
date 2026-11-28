package main

// The mock as a managed SGLang. Run as `python3 -m sglang.launch_server --flag=value ...` (the image links
// /usr/local/bin/python3 to this binary), it does what the deployer and the tuning loop rely on, as SGLang
// v0.5.23 does:
//   - it listens only once "booted", after SGLANG_MOCK_BOOT_SECONDS, on --host and --port (30000 by default);
//   - a flag in SGLANG_MOCK_UNKNOWN_FLAGS fails like SGLang's argument parser (usage line, exit 2);
//   - args matching every flag of a set in SGLANG_MOCK_INFEASIBLE fail like an empty KV pool ("Not enough
//     memory. Please try to increase --mem-fraction-static.", then "Initialization failed.", exit 1);
//   - --ssl-certfile and --ssl-keyfile serve TLS with no client certificate;
//   - SGLang's endpoints and metrics (--emulate sglang), with a KV pool (max_total_num_tokens) that grows
//     with --mem-fraction-static and --tp-size;
//   - --max-running-requests caps requests in service (the rest queue), so the scheduler knob changes
//     throughput.
// Every other flag is accepted and ignored: the mock emulates the interface, not the engine.

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	sglangPython = "python3"
	// SGLang's own defaults where the mock needs one.
	sglangDefaultPort = "30000"
	// The KV pool at the default memory fraction on one GPU; it scales with both.
	sglangMockBaseTokens = sglangMockMaxTotalTokens
	sglangMockBootSec    = 3
	sglangArgparseExit   = 2
	sglangFailureExit    = 1
	// A scheduler that fails at startup makes SGLang kill its own process tree (SIGKILL: 128+9),
	// as SGLang 0.5.23 did live when the KV pool would hold nothing.
	sglangSelfKilledExit = 137
)

// sglangServeArgs are SGLang's flags: --name -> value; a bare switch has "true".
type sglangServeArgs map[string]string

// parseSGLangArgs reads `-m sglang.launch_server --flag=value | --flag value | --switch ...`.
func parseSGLangArgs(args []string) (sglangServeArgs, error) {
	if len(args) < 2 || args[0] != "-m" || args[1] != sglangModule {
		return nil, fmt.Errorf("the mock emulates only `python3 -m %s`", sglangModule)
	}
	flags := sglangServeArgs{}
	rest := args[2:]
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if !strings.HasPrefix(arg, "--") {
			return nil, fmt.Errorf("unrecognized arguments: %s", arg)
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if !hasValue {
			value = "true"
			if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "--") {
				value = rest[i+1]
				i++
			}
		}
		flags[name] = value
	}
	if flags["--model-path"] == "" && flags["--model"] == "" {
		return nil, fmt.Errorf("the following arguments are required: --model-path")
	}
	return flags, nil
}

// sglangModule is SGLang's documented server entry point.
const sglangModule = "sglang.launch_server"

func (a sglangServeArgs) number(name string, fallback float64) float64 {
	if value, err := strconv.ParseFloat(a[name], 64); err == nil {
		return value
	}
	return fallback
}

// matchesAll is true when the args hold every --flag=value of the set.
func (a sglangServeArgs) matchesAll(set string) bool {
	for _, condition := range strings.Split(set, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(condition), "=")
		if got, ok := a[name]; !ok || got != value {
			return false
		}
	}
	return true
}

// runSGLang is the mock's SGLang server: it returns the process exit code.
func runSGLang(args []string, environment func(string) string, stdout, stderr io.Writer, sleep func(time.Duration)) int {
	parsed, err := parseSGLangArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: launch_server.py [-h] --model-path MODEL_PATH [options]")
		fmt.Fprintln(stderr, "launch_server.py: error:", err)
		return sglangArgparseExit
	}
	for _, flag := range strings.Split(environment("SGLANG_MOCK_UNKNOWN_FLAGS"), ",") {
		if flag = strings.TrimSpace(flag); flag != "" {
			if _, ok := parsed[flag]; ok {
				// SGLang 0.5.23's parser calls itself `sglang serve` under either entry point.
				fmt.Fprintln(stderr, "usage: sglang serve [-h] --model-path MODEL_PATH [options]")
				fmt.Fprintf(stderr, "sglang serve: error: unrecognized arguments: %s\n", flag)
				return sglangArgparseExit
			}
		}
	}
	bootSeconds := sglangMockBootSec
	if value, err := strconv.Atoi(environment("SGLANG_MOCK_BOOT_SECONDS")); err == nil && value >= 0 {
		bootSeconds = value
	}
	fmt.Fprintf(stdout, "INFO: sglang (servtuner mock) serving %s; loading for %d s\n", parsed.model(), bootSeconds)
	sleep(time.Duration(bootSeconds) * time.Second)
	for _, set := range strings.Split(environment("SGLANG_MOCK_INFEASIBLE"), ";") {
		if set = strings.TrimSpace(set); set != "" && parsed.matchesAll(set) {
			// SGLang 0.5.23's words, recorded live (bench/internal/target/testdata/sglang/).
			fraction := parsed["--mem-fraction-static"]
			if fraction == "" {
				fraction = "0.82"
			}
			fmt.Fprintf(stderr, "ValueError: Loaded weights leave no GPU memory for the KV cache under --mem-fraction-static=%s."+
				" Raise --mem-fraction-static above 0.147 (minimum viable = 1 - available/pre = 0.1460).\n", fraction)
			fmt.Fprintln(stderr, "Received sigquit from a child process. It usually means the child failed.")
			return sglangSelfKilledExit
		}
	}
	cfg, err := sglangServeConfig(parsed, environment)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		fmt.Fprintln(stderr, "openai-mock as sglang:", err)
		return sglangFailureExit
	}
	fmt.Fprintf(stdout, "max_total_num_tokens=%d, max_running_requests=%d\n", cfg.SGLang.MaxTotalNumTokens, cfg.SGLang.MaxRunningRequests)
	if err := runUntilSignal(cfg); err != nil {
		fmt.Fprintln(stderr, "openai-mock as sglang:", err)
		return sglangFailureExit
	}
	return 0
}

func (a sglangServeArgs) model() string {
	if path := a["--model-path"]; path != "" {
		return path
	}
	return a["--model"]
}

// sglangServeConfig turns SGLang's flags into the mock's config. Latency and other mock knobs still come
// from the OPENAI_MOCK_* environment.
func sglangServeConfig(parsed sglangServeArgs, environment func(string) string) (Config, error) {
	cfg, err := loadConfig(nil, func(name string) string {
		switch name {
		case envPrefix + "TLS":
			return tlsOff // TLS comes from the SGLang flags below
		case envPrefix + "CLIENT_AUTH", envPrefix + "EMULATE":
			return ""
		}
		return environment(name)
	}, io.Discard)
	if err != nil {
		return Config{}, err
	}
	port := parsed["--port"]
	if port == "" {
		port = sglangDefaultPort
	}
	cfg.ListenAddress = parsed["--host"] + ":" + port
	cfg.ModelName = parsed.model()
	if name := parsed["--served-model-name"]; name != "" {
		cfg.ModelName = name
	}
	cert, key := parsed["--ssl-certfile"], parsed["--ssl-keyfile"]
	if cert != "" || key != "" {
		cfg.TLS, cfg.ClientAuth, cfg.CertFile, cfg.KeyFile = tlsOn, clientAuthNone, cert, key
	}
	tensorParallel := parsed.number("--tp-size", parsed.number("--tensor-parallel-size", 1))
	fraction := parsed.number("--mem-fraction-static", sglangDefaultMemFraction)
	running := int(parsed.number("--max-running-requests", sglangMockMaxRunningRequests))
	cfg.Faults.MaxConcurrency = running
	cfg.Faults.OverCapacity = overCapacityQueue
	emulation := defaultSGLangEmulation()
	emulation.MemFractionStatic = fraction
	emulation.MaxRunningRequests = running
	emulation.MaxTotalNumTokens = int(math.Round(sglangMockBaseTokens * tensorParallel * fraction / sglangDefaultMemFraction))
	cfg.SGLang = emulation
	return cfg, nil
}
