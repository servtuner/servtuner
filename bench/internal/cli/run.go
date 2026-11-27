package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/events"
	"github.com/servtuner/servtuner/bench/internal/pipeline"
	"github.com/servtuner/servtuner/bench/internal/pricing"
	"github.com/servtuner/servtuner/bench/internal/runid"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/target"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

// eventTimeout bounds one status-event POST, so a hung api cannot stall a run.
const eventTimeout = 10 * time.Second

// priceBookTimeout bounds the one download of the price book's datasheet from
// the api: the sheet was about 5 MB in November 2026.
const priceBookTimeout = 60 * time.Second

// objectStoreTimeout bounds one object-store request; large bundle files
// take a while to upload.
const objectStoreTimeout = 10 * time.Minute

type runFlags struct {
	specPath     string
	outDir       string
	runID        string
	apiURL       string
	runTokenFile string
	apiTLS       string
	tlsCertFile  string
	tlsKeyFile   string
	tlsCAFile    string
	// The object-store hop, for an s3:// --out.
	objectStoreTLS      string
	objectStoreCertFile string
	objectStoreKeyFile  string
	objectStoreCAFile   string
	engineOverride      string
	kubeconfig          string
	secretsDir          string
	shardIndex          int
	offline             bool
}

func newRunCommand(streams streams) *cobra.Command {
	var flags runFlags
	command := &cobra.Command{
		Use:   "run --spec <path> --out <bundle dir>",
		Short: "Execute one run and write its bundle",
		Long: "Executes one run end to end and writes the run bundle into --out. " +
			"With --api-url, status events are POSTed to the api (--api-tls is then required; it is never inferred).",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runBench(command.Context(), streams, flags)
		},
	}
	set := command.Flags()
	set.StringVar(&flags.specPath, "spec", "", "path to the RunSpec (YAML or JSON)")
	set.StringVar(&flags.outDir, "out", "", "bundle directory, or s3://<bucket>/<prefix> (default: <data dir>/runs/<run id>)")
	set.StringVar(&flags.objectStoreTLS, "object-store-tls", "", "whether the object-store hop of an s3:// --out uses TLS: true or false (required then; never inferred)")
	set.StringVar(&flags.objectStoreCertFile, "object-store-cert-file", "", "client certificate to present to the object store (with --object-store-tls true)")
	set.StringVar(&flags.objectStoreKeyFile, "object-store-key-file", "", "client key for --object-store-cert-file")
	set.StringVar(&flags.objectStoreCAFile, "object-store-ca-file", "", "extra CA bundle the object store's certificate is verified against (with --object-store-tls true)")
	set.StringVar(&flags.runID, "run-id", "", "run id, a ULID (default: a fresh one)")
	set.StringVar(&flags.apiURL, "api-url", "", "api base URL for status events")
	set.StringVar(&flags.runTokenFile, "run-token-file", "", "file holding the per-run bearer token")
	set.StringVar(&flags.apiTLS, "api-tls", "", "whether the api hop uses TLS: true or false (required with --api-url; never inferred)")
	set.StringVar(&flags.tlsCertFile, "tls-cert-file", "", "client certificate to present to the api (with --api-tls true)")
	set.StringVar(&flags.tlsKeyFile, "tls-key-file", "", "client key for --tls-cert-file")
	set.StringVar(&flags.tlsCAFile, "tls-ca-file", "", "extra CA bundle the api's certificate is verified against, added to the system pool (with --api-tls true)")
	set.StringVar(&flags.kubeconfig, "kubeconfig", "", "kubeconfig for the read-only target snapshot (default: in-cluster config; no cluster means the Kubernetes part is skipped)")
	set.StringVar(&flags.secretsDir, "secrets-dir", "", "directory where the Secrets named by the RunSpec are mounted, one subdirectory per Secret (default: $SERVTUNER_SECRETS_DIR, else "+pipeline.DefaultSecretsDir+")")
	set.BoolVar(&flags.offline, "offline", false, "no internet access: never fetch the public token-price datasheet and refuse AWS Price List prices; recorded in environment.json")
	set.IntVar(&flags.shardIndex, "shard-index", 0, "this process's shard of a RunSpec with engine.shards N: 0 (the default) leads and writes the bundle; 1..N-1 send their share of the load and hand their rows to the leader through --out")
	set.StringVar(&flags.engineOverride, "engine-override", "", "run with a stand-in engine (null)")
	_ = set.MarkHidden("engine-override")
	_ = command.MarkFlagRequired("spec")
	return command
}

