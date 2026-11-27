package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
	"github.com/servtuner/servtuner/bench/internal/validate"
	"github.com/servtuner/servtuner/bench/internal/version"
)

// clockSource is recorded in environment.json: the host clock, with no NTP
// sync state checked.
const clockSource = "host"

// nativeOutputDirName is the bundle directory of an engine's untouched output.
const nativeOutputDirName = "engine"

// prometheusDiscoveryTimeout bounds the /api/v1/targets read at the start.
const prometheusDiscoveryTimeout = 30 * time.Second

// errSecretUnavailable marks a Secret file that is not mounted or not
// readable, as opposed to a RunSpec that is wrong.
var errSecretUnavailable = errors.New("secret unavailable")

// resolve stores the spec as submitted, validates it (with the scrape interval
// discovered from Prometheus when it is reachable, else assumed), applies the
// defaults, opens the bundle's streams and prepares the engine. Everything
// after it can rely on a valid spec.
func (r *run) resolve(ctx context.Context) (string, error) {
	if r.opts.BundleDir == "" {
		return "", errors.New("pipeline: Options.BundleDir is required")
	}
	if err := r.writer.WriteFile(ctx, bundle.SpecYAML, r.opts.SpecDocument); err != nil {
		return "", err
	}
	parsed, _, err := spec.LoadRunSpec(r.opts.SpecDocument)
	if err != nil {
		return "", validate.WrapParseError(err)
	}
	// Rules that need no network come first, so a bad spec never waits on one.
	if err := validate.Check(parsed).Err(); err != nil {
		return "", err
	}
	r.runSpec, r.hasSpec = parsed, true

	if err := r.discoverPrometheus(ctx); err != nil {
		return "", err
	}
	r.report = validate.CheckWithResolution(parsed, r.resolution)
	for _, warning := range r.report.Warnings {
		r.logger.Warn("spec warning", "warning", warning)
	}
	if err := r.report.Err(); err != nil {
		return "", err
	}

	// Marshalling the typed spec applies the defaults, because the generated
	// types fill them in on unmarshal.
	if err := r.writer.WriteDocument(ctx, bundle.ResolvedSpecJSON, spec.SchemaRunSpec, parsed); err != nil {
		return "", err
	}
	if err := r.openStreams(); err != nil {
		return "", err
	}
	selected, err := r.opts.NewEngine(parsed, r.engineInputs())
	if err != nil {
		return "", fmt.Errorf("select engine: %w", err)
	}
	if err := selected.Prepare(ctx, parsed); err != nil {
		return "", fmt.Errorf("prepare engine %s: %w", selected.Name(), err)
	}
	r.engine = selected
	return "", r.writeEnvironment(ctx)
}

// catalogNames are the catalogs the run collects: the target type's defaults
// plus the RunSpec's, and separately the optional ones that are probed.
func (r *run) catalogNames() (required, optional []spec.CatalogName, err error) {
	adapter, err := target.AdapterFor(r.runSpec.Target.Type)
	if err != nil {
		return nil, nil, err
	}
	seen := map[spec.CatalogName]bool{}
	add := func(into *[]spec.CatalogName, name string) {
		catalog := spec.CatalogName(name)
		if !seen[catalog] {
			seen[catalog] = true
			*into = append(*into, catalog)
		}
	}
	for _, name := range adapter.DefaultCatalogs() {
		add(&required, name)
	}
	if r.runSpec.Prometheus != nil {
		for _, name := range r.runSpec.Prometheus.Catalogs {
			add(&required, name)
		}
		for _, name := range r.runSpec.Prometheus.OptionalCatalogs {
			add(&optional, name)
		}
	}
	return required, optional, nil
}

