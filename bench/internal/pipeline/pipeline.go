// Package pipeline runs the steps of a servtuner run (bench/CLAUDE.md, "Run
// pipeline"). It holds no engine- or target-specific code: the engine arrives
// as an engine.Engine from a factory, the target through target adapters, and
// the bundle goes out through a storage.BundleStore.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/events"
	"github.com/servtuner/servtuner/bench/internal/pricing"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/target"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

// Options is everything one run needs from its caller.
type Options struct {
	RunID string
	// SpecDocument is the RunSpec exactly as submitted (YAML or JSON); it is
	// stored byte for byte as spec.yaml.
	SpecDocument []byte
	// Store receives the bundle; keys are file names relative to the bundle.
	Store storage.BundleStore
	// BundleDir is the directory Store writes into. It is required, because
	// request rows and client samples are streamed to files there, the engine
	// writes its native output under engine/, and analysis reads the files.
	BundleDir string
	// NewEngine creates the engine for a parsed spec from the inputs the
	// pipeline resolved.
	NewEngine func(spec.RunSpec, engine.Inputs) (engine.Engine, error)
	// Reporter receives one status event per step. Nil discards them.
	Reporter events.Reporter
	// APITransport is how the bench to api hop is secured, recorded in
	// environment.json; nil when no api URL was given.
	APITransport *spec.HopTransport
	// SecretsDir is where the Secrets named by the RunSpec are mounted
	// (secrets.go). Empty means DefaultSecretsDir.
	SecretsDir string
	// KubeClient returns the read-only Kubernetes client and, when it is nil,
	// the reason the snapshot is skipped (target.NewKubeClient). Nil means no
	// cluster.
	KubeClient func() (kubernetes.Interface, string, error)
	// Getenv reads the environment (POD_NAME, POD_NAMESPACE, SSL_CERT_FILE).
	// Nil means os.Getenv.
	Getenv func(string) string
	// NumWorkers overrides the worker count handed to engines. Zero means the
	// pod's CPU limit: Go sizes GOMAXPROCS from the cgroup limit, not from the
	// node's CPU count.
	NumWorkers int
	// PrometheusOptions tunes the Prometheus client (retries, timeouts).
	PrometheusOptions prometheus.Options
	// StreamOptions tunes how request rows and client samples are flushed.
	StreamOptions bundle.StreamOptions
	// Sleep waits for d or until ctx ends. Nil uses a real timer; tests pass a
	// fake so the settle delay costs nothing.
	Sleep func(ctx context.Context, d time.Duration) error
	// SystemCABundle overrides where the system trust bundle is read from when
	// building the combined bundle for a child process (tests).
	SystemCABundle string
	// PriceList builds the AWS Price List client, only for a RunSpec whose
	// infra_pricing source is aws. Nil means aws prices cannot be resolved,
	// which the run records as a pricing problem.
	PriceList func(ctx context.Context) (pricing.ProductsAPI, error)
	// DatasheetClient fetches the token pricing datasheet. Nil means a client
	// on Go's default transport, which honours the proxy variables like
	// bench's other non-target hops.
	DatasheetClient *http.Client
	// PriceBook is where token_pricing.from_price_book fetches the datasheet:
	// the api's GET /runs/{id}/datasheet, with the run token. Nil without --api-url, which
	// leaves such a run's token cost unpriced with the reason logged.
	PriceBook *PriceBookSource
	// Offline is servtuner run --offline: the public datasheet is
	// never fetched and AWS Price List prices are refused.
	Offline bool
	// ShardIndex is this process's shard of a sharded run (engine.shards):
	// 0, the default, leads; ExecuteShard runs the others.
	ShardIndex int
	// ShardStore is where the shards of a sharded run meet: the final bundle
	// store (the bucket for an s3:// --out). Nil means Store.
	ShardStore storage.BundleStore
	// ShardReadyTimeout and ShardDoneTimeout bound the waits between shards.
	// Zero means DefaultShardReadyTimeout and DefaultShardDoneTimeout.
	ShardReadyTimeout, ShardDoneTimeout time.Duration
	// Now supplies timestamps, so tests can fix them. Nil means time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// PriceBookSource is the api's datasheet endpoint and a client that carries
// the run token on the api hop's transport.
type PriceBookSource struct {
	URL    string
	Client *http.Client
}

// Result is the outcome of a run. The bundle holds the details.
type Result struct {
	RunID  string
	Status spec.RunStatus
	// Failure is the first failing step, nil on success.
	Failure *spec.Failure
	// Err is nil on success. Otherwise it wraps the cause, so the caller can
	// choose an exit code with errors.Is.
	Err error
}

// step is one named pipeline step. state is what the step reports to the api
// while it runs.
type step struct {
	name  spec.PipelineStep
	state spec.ReportedState
	run   func(r *run, ctx context.Context) (skipReason string, err error)
}

// pipelineSteps is the pipeline in order. finalize is last and always runs.
// Analysis runs at the end of summarize: the status event schema has no
// analyze step, and both read only the stored raw files.
var pipelineSteps = []step{
	{spec.PipelineStepResolve, spec.ReportedStateRunning, (*run).resolve},
	{spec.PipelineStepSnapshot, spec.ReportedStateRunning, (*run).snapshot},
	{spec.PipelineStepLoad, spec.ReportedStateRunning, (*run).load},
	{spec.PipelineStepCollect, spec.ReportedStateCollecting, (*run).collect},
	{spec.PipelineStepNormalize, spec.ReportedStateRunning, (*run).normalize},
	{spec.PipelineStepSummarize, spec.ReportedStateRunning, (*run).summarize},
	{spec.PipelineStepFinalize, spec.ReportedStateRunning, (*run).finalize},
}

// run is the state shared by the steps of one execution.
type run struct {
	opts      Options
	writer    *bundle.Writer
	logger    *slog.Logger
	startedAt time.Time
	secrets   secrets
	tempDir   string // removed when the run ends; holds the combined CA bundle

	runSpec spec.RunSpec
	hasSpec bool
	engine  engine.Engine
	// report is the validation outcome, kept for phases.json.
	report validate.Report

	// Prometheus. client is nil when the RunSpec names none or the hop could
	// not be built; prometheusProblem then says why, for the logs and for
	// queries.json.
	client              *prometheus.Client
	prometheusTransport *spec.HopTransport
	prometheusProblem   string
	resolution          prometheus.Resolution
	targets             *spec.PrometheusTargets
	optionalCatalogs    []spec.OptionalCatalogResult

	// Kubernetes snapshot.
	kube         kubernetes.Interface
	kubeSkip     string
	preSnapshot  *spec.TargetSnapshot
	postSnapshot *spec.TargetSnapshot
	knobs        target.Knobs

	requests bundle.RequestStream
	samples  bundle.ClientStream
	// streams is true once resolve created the two streams; streamsDone once
	// they are closed and recorded.
	streams     bool
	streamsDone bool

	windows []engine.Window
	// shardOfferedHits are the followers' offered hits per stage (sharded runs).
	shardOfferedHits [][]float64
	// shardPods are the followers' pods, from their ready.json.
	shardPods []string
	// failure is set before finalize runs when an earlier step failed.
	failure *spec.Failure
}

// Execute runs every step. A failing step stops the later work steps but not
// finalize: the manifest is always written, with status failed and the error
// (bench/CLAUDE.md, "Run pipeline").
func Execute(ctx context.Context, opts Options) Result {
	applyDefaults(&opts)
	r := &run{
		opts:      opts,
		writer:    bundle.NewWriter(opts.Store),
		logger:    opts.Logger,
		startedAt: opts.Now(),
		secrets:   secrets{dir: opts.SecretsDir},
	}
	defer r.cleanup()
	return r.execute(ctx)
}

func applyDefaults(opts *Options) {
	if opts.ShardReadyTimeout <= 0 {
		opts.ShardReadyTimeout = DefaultShardReadyTimeout
	}
	if opts.ShardDoneTimeout <= 0 {
		opts.ShardDoneTimeout = DefaultShardDoneTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Reporter == nil {
		opts.Reporter = events.Discard{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.SecretsDir == "" {
		opts.SecretsDir = DefaultSecretsDir
	}
	if opts.NumWorkers <= 0 {
		opts.NumWorkers = runtime.GOMAXPROCS(0)
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepContext
	}
}

func (r *run) execute(ctx context.Context) Result {
	opts := r.opts
	var stepErr error
	for _, s := range pipelineSteps {
		if s.name == spec.PipelineStepFinalize {
			break
		}
		if err := r.runStep(ctx, s); err != nil {
			r.failure = &spec.Failure{Step: s.name, Message: err.Error()}
			stepErr = fmt.Errorf("%s: %w", s.name, err)
			break
		}
	}

	// A cancelled run must still leave a manifest behind.
	var finalizeErr error
	if err := r.runStep(context.WithoutCancel(ctx), pipelineSteps[len(pipelineSteps)-1]); err != nil {
		finalizeErr = fmt.Errorf("%s: %w", spec.PipelineStepFinalize, err)
	}

	result := Result{RunID: opts.RunID, Status: spec.RunStatusSucceeded, Failure: r.failure, Err: errors.Join(stepErr, finalizeErr)}
	if result.Err != nil {
		result.Status = spec.RunStatusFailed
		if result.Failure == nil {
			result.Failure = &spec.Failure{Step: spec.PipelineStepFinalize, Message: finalizeErr.Error()}
		}
	}
	return result
}

// cleanup removes the run's temp files.
func (r *run) cleanup() {
	if r.tempDir != "" {
		if err := os.RemoveAll(r.tempDir); err != nil {
			r.logger.Warn("remove temp directory", "error", err)
		}
	}
}

// runStep reports the step's start and outcome as status events around its
// work. finalize reports the terminal state of the whole run.
func (r *run) runStep(ctx context.Context, s step) error {
	r.opts.Reporter.Report(ctx, s.name, s.state, "")
	skipReason, err := s.run(r, ctx)
	isFinalize := s.name == spec.PipelineStepFinalize
	switch {
	case err != nil:
		r.opts.Reporter.Report(ctx, s.name, spec.ReportedStateFailed, err.Error())
	case isFinalize && r.failure != nil:
		r.opts.Reporter.Report(ctx, s.name, spec.ReportedStateFailed, r.failure.Message)
	case isFinalize:
		r.opts.Reporter.Report(ctx, s.name, spec.ReportedStateSucceeded, "")
	case skipReason != "":
		r.logger.Info("step skipped", "step", s.name, "reason", skipReason)
	}
	return err
}

// sleepContext waits for d or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
