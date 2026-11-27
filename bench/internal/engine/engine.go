// Package engine defines what a load engine is (bench/CLAUDE.md, "Design"). An
// engine only generates load and reports raw results: it must not query
// Prometheus or write summaries, which are harness concerns.
package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// ErrTargetUnreachable is wrapped by an engine that cannot reach the target.
// The CLI maps it to exit code 3.
var ErrTargetUnreachable = errors.New("target unreachable")

// Engine drives one run's load.
type Engine interface {
	// Name and Version identify the engine in environment.json.
	Name() string
	Version() string
	// Prepare checks the spec and gets the engine ready. It sends no load.
	Prepare(ctx context.Context, runSpec spec.RunSpec) error
	// Run drives the planned phases in order, writing one row per request to
	// sink as the run progresses, and returns the exact window of every phase
	// it started, including a phase cut short by an error.
	Run(ctx context.Context, phases []Phase, sink Sink) (Result, error)
}

// Phase is one planned step of the load: warmup or a stage.
type Phase struct {
	Name       string
	Kind       spec.PhaseKind
	StageIndex *int
	OfferedRPS float64
	Duration   time.Duration
}

// Window is the measured UTC window of one phase.
type Window struct {
	Phase Phase
	Start time.Time
	End   time.Time
}

// Result is what an engine hands back to the harness.
type Result struct {
	Windows []Window
	// NativeOutputDir holds the engine's untouched output, or is empty when
	// the engine has none. Its contents go to engine/ in the bundle.
	NativeOutputDir string
}

// PlanPhases turns the load section of a spec into the ordered phases to run:
// warmup (when non-zero), then one phase per stage.
func PlanPhases(load spec.Load) ([]Phase, error) {
	var phases []Phase
	warmup, err := load.Warmup.Std()
	if err != nil {
		return nil, fmt.Errorf("load.warmup: %w", err)
	}
	if warmup > 0 {
		phases = append(phases, Phase{Name: "warmup", Kind: spec.PhaseKindWarmup, Duration: warmup})
	}
	for index, stage := range load.Stages {
		duration, err := stage.Duration.Std()
		if err != nil {
			return nil, fmt.Errorf("load.stages[%d].duration: %w", index, err)
		}
		stageIndex := index
		phases = append(phases, Phase{
			Name:       fmt.Sprintf("stage-%d", index),
			Kind:       spec.PhaseKindStage,
			StageIndex: &stageIndex,
			OfferedRPS: stage.Rate,
			Duration:   duration,
		})
	}
	return phases, nil
}

// Credentials are the resolved files and token for the hop to the target. The
// pipeline reads them from the mounted Secrets the RunSpec names (never from
// the RunSpec itself), and no engine logs or records them.
type Credentials struct {
	// BearerToken is the target.auth secret, sent as "Authorization: Bearer".
	// Empty when the RunSpec names no target.auth.
	BearerToken string
	// CertFile and KeyFile are the client certificate and key (mtls only).
	CertFile, KeyFile string
	// CAFile is the private CA (ca.crt of the mtls Secret), or empty when the
	// Secret has none. Go clients add it to the system pool.
	CAFile string
	// CABundleFile is a combined system-plus-private-CA bundle in a temp file
	// the pipeline removes at the end of the run, for a child process that has no
	// CA option (mtls only; empty without a private CA).
	CABundleFile string
}

// Inputs is what the pipeline hands an engine's Factory (bench/CLAUDE.md,
// "Run pipeline"): everything the engine needs that is not in the RunSpec.
type Inputs struct {
	RunID string
	// OutputDir is <bundle>/engine/, where an engine with native output puts it.
	OutputDir string
	// NumWorkers is the CPU count of the pod (its CPU limit, not the node's),
	// for an engine that runs worker processes.
	NumWorkers int
	// ClientSink receives the engine's self-monitoring samples.
	ClientSink ClientSink
	// ShardIndex and ShardCount say which part of a sharded run this process
	// generates (engine.shards); ShardCount is 1 for an unsharded run.
	ShardIndex, ShardCount int
	// Now is the clock of the run, shared with the phase windows.
	Now func() time.Time
	// Credentials resolves the target credentials. It is called only by an
	// engine that talks to the target, so a run with an engine that does not
	// (null) needs no mounted Secrets. It returns an error when a Secret the
	// RunSpec names cannot be read.
	Credentials func() (Credentials, error)
}

// Factory creates one engine for a run.
type Factory func(inputs Inputs) (Engine, error)

// Registry maps an engine name to its Factory. The pipeline does not import
// the engines; the CLI registers them, so adding an engine touches only the
// registration.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register adds an engine. Registering a name twice is a programming error.
func (r *Registry) Register(name string, factory Factory) {
	if _, exists := r.factories[name]; exists {
		panic("engine " + name + " registered twice")
	}
	r.factories[name] = factory
}

// New creates the engine called name, or says which engines exist.
func (r *Registry) New(name string, inputs Inputs) (Engine, error) {
	factory, found := r.factories[name]
	if !found {
		known := make([]string, 0, len(r.factories))
		for registered := range r.factories {
			known = append(known, registered)
		}
		slices.Sort(known)
		return nil, fmt.Errorf("engine %q is not available in this build (registered: %s)", name, strings.Join(known, ", "))
	}
	return factory(inputs)
}

// OfferedReporter is implemented by an engine that knows how many requests each
// stage was expected to offer (the integral of its pacer's rate). The pipeline
// writes offered_rps to phases.json from it.
type OfferedReporter interface {
	// OfferedHits returns one count per stage, in stage order. Call after Run.
	OfferedHits() []float64
}

// EnvironmentReporter is implemented by an engine that observes facts for
// environment.json while it runs. Call the methods after Run.
type EnvironmentReporter interface {
	HTTPVersion() spec.HttpVersion
	WorkloadInfo() spec.WorkloadInfo
}

// InFlightCapper is implemented by an engine with a hard cap on requests in
// flight (loadgen). The cap is recorded in environment.json, so the analysis
// can tell a target that fell behind (in-flight piled up to the cap) from a
// slow client. Call after Prepare.
type InFlightCapper interface {
	MaxInFlight() int
}

// StatsReporter is implemented by an engine that counts events worth logging
// after the run (shed requests, abandoned requests, queue blocks). The pipeline
// logs the attributes; they are not part of the bundle contract.
type StatsReporter interface {
	// LogAttrs returns slog key/value pairs. Call after Run.
	LogAttrs() []any
}
