package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

const (
	examplesRoot = "../../../schema/examples/runspec"
	yamlFixture  = "../../testdata/runspec-minimal.yaml"
	testRunID    = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

// runAsCLIEnv makes the test binary behave as the bench binary. mTLS tests
// need it because Go reads SSL_CERT_FILE once per process, and it is the way
// the pod's combined CA bundle reaches the client.
const runAsCLIEnv = "BENCH_TEST_RUN_AS_CLI"

func TestMain(m *testing.M) {
	if os.Getenv(runAsCLIEnv) == "1" {
		os.Exit(Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

type outcome struct {
	code   int
	stdout string
	stderr string
}

func run(args ...string) outcome {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), args, &stdout, &stderr)
	return outcome{code, stdout.String(), stderr.String()}
}

func runSubprocess(t *testing.T, env []string, args ...string) outcome {
	t.Helper()
	command := exec.Command(os.Args[0], args...)
	command.Env = append(os.Environ(), append(env, runAsCLIEnv+"=1")...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return outcome{code, stdout.String(), stderr.String()}
}

func examples(t *testing.T, validity string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(examplesRoot, validity, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no %s examples: %v", validity, err)
	}
	return files
}

func writeSpec(t *testing.T, content []byte) string {
	t.Helper()
	return tlstest.WriteFile(t, t.TempDir(), "spec.json", content)
}

func TestValidateAcceptsEveryValidExample(t *testing.T) {
	for _, file := range examples(t, "valid") {
		t.Run(filepath.Base(file), func(t *testing.T) {
			result := run("validate", "--spec", file)
			if result.code != ExitOK || !strings.HasPrefix(result.stdout, "ok: ") {
				t.Errorf("exit %d, stdout %q, stderr %q", result.code, result.stdout, result.stderr)
			}
		})
	}
}

func TestValidateRejectsEveryInvalidExample(t *testing.T) {
	for _, file := range examples(t, "invalid") {
		t.Run(filepath.Base(file), func(t *testing.T) {
			result := run("validate", "--spec", file)
			if result.code != ExitInvalidSpec || result.stdout != "" || !strings.Contains(result.stderr, "invalid spec") {
				t.Errorf("exit %d, stdout %q, stderr %q", result.code, result.stdout, result.stderr)
			}
		})
	}
}

func TestValidateAcceptsYAML(t *testing.T) {
	result := run("validate", "--spec", yamlFixture)
	if result.code != ExitOK || !strings.Contains(result.stdout, "vllm-smoke-yaml") {
		t.Errorf("exit %d, stdout %q, stderr %q", result.code, result.stdout, result.stderr)
	}
}

func TestValidateShortStageIsAWarningUnlessStrict(t *testing.T) {
	minimal := filepath.Join(examplesRoot, "valid", "minimal-vllm-client-cert.json") // one 30s stage
	result := run("validate", "--spec", minimal)
	if result.code != ExitOK || !strings.Contains(result.stderr, "warning: load.stages[0].duration 30s is shorter than the minimum 1m0s") {
		t.Errorf("exit %d, stderr %q", result.code, result.stderr)
	}
	if strict := run("validate", "--spec", minimal, "--strict"); strict.code != ExitInvalidSpec {
		t.Errorf("--strict exit = %d, want 2; stderr %q", strict.code, strict.stderr)
	}
}

// TestValidateListsEveryTransportProblem: both hops of the litellm example are
// broken at once and both are reported.
func TestValidateListsEveryTransportProblem(t *testing.T) {
	original, err := os.ReadFile(filepath.Join(examplesRoot, "valid", "litellm-target-client-cert-prometheus-tls-off.json"))
	if err != nil {
		t.Fatal(err)
	}
	broken := bytes.Replace(original, []byte(`"https://vllm`), []byte(`"http://vllm`), 1)          // tls with http://
	broken = bytes.Replace(broken, []byte(`"http://prometheus`), []byte(`"https://prometheus`), 1) // no tls with https://
	result := run("validate", "--spec", writeSpec(t, broken))
	if result.code != ExitInvalidSpec {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	for _, want := range []string{"2 problem(s)", "target: tls enabled needs an https:// url", "prometheus: tls disabled needs an http:// url"} {
		if !strings.Contains(result.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, result.stderr)
		}
	}
}

func TestValidateListsEverySchemaViolation(t *testing.T) {
	result := run("validate", "--spec", writeSpec(t, []byte(`{"apiVersion": "servtuner/v9", "kind": "RunSpec", "extra": 1}`)))
	if result.code != ExitInvalidSpec {
		t.Fatalf("exit %d", result.code)
	}
	for _, want := range []string{"apiVersion", "extra", "target"} {
		if !strings.Contains(result.stderr, want) {
			t.Errorf("stderr does not mention %q:\n%s", want, result.stderr)
		}
	}
}

func TestValidateRejectsUrlsWithoutHost(t *testing.T) {
	original, err := os.ReadFile(filepath.Join(examplesRoot, "valid", "litellm-target-client-cert-prometheus-tls-off.json"))
	if err != nil {
		t.Fatal(err)
	}
	broken := bytes.Replace(original, []byte(`"https://vllm.inference.svc:8000/v1"`), []byte(`"https://"`), 1)
	broken = bytes.Replace(broken, []byte(`"http://prometheus.monitoring.svc:9090"`), []byte(`"http:///x"`), 1)
	result := run("validate", "--spec", writeSpec(t, broken))
	if result.code != ExitInvalidSpec || strings.Count(result.stderr, "has no host") != 2 {
		t.Errorf("exit %d, stderr %q", result.code, result.stderr)
	}
}

func TestValidateRejectsDisabledTLSWithACertificateAndMissingFile(t *testing.T) {
	// The stderr check keeps a renamed fixture from passing as a missing file.
	if result := run("validate", "--spec", filepath.Join(examplesRoot, "invalid", "tls-disabled-with-client-cert.json")); result.code != ExitInvalidSpec || !strings.Contains(result.stderr, "target") {
		t.Errorf("exit %d, stderr %q", result.code, result.stderr)
	}
	if result := run("validate", "--spec", filepath.Join(t.TempDir(), "absent.json")); result.code != ExitInvalidSpec {
		t.Errorf("missing file exit %d", result.code)
	}
	if result := run("validate"); result.code != ExitInvalidSpec {
		t.Errorf("missing --spec exit %d", result.code)
	}
}

func bundleDir(t *testing.T) string { return filepath.Join(t.TempDir(), "bundle") }

func readBundleFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestRunWithNullEngineWritesAValidBundle(t *testing.T) {
	out := bundleDir(t)
	specFile := filepath.Join(examplesRoot, "valid", "full-vllm-client-cert.json")
	result := run("run", "--spec", specFile, "--out", out, "--run-id", testRunID, "--engine-override", "null")
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	manifest, err := spec.Parse[spec.Manifest](spec.SchemaManifest, readBundleFile(t, out, bundle.ManifestJSON))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RunId != testRunID || manifest.Status != spec.RunStatusSucceeded {
		t.Errorf("manifest = %+v", manifest)
	}
	var listed []string
	for _, file := range manifest.Files {
		listed = append(listed, file.Path)
	}
	// No cluster and no reachable Prometheus: both snapshots are recorded as
	// skipped, queries.json records why nothing was collected, and the analysis
	// is still written from the raw files.
	want := []string{
		"analysis.json", "capacity.csv", "capacity.json", "capacity_model.json", "charts/client_timeseries.json", "charts/histograms.json", "charts/inflight.json", "charts/server.json", "client/samples.parquet", "environment.json",
		"patches/resources.yaml", "phases.json", "prometheus/queries.json", "recommendations.json", "requests.parquet", "spec.resolved.json", "spec.yaml", "summary.json",
		"target/snapshot-post.json", "target/snapshot-pre.json",
	}
	if !slices.Equal(listed, want) {
		t.Errorf("manifest files = %v, want %v", listed, want)
	}
	original, _ := os.ReadFile(specFile)
	if !bytes.Equal(readBundleFile(t, out, bundle.SpecYAML), original) {
		t.Error("spec.yaml is not byte-for-byte the submitted file")
	}
}

func TestRunAcceptsYAMLSpecAndGeneratesARunID(t *testing.T) {
	out := bundleDir(t)
	result := run("run", "--spec", yamlFixture, "--out", out, "--engine-override", "null")
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	manifest, err := spec.Parse[spec.Manifest](spec.SchemaManifest, readBundleFile(t, out, bundle.ManifestJSON))
	if err != nil || len(manifest.RunId) != 26 {
		t.Errorf("manifest = %+v, %v", manifest, err)
	}
	var resolved map[string]any
	if err := json.Unmarshal(readBundleFile(t, out, bundle.ResolvedSpecJSON), &resolved); err != nil {
		t.Fatal(err)
	}
	if load := resolved["load"].(map[string]any); load["warmup"] != "30s" || load["profile"] != "stages" {
		t.Errorf("defaults not applied in spec.resolved.json: %v", load)
	}
}

func TestRunWithoutOutUsesTheDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SERVTUNER_DATA_DIR", dataDir)
	result := run("run", "--spec", yamlFixture, "--run-id", testRunID, "--engine-override", "null")
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	if _, err := os.Stat(filepath.Join(storage.RunBundleDir(dataDir, testRunID), bundle.ManifestJSON)); err != nil {
		t.Error(err)
	}
}

func TestRunWithoutTheTargetSecretFailsWithAFailedBundle(t *testing.T) {
	out := bundleDir(t)
	// The fixture's target names a client certificate Secret that is not mounted here,
	// so the real engine cannot be built: the run fails at resolve, and the
	// bundle still records why.
	result := run("run", "--spec", yamlFixture, "--out", out)
	if result.code != ExitRunFailed || !strings.Contains(result.stderr, "secret unavailable") {
		t.Errorf("exit %d, stderr %q", result.code, result.stderr)
	}
	manifest, err := spec.Parse[spec.Manifest](spec.SchemaManifest, readBundleFile(t, out, bundle.ManifestJSON))
	if err != nil || manifest.Status != spec.RunStatusFailed || manifest.Error == nil || manifest.Error.Step != spec.PipelineStepResolve {
		t.Errorf("manifest = %+v, %v", manifest, err)
	}
}

func TestRunWithInvalidSpecExitsTwoButStillWritesAFailedManifest(t *testing.T) {
	out := bundleDir(t)
	result := run("run", "--spec", filepath.Join(examplesRoot, "invalid", "stage-rate-zero.json"), "--out", out, "--engine-override", "null")
	if result.code != ExitInvalidSpec {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	manifest, err := spec.Parse[spec.Manifest](spec.SchemaManifest, readBundleFile(t, out, bundle.ManifestJSON))
	if err != nil || manifest.Status != spec.RunStatusFailed {
		t.Errorf("manifest = %+v, %v", manifest, err)
	}
}

func TestRunExitsFourWhenTheBundleCannotBeWritten(t *testing.T) {
	blocker := tlstest.WriteFile(t, t.TempDir(), "file", []byte("x"))
	result := run("run", "--spec", yamlFixture, "--out", filepath.Join(blocker, "bundle"), "--engine-override", "null")
	if result.code != ExitBundleWriteFailed {
		t.Errorf("exit %d, stderr %q", result.code, result.stderr)
	}
}

func TestBadFlagsExitTwo(t *testing.T) {
	token := tlstest.WriteFile(t, t.TempDir(), "token", []byte("t\n"))
	base := []string{"run", "--spec", yamlFixture, "--out", bundleDir(t), "--engine-override", "null"}
	cases := map[string][]string{
		"unknown command":           {"frobnicate"},
		"unknown flag":              {"run", "--spec", yamlFixture, "--nope"},
		"missing spec":              {"run", "--out", bundleDir(t)},
		"unreadable spec":           {"run", "--spec", filepath.Join(t.TempDir(), "absent.yaml"), "--engine-override", "null"},
		"bad run id":                append(slices.Clone(base), "--run-id", "not-a-ulid"),
		"unknown engine override":   append(slices.Clone(base), "--engine-override", "loadgen"),
		"s3 output":                 {"run", "--spec", yamlFixture, "--out", "s3://bucket/prefix", "--engine-override", "null"},
		"api url without --api-tls": append(slices.Clone(base), "--api-url", "http://api:8080", "--run-token-file", token),
		"api url without token":     append(slices.Clone(base), "--api-url", "http://api:8080", "--api-tls", "false"),
		"--api-tls not a boolean":   append(slices.Clone(base), "--api-url", "http://api:8080", "--run-token-file", token, "--api-tls", "plain"),
		"0.1 mode name":             append(slices.Clone(base), "--api-url", "https://api:8443", "--run-token-file", token, "--api-tls", "mtls"),
		"tls with http url":         append(slices.Clone(base), "--api-url", "http://api:8080", "--run-token-file", token, "--api-tls", "true", "--tls-cert-file", token, "--tls-key-file", token),
		"cert without key":          append(slices.Clone(base), "--api-url", "https://api:8443", "--run-token-file", token, "--api-tls", "true", "--tls-cert-file", token),
		"no tls with https url":     append(slices.Clone(base), "--api-url", "https://api:8443", "--run-token-file", token, "--api-tls", "false"),
		"no tls with cert flags":    append(slices.Clone(base), "--api-url", "http://api:8080", "--run-token-file", token, "--api-tls", "false", "--tls-cert-file", token),
		"api flags without api url": append(slices.Clone(base), "--api-tls", "false"),
		"ca file without api url":   append(slices.Clone(base), "--tls-ca-file", token),
		"no tls with ca file":       append(slices.Clone(base), "--api-url", "http://api:8080", "--run-token-file", token, "--api-tls", "false", "--tls-ca-file", token),
		"token file missing":        append(slices.Clone(base), "--api-url", "http://api:8080", "--api-tls", "false", "--run-token-file", filepath.Join(t.TempDir(), "absent")),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if result := run(args...); result.code != ExitInvalidSpec {
				t.Errorf("exit %d, want 2; stderr %q", result.code, result.stderr)
			}
		})
	}
}

// eventLog collects the events the api double received.
type eventLog struct {
	mutex  sync.Mutex
	paths  []string
	tokens []string
	events []spec.Event
}

func (e *eventLog) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var event spec.Event
		_ = json.Unmarshal(body, &event)
		e.mutex.Lock()
		defer e.mutex.Unlock()
		e.paths = append(e.paths, r.Method+" "+r.URL.Path)
		e.tokens = append(e.tokens, r.Header.Get("Authorization"))
		e.events = append(e.events, event)
		w.WriteHeader(http.StatusAccepted)
	})
}