func runBench(ctx context.Context, streams streams, flags runFlags) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := streams.logger()

	if flags.engineOverride != "" && flags.engineOverride != engineOverrideNull {
		return usageError(fmt.Errorf("--engine-override %q is not supported (only %q)", flags.engineOverride, engineOverrideNull))
	}
	runID, err := resolveRunID(flags.runID)
	if err != nil {
		return usageError(err)
	}
	if flags.shardIndex < 0 {
		return usageError(fmt.Errorf("--shard-index %d: must be at least 0", flags.shardIndex))
	}
	if flags.shardIndex > 0 && flags.runID == "" {
		return usageError(errors.New("--shard-index needs the leader's --run-id"))
	}
	destination, err := openDestination(ctx, flags, runID)
	if err != nil {
		return usageError(err)
	}
	defer destination.cleanup()
	if flags.shardIndex > 0 {
		return runShard(ctx, streams, flags, runID, destination)
	}
	store, bundleDir := destination.local, destination.local.Root()
	document, err := os.ReadFile(flags.specPath)
	if err != nil {
		return usageError(fmt.Errorf("read spec: %w", err))
	}
	reporter, apiTransport, priceBook, err := newReporter(flags, runID, streams)
	if err != nil {
		return usageError(err)
	}

	result := pipeline.Execute(ctx, pipeline.Options{
		RunID:        runID,
		SpecDocument: document,
		Store:        store,
		BundleDir:    bundleDir,
		NewEngine:    engineFactory(flags.engineOverride),
		SecretsDir:   secretsDir(flags.secretsDir),
		KubeClient:   func() (kubernetes.Interface, string, error) { return target.NewKubeClient(flags.kubeconfig) },
		PriceList:    pricing.NewPriceListClient,
		Reporter:     reporter,
		APITransport: apiTransport,
		PriceBook:    priceBook,
		Offline:      flags.offline,
		Logger:       logger,
		ShardStore:   destination.shared,
	})
	// A failed run's bundle is uploaded too: its manifest says why it failed.
	if err := destination.finish(context.WithoutCancel(ctx)); err != nil {
		return failure(fmt.Errorf("run %s: store the bundle at %s: %w", runID, destination.location, err))
	}
	if result.Err != nil {
		return failure(fmt.Errorf("run %s failed (bundle: %s): %w", runID, destination.location, result.Err))
	}
	fmt.Fprintf(streams.out, "run %s %s; bundle: %s\n", runID, result.Status, destination.location)
	return nil
}

// secretsDir is the flag, else $SERVTUNER_SECRETS_DIR, else the pipeline default.
// runShard runs a follower shard. It writes no bundle of its own: its rows go
// to the leader through the shared store, so the local copy is never uploaded.
func runShard(ctx context.Context, streams streams, flags runFlags, runID string, destination destination) error {
	document, err := os.ReadFile(flags.specPath)
	if err != nil {
		return usageError(fmt.Errorf("read spec: %w", err))
	}
	err = pipeline.ExecuteShard(ctx, pipeline.Options{
		RunID:        runID,
		SpecDocument: document,
		Store:        destination.local,
		ShardStore:   destination.shared,
		ShardIndex:   flags.shardIndex,
		NewEngine:    engineFactory(flags.engineOverride),
		SecretsDir:   secretsDir(flags.secretsDir),
		Logger:       streams.logger(),
	})
	if err != nil {
		return failure(fmt.Errorf("run %s shard %d failed: %w", runID, flags.shardIndex, err))
	}
	fmt.Fprintf(streams.out, "run %s shard %d succeeded\n", runID, flags.shardIndex)
	return nil
}

