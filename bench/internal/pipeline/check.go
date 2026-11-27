package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
	"github.com/servtuner/servtuner/bench/internal/workload"
)

// Timeouts of servtuner check-target. A check answers in seconds or says why not;
// generate waits longer because a cold model can take a while to its first token.
const (
	checkDialTimeout       = 5 * time.Second
	checkRequestTimeout    = 10 * time.Second
	checkGenerateTimeout   = 60 * time.Second
	checkPrometheusTimeout = 10 * time.Second
	// maxListedModels caps the served models named when the wanted one is missing.
	maxListedModels = 5
	// maxBodyExcerpt caps how much of an error response a check message quotes.
	maxBodyExcerpt = 200
	// maxResponseBytes caps how much of a response a check reads: 1 MiB.
	maxResponseBytes = 1048576
)

// CheckOptions are what servtuner check-target needs from its environment.
type CheckOptions struct {
	// SecretsDir is where the Secrets the target names are, as for servtuner run.
	// Empty means DefaultSecretsDir.
	SecretsDir string
	// KubeClient returns the read-only Kubernetes client, as for servtuner run; nil
	// means no cluster, so a workload target's pods are not checked.
	KubeClient func() (kubernetes.Interface, string, error)
	// Now is the clock; nil means time.Now.
	Now               func() time.Time
	PrometheusOptions prometheus.Options
}

// checker runs the target checks in order. Each check
// that depends on a failed one is skipped, so the first failure is the one to fix.
type checker struct {
	opts    CheckOptions
	secrets secrets
	request spec.CheckTargetRequest
	target  spec.Target
	parsed  *url.URL
	checks  []spec.TargetCheck
	// failed is the first failed check of the path to the target; later target
	// checks are skipped with it as the reason.
	failed string
	// Filled in as the checks go.
	hop       tlsconfig.Hop
	client    *http.Client
	served    []string
	snapshots []*spec.TargetSnapshot
	workload  *spec.TargetCheck
}

// CheckTarget answers whether a run could reach, authenticate to and observe
// the target, with the same TLS, credential and Prometheus code a run uses.
// It never returns an error: every problem is a
// check result. No secret value ever reaches a message.
func CheckTarget(ctx context.Context, opts CheckOptions, request spec.CheckTargetRequest) spec.CheckTargetResult {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SecretsDir == "" {
		opts.SecretsDir = DefaultSecretsDir
	}
	c := &checker{opts: opts, secrets: secrets{dir: opts.SecretsDir}, request: request, target: request.Target}
	checkedAt := opts.Now()
	c.resolve(ctx)
	c.connect(ctx)
	c.transport(ctx)
	c.auth(ctx)
	c.model()
	c.generate(ctx)
	// The workload's pods are read before the Prometheus checks, which select
	// by them, and reported last.
	c.readWorkload(ctx)
	c.metrics(ctx)
	c.gpuMetrics(ctx)
	if c.workload != nil {
		c.checks = append(c.checks, *c.workload)
	}
	return spec.CheckTargetResult{
		Status: overallStatus(c.checks), CheckedAt: spec.NewUTCTimestamp(checkedAt),
		TargetType: c.target.Type, Endpoint: c.target.Endpoint, Checks: c.checks,
	}
}

func overallStatus(checks []spec.TargetCheck) spec.CheckStatus {
	status := spec.CheckStatusOk
	for _, check := range checks {
		switch check.Status {
		case spec.CheckStepStatusFailed:
			return spec.CheckStatusFailed
		case spec.CheckStepStatusWarning:
			status = spec.CheckStatusWarning
		}
	}
	return status
}

func (c *checker) record(name spec.CheckName, status spec.CheckStepStatus, started time.Time, format string, args ...any) {
	duration := 0.0
	if !started.IsZero() {
		duration = time.Since(started).Seconds()
	}
	c.checks = append(c.checks, spec.TargetCheck{Name: name, Status: status, Message: fmt.Sprintf(format, args...), DurationSeconds: duration})
	if status == spec.CheckStepStatusFailed && c.failed == "" {
		c.failed = string(name)
	}
}

// skipIfBlocked records a skipped check when an earlier step on the path to the
// target failed, and reports whether it did.
func (c *checker) skipIfBlocked(name spec.CheckName) bool {
	if c.failed == "" {
		return false
	}
	c.record(name, spec.CheckStepStatusSkipped, time.Time{}, "Skipped: the %s check failed.", c.failed)
	return true
}