// discoverPrometheus builds the Prometheus client and reads the effective
// scrape intervals. Without a reachable Prometheus the interval is assumed and
// the reason is kept for the bundle; that is never an error, except for a
// catalog name that does not exist (an invalid spec).
func (r *run) discoverPrometheus(ctx context.Context) error {
	required, optional, err := r.catalogNames()
	if err != nil {
		return err
	}
	roles, err := prometheus.RelevantRoles(prometheus.InKubernetes(r.runSpec.Target), append(required, optional...)...)
	if err != nil {
		return validate.WrapParseError(err)
	}
	r.resolution = prometheus.AssumedResolution()
	configured := r.runSpec.Prometheus
	if configured == nil {
		return nil
	}
	hop, err := r.secrets.hop("prometheus", configured.Tls, configured.Url)
	if err != nil {
		if errors.Is(err, errSecretUnavailable) {
			r.prometheusProblem = err.Error()
			r.logger.Warn("prometheus is not usable; the scrape interval is assumed", "reason", r.prometheusProblem)
			return nil
		}
		return validate.WrapParseError(err)
	}
	client, err := prometheus.NewClient(hop, r.opts.PrometheusOptions)
	if err != nil {
		r.prometheusProblem = err.Error()
		r.logger.Warn("prometheus is not usable; the scrape interval is assumed", "reason", r.prometheusProblem)
		return nil
	}
	r.client = client
	r.prometheusTransport = &spec.HopTransport{Tls: hop.TLS, ClientCert: hop.PresentsClientCert()}
	return r.readTargets(ctx, roles)
}

// readTargets reads /api/v1/targets and resolves the interval from it. A
// failure leaves the assumed resolution in place.
func (r *run) readTargets(ctx context.Context, roles []spec.JobRole) error {
	discoverCtx, cancel := context.WithTimeout(ctx, prometheusDiscoveryTimeout)
	defer cancel()
	targets, err := r.client.DiscoverTargets(discoverCtx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.prometheusProblem = fmt.Sprintf("discover scrape targets: %v", err)
		r.logger.Warn("prometheus is unreachable; the scrape interval is assumed", "reason", r.prometheusProblem)
		return nil
	}
	r.prometheusProblem = ""
	r.targets = &targets
	r.resolution = prometheus.ResolveScrapeInterval(targets, roles)
	return r.writer.WriteDocument(ctx, bundle.PrometheusTargetsJSON, spec.SchemaPrometheusTargets, targets)
}

// openStreams starts requests.parquet and client/samples.parquet. A parts
// directory left by an earlier run in this bundle is recovered first, keeping
// every flushed row (raw first), and then refused: a bundle never mixes runs.
func (r *run) openStreams() error {
	for _, name := range []string{bundle.RequestsParquet, bundle.ClientSamplesParquet} {
		path := filepath.Join(r.opts.BundleDir, filepath.FromSlash(name))
		if err := r.recoverLeftover(name, path); err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: bundle %s already holds %s from an earlier run; use an empty --out", bundle.ErrWrite, r.opts.BundleDir, name)
		}
	}
	var err error
	if r.requests, err = bundle.NewRequestStream(r.opts.BundleDir, r.opts.StreamOptions); err != nil {
		return fmt.Errorf("%w: %w", bundle.ErrWrite, err)
	}
	if r.samples, err = bundle.NewClientStream(r.opts.BundleDir, r.opts.StreamOptions); err != nil {
		_ = r.requests.Close()
		return fmt.Errorf("%w: %w", bundle.ErrWrite, err)
	}
	r.streams = true
	return nil
}

// engineInputs is what the engine factory gets. Credentials resolve lazily, so
// only an engine that talks to the target needs the Secrets mounted.
func (r *run) engineInputs() engine.Inputs {
	inputs := engine.Inputs{
		RunID:       r.opts.RunID,
		OutputDir:   filepath.Join(r.opts.BundleDir, nativeOutputDirName),
		NumWorkers:  r.opts.NumWorkers,
		ClientSink:  r.samples,
		Now:         r.opts.Now,
		Credentials: r.credentials,
		ShardCount:  r.shardCount(),
	}
	if inputs.ShardCount > 1 {
		// The leader's rows say worker 0, like each follower's its own index.
		inputs.ClientSink = workerSink{worker: 0, requests: r.requests, samples: r.samples}
	}
	return inputs
}

