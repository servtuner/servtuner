package inferenceperf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

// Name is what environment.json records as the engine.
const Name = "inference-perf"

// ExecutableEnvVar names the environment variable that overrides the CLI path.
const ExecutableEnvVar = "SERVTUNER_INFERENCE_PERF_BIN"

// DefaultExecutable is where the bench image installs the pinned CLI, in its
// own virtualenv (bench/Dockerfile).
const DefaultExecutable = "/opt/inference-perf/bin/inference-perf"

// Names of the files the engine writes next to inference-perf's own output.
const (
	// RenderedConfigFile is the config bench rendered and passed with -c.
	RenderedConfigFile = "servtuner-input-config.yaml"
	// StdoutFile and StderrFile hold the child's output untouched.
	StdoutFile = "stdout.txt"
	StderrFile = "stderr.txt"
)

// interruptGrace is how long the CLI gets to write its reports after the run's
// context ends. inference-perf treats SIGINT as "stop and report".
const interruptGrace = 60 * time.Second

// checksumEnvironment stops the AWS SDK default checksums from breaking
// S3-compatible stores. inference-perf bundles boto3.
var checksumEnvironment = []string{
	"AWS_REQUEST_CHECKSUM_CALCULATION=when_required",
	"AWS_RESPONSE_CHECKSUM_VALIDATION=when_required",
}

// Options configure an Engine.
type Options struct {
	// Executable is the inference-perf CLI. Empty means $SERVTUNER_INFERENCE_PERF_BIN,
	// then DefaultExecutable.
	Executable string
	// OutputDir receives the native output: the child's stdout and stderr and
	// its report files. Required.
	OutputDir string
	// CertFile and KeyFile are the client certificate and key (mtls only).
	CertFile, KeyFile string
	// CABundleFile is the combined system-plus-private-CA bundle, exported as
	// SSL_CERT_FILE to the child only (mtls only; see the package doc).
	CABundleFile string
	// NumWorkers is the worker process count; 0 keeps inference-perf's default,
	// the CPU count.
	NumWorkers int
	// Environment is the base environment of the child. Nil means os.Environ().
	Environment []string
}

// Engine implements engine.Engine for inference-perf.
type Engine struct {
	options Options
	// Set by Prepare.
	runSpec spec.RunSpec
	phases  []engine.Phase
	config  []byte
}

var _ engine.Engine = (*Engine)(nil)

// New returns an engine. It does not register itself (the pipeline does).
func New(options Options) *Engine {
	return &Engine{options: options}
}

func (*Engine) Name() string    { return Name }
func (*Engine) Version() string { return Version }

// Prepare rejects anything inference-perf cannot do, then renders the config.
// It sends no load.
func (e *Engine) Prepare(_ context.Context, runSpec spec.RunSpec) error {
	if e.options.OutputDir == "" {
		return errors.New("inference-perf engine: Options.OutputDir is required")
	}
	if err := e.checkTransport(runSpec); err != nil {
		return err
	}
	if err := checkSpec(runSpec); err != nil {
		return err
	}
	phases, err := engine.PlanPhases(runSpec.Load)
	if err != nil {
		return err
	}
	config, err := renderConfig(runSpec, configInputs{
		Phases:     phases,
		OutputDir:  e.options.OutputDir,
		CertFile:   e.options.CertFile,
		KeyFile:    e.options.KeyFile,
		NumWorkers: e.options.NumWorkers,
	})
	if err != nil {
		return err
	}
	e.runSpec, e.phases, e.config = runSpec, phases, config
	return nil
}

// checkTransport applies the transport rules: the mode is the spec's, never
// inferred, and the files given must be exactly the ones that mode allows.
func (e *Engine) checkTransport(runSpec spec.RunSpec) error {
	mode, err := spec.ModeOf(runSpec.Target.Tls)
	if err != nil {
		return fmt.Errorf("target.tls: %w", err)
	}
	return tlsconfig.Hop{
		Name:     "target",
		Mode:     mode,
		URL:      runSpec.Target.Endpoint,
		CertFile: e.options.CertFile,
		KeyFile:  e.options.KeyFile,
		CAFile:   e.options.CABundleFile,
	}.Validate()
}

// checkSpec lists every RunSpec feature the engine cannot honour.
func checkSpec(runSpec spec.RunSpec) error {
	var problems []error
	reject := func(format string, args ...any) { problems = append(problems, fmt.Errorf(format, args...)) }

	if runSpec.Engine.Type != spec.EngineTypeInferencePerf {
		reject("engine.type is %q, not %q", runSpec.Engine.Type, spec.EngineTypeInferencePerf)
	}
	if version := runSpec.Engine.Version; version != "" && version != versionPinned && version != Version {
		reject("engine.version %q: only the pinned %s is installed", version, Version)
	}
	problems = append(problems, checkEngineOptions(runSpec.Engine.Options)...)
	if runSpec.Target.Auth != nil {
		reject("target.auth: inference-perf takes its API key only in its config file or argv, which would write a secret into the bundle")
	}
	if api := runSpec.Workload.Api; api != "" && api != spec.WorkloadApiCompletion {
		reject("workload.api %q: inference-perf's random data generator supports only completion; use engine.type: loadgen for chat", api)
	}
	switch runSpec.Load.Pacing {
	case "", spec.PacingConstant, spec.PacingPoisson:
	default:
		reject("load.pacing %q: inference-perf supports only constant and poisson", runSpec.Load.Pacing)
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("inference-perf cannot run this spec: %w", err)
	}
	return nil
}