func (c *checker) resolve(ctx context.Context) {
	started := time.Now()
	parsed, err := url.Parse(c.target.Endpoint)
	if err != nil || parsed.Hostname() == "" {
		c.record(spec.CheckNameResolve, spec.CheckStepStatusFailed, started, "The endpoint %q is not a URL with a host.", c.target.Endpoint)
		return
	}
	c.parsed = parsed
	host := parsed.Hostname()
	if net.ParseIP(host) != nil {
		c.record(spec.CheckNameResolve, spec.CheckStepStatusOk, started, "%s is an IP address; nothing to resolve.", host)
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, checkDialTimeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupHost(lookupCtx, host)
	if err != nil {
		c.record(spec.CheckNameResolve, spec.CheckStepStatusFailed, started, "%s does not resolve from here: %v.", host, err)
		return
	}
	c.record(spec.CheckNameResolve, spec.CheckStepStatusOk, started, "%s resolves to %s.", host, strings.Join(addresses, ", "))
}

func (c *checker) address() string {
	port := c.parsed.Port()
	if port == "" {
		port = "80"
		if c.parsed.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(c.parsed.Hostname(), port)
}

func (c *checker) connect(ctx context.Context) {
	if c.skipIfBlocked(spec.CheckNameConnect) {
		return
	}
	started := time.Now()
	dialer := net.Dialer{Timeout: checkDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.address())
	if err != nil {
		c.record(spec.CheckNameConnect, spec.CheckStepStatusFailed, started, "Cannot open a TCP connection to %s: %v.", c.address(), err)
		return
	}
	_ = conn.Close()
	c.record(spec.CheckNameConnect, spec.CheckStepStatusOk, started, "Connected to %s in %d ms.", c.address(), time.Since(started).Milliseconds())
}

// transport is the tls check: the declared tls block against the scheme, and
// with TLS a real handshake, with the client certificate when there is one.
func (c *checker) transport(ctx context.Context) {
	if c.skipIfBlocked(spec.CheckNameTls) {
		return
	}
	started := time.Now()
	hop, err := c.secrets.hop("target", c.target.Tls, c.target.Endpoint)
	if err != nil {
		c.record(spec.CheckNameTls, spec.CheckStepStatusFailed, started, "The certificate files are not usable: %v.", err)
		return
	}
	if err := tlsconfig.CheckScheme(hop.TLS, c.target.Endpoint); err != nil {
		c.record(spec.CheckNameTls, spec.CheckStepStatusFailed, started, "%v.", err)
		return
	}
	client, err := tlsconfig.NewHTTPClient(hop, checkRequestTimeout)
	if err != nil {
		c.record(spec.CheckNameTls, spec.CheckStepStatusFailed, started, "The TLS settings are not usable: %v.", err)
		return
	}
	c.hop, c.client = hop, client
	if !hop.TLS {
		c.record(spec.CheckNameTls, spec.CheckStepStatusOk, started, "Plain HTTP, as tls.enabled false declares: servtuner does not encrypt this hop, so a service mesh or an encrypted network must.")
		return
	}
	config, err := tlsconfig.ClientConfig(hop)
	if err != nil {
		c.record(spec.CheckNameTls, spec.CheckStepStatusFailed, started, "The TLS settings are not usable: %v.", err)
		return
	}
	config = config.Clone()
	if config.ServerName == "" {
		config.ServerName = c.parsed.Hostname()
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: checkDialTimeout}, Config: config}
	conn, err := dialer.DialContext(ctx, "tcp", c.address())
	if err != nil {
		c.record(spec.CheckNameTls, spec.CheckStepStatusFailed, started, "The TLS handshake with %s failed: %v.", c.address(), err)
		return
	}
	version := tls.VersionName(conn.(*tls.Conn).ConnectionState().Version)
	_ = conn.Close()
	if !hop.PresentsClientCert() {
		c.record(spec.CheckNameTls, spec.CheckStepStatusOk, started, "%s handshake succeeded without a client certificate, as the tls block declares, and the server's certificate verified.", version)
		return
	}
	c.record(spec.CheckNameTls, spec.CheckStepStatusOk, started, "%s handshake succeeded with the client certificate, and the server's certificate verified.", version)
}

func (c *checker) url(path string) string {
	return strings.TrimRight(c.target.Endpoint, "/") + path
}

// excerpt is the start of a response body for a message.
func excerpt(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > maxBodyExcerpt {
		text = text[:maxBodyExcerpt] + "…"
	}
	if text == "" {
		return "(empty body)"
	}
	return text
}

func (c *checker) auth(ctx context.Context) {
	if c.skipIfBlocked(spec.CheckNameAuth) {
		return
	}
	started := time.Now()
	token, err := c.secrets.bearerToken(c.target.Auth)
	if err != nil {
		c.record(spec.CheckNameAuth, spec.CheckStepStatusFailed, started, "No usable API key: %v.", err)
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/models"), nil)
	if err != nil {
		c.record(spec.CheckNameAuth, spec.CheckStepStatusFailed, started, "Cannot build the request: %v.", err)
		return
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.record(spec.CheckNameAuth, spec.CheckStepStatusFailed, started, "GET %s failed: %v.", request.URL.Path, err)
		return
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	switch {
	case response.StatusCode == http.StatusOK:
		c.served = servedModels(body)
		if token == "" {
			c.record(spec.CheckNameAuth, spec.CheckStepStatusOk, started, "GET %s returned 200 without a key: the server needs none.", request.URL.Path)
		} else {
			c.record(spec.CheckNameAuth, spec.CheckStepStatusOk, started, "GET %s with the API key returned 200.", request.URL.Path)
		}
	case (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) && token == "":
		c.record(spec.CheckNameAuth, spec.CheckStepStatusFailed, started, "%d from GET %s: the server needs an API key and the target has none (target.auth).", response.StatusCode, request.URL.Path)
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		c.record(spec.CheckNameAuth, spec.CheckStepStatusFailed, started, "%d from GET %s: the API key was rejected.", response.StatusCode, request.URL.Path)
	default:
		c.record(spec.CheckNameAuth, spec.CheckStepStatusFailed, started, "GET %s returned %d: %s", request.URL.Path, response.StatusCode, excerpt(body))
	}
}

// servedModels reads the ids of an OpenAI /v1/models document.
func servedModels(body []byte) []string {
	var document struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &document) != nil {
		return nil
	}
	ids := make([]string, 0, len(document.Data))
	for _, model := range document.Data {
		ids = append(ids, model.ID)
	}
	return ids
}

func (c *checker) model() {
	if c.skipIfBlocked(spec.CheckNameModel) {
		return
	}
	switch {
	case slices.Contains(c.served, c.target.Model):
		c.record(spec.CheckNameModel, spec.CheckStepStatusOk, time.Time{}, "Model %s is served.", c.target.Model)
	case len(c.served) == 0:
		c.record(spec.CheckNameModel, spec.CheckStepStatusFailed, time.Time{}, "The server listed no models, so %s is not served.", c.target.Model)
	default:
		listed := c.served
		suffix := ""
		if len(listed) > maxListedModels {
			listed, suffix = listed[:maxListedModels], fmt.Sprintf(" and %d more", len(c.served)-maxListedModels)
		}
		c.record(spec.CheckNameModel, spec.CheckStepStatusFailed, time.Time{}, "Model %s is not served; the server lists %s%s.", c.target.Model, strings.Join(listed, ", "), suffix)
	}
}

// generate sends one streaming chat request with max_tokens 1, only when asked:
// on a paid API it costs a real request.
func (c *checker) generate(ctx context.Context) {
	if c.request.Generate == nil || !*c.request.Generate {
		c.record(spec.CheckNameGenerate, spec.CheckStepStatusSkipped, time.Time{}, "Not asked for: it sends one real request.")
		return
	}
	if c.skipIfBlocked(spec.CheckNameGenerate) {
		return
	}
	started := time.Now()
	token, _ := c.secrets.bearerToken(c.target.Auth) // auth passed, so this succeeds
	body, _ := json.Marshal(map[string]any{
		"model": c.target.Model, "max_tokens": 1, "stream": true,
		"messages": []map[string]string{{"role": "user", "content": "Reply with one word."}},
	})
	generateCtx, cancel := context.WithTimeout(ctx, checkGenerateTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(generateCtx, http.MethodPost, c.url(workload.PathChat), bytes.NewReader(body))
	if err != nil {
		c.record(spec.CheckNameGenerate, spec.CheckStepStatusFailed, started, "Cannot build the request: %v.", err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := *c.client
	client.Timeout = checkGenerateTimeout
	response, err := client.Do(request)
	if err != nil {
		c.record(spec.CheckNameGenerate, spec.CheckStepStatusFailed, started, "POST %s failed: %v.", request.URL.Path, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		failure, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		c.record(spec.CheckNameGenerate, spec.CheckStepStatusFailed, started, "POST %s returned %d: %s", request.URL.Path, response.StatusCode, excerpt(failure))
		return
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			c.record(spec.CheckNameGenerate, spec.CheckStepStatusOk, started, "One streaming request succeeded; the first chunk came after %d ms.", time.Since(started).Milliseconds())
			return
		}
	}
	c.record(spec.CheckNameGenerate, spec.CheckStepStatusFailed, started, "POST %s returned 200 but streamed no data lines.", request.URL.Path)
}

// readWorkload reads a Kubernetes target's pods with the snapshot code a run
// uses; the result is recorded as the workload check.
func (c *checker) readWorkload(ctx context.Context) {
	if c.target.Workload == nil {
		c.workload = &spec.TargetCheck{Name: spec.CheckNameWorkload, Status: spec.CheckStepStatusSkipped, Message: "Not a Kubernetes workload (the target names no target.workload)."}
		return
	}
	started := time.Now()
	finish := func(status spec.CheckStepStatus, format string, args ...any) {
		c.workload = &spec.TargetCheck{Name: spec.CheckNameWorkload, Status: status, Message: fmt.Sprintf(format, args...), DurationSeconds: time.Since(started).Seconds()}
	}
	if c.opts.KubeClient == nil {
		finish(spec.CheckStepStatusWarning, "No Kubernetes access from here, so the workload's pods were not checked.")
		return
	}
	kube, skipReason, err := c.opts.KubeClient()
	if err != nil || kube == nil {
		if err != nil {
			skipReason = err.Error()
		}
		finish(spec.CheckStepStatusWarning, "No Kubernetes access from here (%s), so the workload's pods were not checked.", skipReason)
		return
	}
	client := c.client
	if client == nil {
		client = &http.Client{Transport: unavailableTransport{err: errors.New("the target is not reachable")}}
	}
	authorization, _ := c.secrets.authorization(c.target)
	ref, _, err := target.RefsFromRunSpec(c.target, client, authorization, func(spec.Upstream) (*http.Client, error) {
		return &http.Client{Transport: unavailableTransport{err: errors.New("upstreams are not checked")}}, nil
	})
	if err != nil {
		finish(spec.CheckStepStatusFailed, "Cannot read the workload: %v.", err)
		return
	}
	captured := target.Capture(ctx, target.Request{Phase: spec.SnapshotPhasePre, Kube: kube, Target: ref, Now: c.opts.Now})
	c.snapshots = []*spec.TargetSnapshot{&captured.Snapshot}
	selector := c.target.Workload
	var pods []string
	for _, workload := range captured.Snapshot.Workloads {
		if workload.Role == spec.WorkloadRoleTarget {
			for _, pod := range workload.Pods {
				pods = append(pods, pod.Name)
			}
		}
	}
	if len(pods) == 0 {
		detail := ""
		if len(captured.Problems) > 0 {
			detail = " (" + captured.Problems[0] + ")"
		}
		finish(spec.CheckStepStatusFailed, "The selector matches no pods in namespace %s%s.", selector.Namespace, detail)
		return
	}
	finish(spec.CheckStepStatusOk, "The selector matches %d pod(s) in namespace %s: %s.", len(pods), selector.Namespace, strings.Join(pods, ", "))
}

// prometheusClient builds the client of the request's Prometheus, or says why
// there is none.
func (c *checker) prometheusClient() (*prometheus.Client, string, error) {
	configured := c.request.Prometheus
	if configured == nil {
		return nil, "No Prometheus was given, so whether its series are collected is not checked.", nil
	}
	hop, err := c.secrets.hop("prometheus", configured.Tls, configured.Url)
	if err != nil {
		return nil, "", err
	}
	client, err := prometheus.NewClient(hop, c.opts.PrometheusOptions)
	return client, "", err
}

// probe runs one catalog's marker query against the target's selectors.
func (c *checker) probe(ctx context.Context, client *prometheus.Client, catalog spec.CatalogName, selectors prometheus.Selectors) spec.OptionalCatalogResult {
	probeCtx, cancel := context.WithTimeout(ctx, checkPrometheusTimeout)
	defer cancel()
	results, err := prometheus.ProbeOptionalCatalogs(probeCtx, client, []spec.CatalogName{catalog}, prometheus.RenderInput{
		Selectors: selectors, Interval: prometheus.AssumedScrapeInterval, StageWindow: time.Minute,
	}, c.opts.Now())
	if err != nil {
		detail := err.Error()
		return spec.OptionalCatalogResult{Name: catalog, Result: spec.CatalogResultError, Detail: &detail}
	}
	return results[0]
}

func detailOf(result spec.OptionalCatalogResult) string {
	if result.Detail == nil {
		return ""
	}
	return *result.Detail
}

func (c *checker) selectors() (prometheus.Selectors, error) {
	return targetSelectors(c.target, c.snapshots, func(string) string { return "" }, nil)
}

// metrics checks that Prometheus holds the series of the target type's own
// catalog, by that catalog's marker query.
func (c *checker) metrics(ctx context.Context) {
	started := time.Now()
	catalog := spec.CatalogName(c.target.Type)
	if _, err := prometheus.CatalogFor(catalog); err != nil {
		c.record(spec.CheckNameMetrics, spec.CheckStepStatusSkipped, time.Time{}, "servtuner has no metrics catalog for type %s; only client-side metrics are measured.", c.target.Type)
		return
	}
	client, reason, err := c.prometheusClient()
	switch {
	case err != nil:
		c.record(spec.CheckNameMetrics, spec.CheckStepStatusFailed, started, "Prometheus is not usable: %v.", err)
		return
	case client == nil:
		c.record(spec.CheckNameMetrics, spec.CheckStepStatusSkipped, time.Time{}, "%s", reason)
		return
	}
	selectors, err := c.selectors()
	if err != nil {
		c.record(spec.CheckNameMetrics, spec.CheckStepStatusFailed, started, "Cannot select the target's series: %v.", err)
		return
	}
	result := c.probe(ctx, client, catalog, selectors)
	switch result.Result {
	case spec.CatalogResultCollected:
		c.record(spec.CheckNameMetrics, spec.CheckStepStatusOk, started, "Prometheus has the target's %s series.", catalog)
	case spec.CatalogResultAbsent:
		c.record(spec.CheckNameMetrics, spec.CheckStepStatusFailed, started, "Prometheus has no %s series for this target: is it scraped, with these labels? %s.", catalog, detailOf(result))
	default:
		c.record(spec.CheckNameMetrics, spec.CheckStepStatusFailed, started, "Asking Prometheus failed: %s.", detailOf(result))
	}
}

// gpuMetrics looks for DCGM series of the target's GPUs. Their absence is a
// warning: a run still measures, but its GPU panels and rules are empty.
func (c *checker) gpuMetrics(ctx context.Context) {
	started := time.Now()
	asked := c.request.Prometheus != nil && slices.Contains(c.request.Prometheus.OptionalCatalogs, string(spec.CatalogNameDcgm))
	if c.target.Type != spec.TargetTypeVllm && !asked {
		c.record(spec.CheckNameGpuMetrics, spec.CheckStepStatusSkipped, time.Time{}, "Not a GPU server, and the dcgm catalog is not asked for.")
		return
	}
	client, reason, err := c.prometheusClient()
	switch {
	case err != nil:
		c.record(spec.CheckNameGpuMetrics, spec.CheckStepStatusWarning, started, "Prometheus is not usable: %v.", err)
		return
	case client == nil:
		c.record(spec.CheckNameGpuMetrics, spec.CheckStepStatusSkipped, time.Time{}, "%s", reason)
		return
	}
	selectors, err := c.selectors()
	if err != nil || selectors.NodeRegex == "" {
		c.record(spec.CheckNameGpuMetrics, spec.CheckStepStatusWarning, started, "No GPU host is known: set target.metrics.node for a target outside Kubernetes, or name a workload whose pods run on GPU nodes.")
		return
	}
	result := c.probe(ctx, client, spec.CatalogNameDcgm, selectors)
	switch result.Result {
	case spec.CatalogResultCollected:
		c.record(spec.CheckNameGpuMetrics, spec.CheckStepStatusOk, started, "Prometheus has DCGM series for the target's GPU host.")
	case spec.CatalogResultAbsent:
		c.record(spec.CheckNameGpuMetrics, spec.CheckStepStatusWarning, started, "No DCGM series for the target's GPU host, so GPU panels will be empty. %s.", detailOf(result))
	default:
		c.record(spec.CheckNameGpuMetrics, spec.CheckStepStatusWarning, started, "Asking Prometheus for DCGM series failed: %s.", detailOf(result))
	}
}