func secretsDir(flag string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv("SERVTUNER_SECRETS_DIR")
}

func resolveRunID(requested string) (string, error) {
	if requested == "" {
		return runid.New(time.Now())
	}
	if !runid.IsValid(requested) {
		return "", fmt.Errorf("--run-id %q is not a ULID", requested)
	}
	return requested, nil
}

// destination is where the bundle goes. The pipeline always writes a local
// directory; with an s3:// --out that directory is temporary and finish
// uploads it, manifest last.
type destination struct {
	local *storage.FileStore
	// shared is the final store, where the shards of a sharded run meet.
	shared   storage.BundleStore
	location string
	finish   func(context.Context) error
	cleanup  func()
}

// openDestination resolves --out: a directory (default <data dir>/runs/<run
// id>) or s3://<bucket>/<prefix>, under which the run's id is a
// further prefix.
func openDestination(ctx context.Context, flags runFlags, runID string) (destination, error) {
	if !strings.HasPrefix(flags.outDir, storage.S3Scheme) {
		if flags.objectStoreTLS != "" || flags.objectStoreCertFile != "" || flags.objectStoreKeyFile != "" || flags.objectStoreCAFile != "" {
			return destination{}, errors.New("--object-store-* flags need an s3:// --out")
		}
		outDir := flags.outDir
		if outDir == "" {
			dataDir, err := storage.DataDir()
			if err != nil {
				return destination{}, err
			}
			outDir = storage.RunBundleDir(dataDir, runID)
		}
		local := storage.NewFileStore(outDir)
		return destination{local: local, shared: local, location: outDir, finish: func(context.Context) error { return nil }, cleanup: func() {}}, nil
	}
	remote, location, err := openS3(ctx, flags, runID)
	if err != nil {
		return destination{}, err
	}
	workDir, err := os.MkdirTemp("", "bench-bundle-")
	if err != nil {
		return destination{}, fmt.Errorf("create the local bundle directory: %w", err)
	}
	local := storage.NewFileStore(workDir)
	return destination{
		local: local, shared: remote, location: location,
		finish:  func(ctx context.Context) error { return storage.Upload(ctx, local, remote, bundle.ManifestJSON) },
		cleanup: func() { os.RemoveAll(workDir) },
	}, nil
}