func (e *eventLog) check(t *testing.T) {
	t.Helper()
	if len(e.events) < 8 { // one start per step plus the terminal event
		t.Fatalf("got %d events, want at least 8", len(e.events))
	}
	for index, event := range e.events {
		if e.paths[index] != "POST /runs/"+testRunID+"/events" || e.tokens[index] != "Bearer secret-token" {
			t.Errorf("event %d: path %q, auth %q", index, e.paths[index], e.tokens[index])
		}
		if event.Sequence != index {
			t.Errorf("event %d has sequence %d", index, event.Sequence)
		}
	}
	last := e.events[len(e.events)-1]
	if last.Step != spec.PipelineStepFinalize || last.State != spec.ReportedStateSucceeded {
		t.Errorf("last event = %+v", last)
	}
}

func TestRunPostsEventsWithoutTLS(t *testing.T) {
	received := &eventLog{}
	server := tlstest.NewPlainServer(t, received.handler())
	token := tlstest.WriteFile(t, t.TempDir(), "token", []byte("secret-token\n"))
	out := bundleDir(t)
	result := run("run", "--spec", yamlFixture, "--out", out, "--run-id", testRunID, "--engine-override", "null",
		"--api-url", server.URL, "--run-token-file", token, "--api-tls", "false")
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	received.check(t)
	environment, err := spec.Parse[spec.Environment](spec.SchemaEnvironment, readBundleFile(t, out, bundle.EnvironmentJSON))
	if err != nil || environment.Transport.Api == nil || *environment.Transport.Api != (spec.HopTransport{}) {
		t.Errorf("environment transport = %+v, %v", environment.Transport, err)
	}
	if strings.Contains(result.stderr, "secret-token") || strings.Contains(result.stdout, "secret-token") {
		t.Error("the run token appeared in the output")
	}
}