// requestSink is where the engine writes its rows: tagged with worker 0 in a
// sharded run.
func (r *run) requestSink() engine.Sink {
	if r.shardCount() > 1 {
		return workerSink{worker: 0, requests: r.requests, samples: r.samples}
	}
	return r.requests
}

// credentials resolves the target hop's files and token from the mounted
// Secrets. Nothing it returns is logged.
func (r *run) credentials() (engine.Credentials, error) {
	hop, err := r.secrets.hop("target", r.runSpec.Target.Tls, r.runSpec.Target.Endpoint)
	if err != nil {
		return engine.Credentials{}, err
	}
	token, err := r.secrets.bearerToken(r.runSpec.Target.Auth)
	if err != nil {
		return engine.Credentials{}, err
	}
	credentials := engine.Credentials{BearerToken: token, CertFile: hop.CertFile, KeyFile: hop.KeyFile, CAFile: hop.CAFile}
	if hop.CAFile != "" {
		if r.tempDir, err = os.MkdirTemp("", "bench-run-"); err != nil {
			return engine.Credentials{}, fmt.Errorf("create temp directory: %w", err)
		}
		credentials.CABundleFile, err = writeCombinedCABundle(r.tempDir, hop.CAFile, r.opts.SystemCABundle, r.opts.Getenv)
		if err != nil {
			return engine.Credentials{}, err
		}
	}
	return credentials, nil
}

// environment records what ran the benchmark and how every hop the run used
// was secured (never inferred, always recorded). An
// unused hop is omitted, as the schema says. It is rewritten as the run learns
// more (knobs after the snapshot, the HTTP version and workload after the load).
func (r *run) environment() (spec.Environment, error) {
	targetTransport, err := spec.TransportOf(r.runSpec.Target.Tls)
	if err != nil {
		return spec.Environment{}, fmt.Errorf("target.tls: %w", err)
	}
	environment := spec.Environment{
		BenchVersion: version.Version,
		GoVersion:    runtime.Version(),
		Engine:       spec.EngineInfo{Name: r.engine.Name(), Version: r.engine.Version()},
		ClockSource:  clockSource,
		Transport:    spec.HopTransports{Target: targetTransport, Prometheus: r.prometheusTransport, Api: r.opts.APITransport},
		TlsRelayUsed: false,
	}
	if r.opts.Offline {
		offline := true
		environment.Offline = &offline
	}
	if capper, ok := r.engine.(engine.InFlightCapper); ok && capper.MaxInFlight() > 0 {
		// Each shard has its own cap, so the run's is their sum.
		limit := capper.MaxInFlight() * r.shardCount()
		environment.Engine.MaxInFlight = &limit
	}
	if len(r.shardPods) > 0 {
		environment.ShardPods = r.shardPods
	}
	if len(r.knobs) > 0 {
		environment.Knobs = spec.EnvironmentKnobs(r.knobs)
	}
	setIfNotEmpty := func(into **string, name string) {
		if value := r.opts.Getenv(name); value != "" {
			*into = &value
		}
	}
	setIfNotEmpty(&environment.Pod, "POD_NAME")
	setIfNotEmpty(&environment.Namespace, "POD_NAMESPACE")
	setIfNotEmpty(&environment.Node, "NODE_NAME")
	if reporter, ok := r.engine.(engine.EnvironmentReporter); ok && len(r.windows) > 0 {
		httpVersion, info := reporter.HTTPVersion(), reporter.WorkloadInfo()
		environment.HttpVersion, environment.Workload = &httpVersion, &info
	}
	return environment, nil
}

func (r *run) writeEnvironment(ctx context.Context) error {
	environment, err := r.environment()
	if err != nil {
		return err
	}
	return r.writer.WriteDocument(ctx, bundle.EnvironmentJSON, spec.SchemaEnvironment, environment)
}