// openS3 builds the store for an s3:// --out, with the object-store hop's TLS
// from its flags.
func openS3(ctx context.Context, flags runFlags, runID string) (*storage.S3Store, string, error) {
	location := strings.TrimSuffix(flags.outDir, "/") + "/" + runID + "/"
	cfg, err := storage.S3ConfigFromEnv(location, os.Getenv)
	if err != nil {
		return nil, "", err
	}
	if flags.objectStoreTLS == "" {
		return nil, "", errors.New("an s3:// --out needs --object-store-tls (true or false); it is never inferred")
	}
	useTLS, err := parseTLSFlag("--object-store-tls", flags.objectStoreTLS)
	if err != nil {
		return nil, "", err
	}
	if cfg.Endpoint != "" {
		hop := tlsconfig.Hop{
			Name: "object store", TLS: useTLS, URL: cfg.Endpoint,
			CertFile: flags.objectStoreCertFile, KeyFile: flags.objectStoreKeyFile, CAFile: flags.objectStoreCAFile,
		}
		if cfg.HTTPClient, err = tlsconfig.NewHTTPClient(hop, objectStoreTimeout); err != nil {
			return nil, "", err
		}
	} else if !useTLS {
		// AWS S3 is always HTTPS with a public certificate.
		return nil, "", errors.New("AWS S3 (no SERVTUNER_S3_ENDPOINT) is reached over TLS: use --object-store-tls true")
	}
	store, err := storage.NewS3Store(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	return store, location, nil
}

// newReporter builds the status-event reporter from the api flags, and the
// source of the price book's datasheet, which the api serves to the same run
// token on the run's own route, GET /runs/{id}/datasheet. With no --api-url there is neither, and any other api flag
// is a mistake rather than something to ignore silently.
func newReporter(flags runFlags, runID string, streams streams) (events.Reporter, *spec.HopTransport, *pipeline.PriceBookSource, error) {
	if flags.apiURL == "" {
		if flags.runTokenFile != "" || flags.apiTLS != "" || flags.tlsCertFile != "" || flags.tlsKeyFile != "" || flags.tlsCAFile != "" {
			return nil, nil, nil, fmt.Errorf("--run-token-file, --api-tls, --tls-cert-file, --tls-key-file and --tls-ca-file need --api-url")
		}
		return events.Discard{}, nil, nil, nil
	}
	if flags.apiTLS == "" {
		return nil, nil, nil, fmt.Errorf("--api-url needs --api-tls (true or false); it is never inferred")
	}
	if flags.runTokenFile == "" {
		return nil, nil, nil, fmt.Errorf("--api-url needs --run-token-file")
	}
	useTLS, err := parseTLSFlag("--api-tls", flags.apiTLS)
	if err != nil {
		return nil, nil, nil, err
	}
	hop := tlsconfig.Hop{Name: "api", TLS: useTLS, URL: flags.apiURL, CertFile: flags.tlsCertFile, KeyFile: flags.tlsKeyFile, CAFile: flags.tlsCAFile}
	transport := spec.HopTransport{Tls: hop.TLS, ClientCert: hop.PresentsClientCert()}
	client, err := tlsconfig.NewHTTPClient(hop, eventTimeout)
	if err != nil {
		return nil, nil, nil, err
	}
	sheetClient, err := tlsconfig.NewHTTPClient(hop, priceBookTimeout)
	if err != nil {
		return nil, nil, nil, err
	}
	token, err := readToken(flags.runTokenFile)
	if err != nil {
		return nil, nil, nil, err
	}
	sheetClient.Transport = bearer{token: token, next: sheetClient.Transport}
	priceBook := &pipeline.PriceBookSource{URL: strings.TrimSuffix(flags.apiURL, "/") + "/runs/" + runID + "/datasheet", Client: sheetClient}
	return events.NewHTTPReporter(client, flags.apiURL, runID, token, streams.logger(), time.Now), &transport, priceBook, nil
}

// bearer adds the run token to each request of the price book client.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	next := b.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(request)
}

// readToken reads the bearer token, dropping one trailing newline. A token
// with any other control character (an embedded newline would split the HTTP
// header) is refused. No error message ever contains the token.
func readToken(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read run token file: %w", err)
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	if token == "" {
		return "", fmt.Errorf("run token file %s is empty", path)
	}
	if strings.IndexFunc(token, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("run token file %s contains control characters", path)
	}
	return token, nil
}

// engineFactory picks the engine by name from the registry.
func engineFactory(override string) func(spec.RunSpec, engine.Inputs) (engine.Engine, error) {
	registry := newEngineRegistry()
	return func(runSpec spec.RunSpec, inputs engine.Inputs) (engine.Engine, error) {
		return registry.New(engineName(override, runSpec), inputs)
	}
}

// parseTLSFlag reads a TLS flag, which must say true or false: it is never
// inferred, and a 0.1 mode name gets its replacement.
func parseTLSFlag(flag, value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "mtls", "tls":
		return false, fmt.Errorf("%s %q: servtuner 0.2 replaced transport modes; use %s true, with the client certificate flags to present one", flag, value, flag)
	case "platform":
		return false, fmt.Errorf("%s %q: servtuner 0.2 replaced transport modes; use %s false", flag, value, flag)
	}
	return false, fmt.Errorf("%s %q: want true or false", flag, value)
}