func TestRunPostsEventsWithAClientCertificate(t *testing.T) {
	received := &eventLog{}
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, received.handler())
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	caFile := tlstest.WriteFile(t, directory, "bundle.crt", authority.CertPEM)
	token := tlstest.WriteFile(t, directory, "token", []byte("secret-token\n"))
	out := bundleDir(t)
	// SSL_CERT_FILE is how the pod's combined CA bundle reaches Go clients.
	result := runSubprocess(t, []string{"SSL_CERT_FILE=" + caFile},
		"run", "--spec", yamlFixture, "--out", out, "--run-id", testRunID, "--engine-override", "null",
		"--api-url", server.URL, "--run-token-file", token, "--api-tls", "true",
		"--tls-cert-file", certFile, "--tls-key-file", keyFile)
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	received.check(t)
	environment, err := spec.Parse[spec.Environment](spec.SchemaEnvironment, readBundleFile(t, out, bundle.EnvironmentJSON))
	if err != nil || environment.Transport.Api == nil || *environment.Transport.Api != (spec.HopTransport{Tls: true, ClientCert: true}) {
		t.Errorf("environment transport = %+v, %v", environment.Transport, err)
	}
}

// A Job pod trusts the private CA through --tls-ca-file, which adds to the
// system pool instead of replacing it the way SSL_CERT_FILE does.
func TestRunPostsEventsWithAClientCertificateAndACAFile(t *testing.T) {
	received := &eventLog{}
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, received.handler())
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	caFile := tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM)
	token := tlstest.WriteFile(t, directory, "token", []byte("secret-token\n"))
	result := run("run", "--spec", yamlFixture, "--out", bundleDir(t), "--run-id", testRunID, "--engine-override", "null",
		"--api-url", server.URL, "--run-token-file", token, "--api-tls", "true",
		"--tls-cert-file", certFile, "--tls-key-file", keyFile, "--tls-ca-file", caFile)
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	received.check(t)
}