// harnessOwnedSections are the top-level config sections the harness renders
// from the RunSpec or owns outright. engine.options may not touch them: the
// workload must come from the RunSpec (or the workload hash lies), and the
// harness owns where output goes.
var harnessOwnedSections = []string{"server", "load", "api", "data", "report", "storage", "metrics"}

// secretLookingNames are fragments of a key name that mark a path or a
// secret; such a key is refused at any depth, because the rendered config is
// kept in the bundle and the harness controls every path.
var secretLookingNames = []string{"path", "secret", "token", "password", "key", "cert", "credential", "auth"}

// checkEngineOptions rejects the options the harness owns and names that look
// like secrets or paths. "tokenizer" is the one special key (a string).
func checkEngineOptions(options spec.EngineOptions) []error {
	var problems []error
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field := "engine.options." + key
		switch {
		case key == engineOptionTokenizer:
			if _, isString := options[key].(string); !isString {
				problems = append(problems, fmt.Errorf("%s must be a string (a model name or a local path)", field))
			}
		case slices.Contains(harnessOwnedSections, key):
			problems = append(problems, fmt.Errorf("%s: %s", field, ownedSectionReason(key)))
		default:
			problems = append(problems, secretLookingKeys(field, options[key])...)
			if looksSecret(key) {
				problems = append(problems, fmt.Errorf("%s: a path or secret-looking name is refused", field))
			}
		}
	}
	return problems
}

// secretLookingKeys walks a nested option value for keys that look like paths
// or secrets.
func secretLookingKeys(field string, value any) []error {
	var problems []error
	switch typed := value.(type) {
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			nested := field + "." + name
			if looksSecret(name) {
				problems = append(problems, fmt.Errorf("%s: a path or secret-looking name is refused", nested))
			}
			problems = append(problems, secretLookingKeys(nested, typed[name])...)
		}
	case []any:
		for index, item := range typed {
			problems = append(problems, secretLookingKeys(fmt.Sprintf("%s[%d]", field, index), item)...)
		}
	}
	return problems
}

func ownedSectionReason(section string) string {
	if section == "metrics" {
		return "engines must not query Prometheus; the harness collects server metrics itself"
	}
	return fmt.Sprintf("the harness owns the %q section; set it through the RunSpec", section)
}

func looksSecret(name string) bool {
	lower := strings.ToLower(name)
	return slices.ContainsFunc(secretLookingNames, func(fragment string) bool { return strings.Contains(lower, fragment) })
}

// Run executes the CLI, then normalizes its per-request report into sink. The
// native output stays untouched in OutputDir. Rows and windows are produced
// even when the CLI fails, so a run cut short still keeps what it measured.
func (e *Engine) Run(ctx context.Context, phases []engine.Phase, sink engine.Sink) (engine.Result, error) {
	if e.config == nil {
		return engine.Result{}, errors.New("inference-perf engine: Run before Prepare")
	}
	if !slices.EqualFunc(phases, e.phases, samePhase) {
		return engine.Result{}, errors.New("inference-perf engine: the phases to run differ from the ones Prepare rendered")
	}
	runErr := e.execute(ctx)
	result := engine.Result{NativeOutputDir: e.options.OutputDir}
	windows, rows, normalizeErr := normalizeDirectory(e.options.OutputDir, e.phases)
	result.Windows = windows
	for _, row := range rows {
		if err := sink.Write(row); err != nil {
			return result, errors.Join(runErr, fmt.Errorf("write request row: %w", err))
		}
	}
	return result, errors.Join(runErr, normalizeErr)
}

func samePhase(a, b engine.Phase) bool {
	sameStage := (a.StageIndex == nil) == (b.StageIndex == nil) && (a.StageIndex == nil || *a.StageIndex == *b.StageIndex)
	return a.Name == b.Name && a.Kind == b.Kind && a.OfferedRPS == b.OfferedRPS && a.Duration == b.Duration && sameStage
}

// execute writes the rendered config and runs the CLI to completion.
func (e *Engine) execute(ctx context.Context) error {
	if err := os.MkdirAll(e.options.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create inference-perf output dir: %w", err)
	}
	configPath := filepath.Join(e.options.OutputDir, RenderedConfigFile)
	if err := os.WriteFile(configPath, e.config, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	stdout, err := os.Create(filepath.Join(e.options.OutputDir, StdoutFile))
	if err != nil {
		return fmt.Errorf("create %s: %w", StdoutFile, err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(e.options.OutputDir, StderrFile))
	if err != nil {
		return fmt.Errorf("create %s: %w", StderrFile, err)
	}
	defer stderr.Close()

	executable := e.executable()
	command := exec.CommandContext(ctx, executable, "-c", configPath)
	command.Stdout, command.Stderr = stdout, stderr
	command.Env = e.childEnvironment()
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGINT) }
	command.WaitDelay = interruptGrace
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w (see %s in the output dir)", executable, err, StderrFile)
	}
	return nil
}

func (e *Engine) executable() string {
	if e.options.Executable != "" {
		return e.options.Executable
	}
	if fromEnvironment := os.Getenv(ExecutableEnvVar); fromEnvironment != "" {
		return fromEnvironment
	}
	return DefaultExecutable
}

// childEnvironment is the base environment plus the checksum variables and,
// in mtls mode, SSL_CERT_FILE. It never touches bench's own environment.
func (e *Engine) childEnvironment() []string {
	base := e.options.Environment
	if base == nil {
		base = os.Environ()
	}
	environment := append(slices.Clone(base), checksumEnvironment...)
	environment = append(environment, "PYTHONUNBUFFERED=1")
	if e.options.CABundleFile != "" {
		environment = append(environment, "SSL_CERT_FILE="+e.options.CABundleFile)
	}
	return environment
}