// TestRunSurvivesEventDeliveryFailure: the api rejecting the events (here an
// untrusted server certificate) is logged and the run still succeeds.
func TestRunSurvivesEventDeliveryFailure(t *testing.T) {
	received := &eventLog{}
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, received.handler())
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	token := tlstest.WriteFile(t, directory, "token", []byte("secret-token\n"))
	// Without SSL_CERT_FILE the private CA is not trusted, so every POST fails.
	result := run("run", "--spec", yamlFixture, "--out", bundleDir(t), "--engine-override", "null",
		"--api-url", server.URL, "--run-token-file", token, "--api-tls", "true",
		"--tls-cert-file", certFile, "--tls-key-file", keyFile)
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "not delivered") || len(received.events) != 0 {
		t.Errorf("stderr %q, events %d", result.stderr, len(received.events))
	}
}

// TestControlCharactersInTheTokenFileExitTwoWithoutEchoingIt: an embedded
// newline would split the Authorization header.
func TestControlCharactersInTheTokenFileExitTwoWithoutEchoingIt(t *testing.T) {
	for name, content := range map[string]string{
		"embedded newline": "secret-part-one\nsecret-part-two\n",
		"embedded CR":      "secret-part-one\rX-Injected: 1",
		"NUL":              "secret\x00token",
		"only a newline":   "\n",
	} {
		t.Run(name, func(t *testing.T) {
			token := tlstest.WriteFile(t, t.TempDir(), "token", []byte(content))
			result := run("run", "--spec", yamlFixture, "--out", bundleDir(t), "--engine-override", "null",
				"--api-url", "http://api:8080", "--api-tls", "false", "--run-token-file", token)
			if result.code != ExitInvalidSpec {
				t.Errorf("exit %d, stderr %q", result.code, result.stderr)
			}
			if strings.Contains(result.stderr, "secret") || strings.Contains(result.stdout, "secret") {
				t.Errorf("the token leaked into the output: %q", result.stderr)
			}
		})
	}
	for _, content := range []string{"secret-token", "secret-token\n", "secret-token\r\n"} {
		if token, err := readToken(tlstest.WriteFile(t, t.TempDir(), "token", []byte(content))); err != nil || token != "secret-token" {
			t.Errorf("readToken(%q) = %q, %v", content, token, err)
		}
	}
}
