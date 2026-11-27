package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

// targetRequestTimeout bounds each request the snapshot makes to a target.
const targetRequestTimeout = 30 * time.Second

// snapshot captures the target workload before the load (bench/CLAUDE.md,
// "Run pipeline"), records the knobs, and probes the optional Prometheus
// catalogs. A run with no cluster reads the target's own endpoints only, and
// logs why the Kubernetes part was skipped.
func (r *run) snapshot(ctx context.Context) (string, error) {
	if r.opts.KubeClient != nil {
		kube, skipReason, err := r.opts.KubeClient()
		if err != nil {
			return "", fmt.Errorf("kubernetes client: %w", err)
		}
		r.kube, r.kubeSkip = kube, skipReason
	} else {
		r.kubeSkip = target.NoClusterReason
	}
	captured, err := r.capture(ctx, spec.SnapshotPhasePre)
	if err != nil {
		return "", err
	}
	r.preSnapshot = &captured.Snapshot
	r.knobs = captured.Knobs
	if err := r.writeInfraPricing(ctx); err != nil {
		return "", err
	}
	if err := r.writeTokenPricing(ctx, captured.ConfigDocuments[target.BifrostConfigDocument]); err != nil {
		return "", err
	}
	if err := r.probeOptionalCatalogs(ctx); err != nil {
		return "", err
	}
	if err := r.writeEnvironment(ctx); err != nil {
		return "", err
	}
	if r.kube == nil {
		return r.kubeSkip, nil
	}
	return "", nil
}

// capture takes one snapshot and writes target/snapshot-<phase>.json and the
// redacted config documents. What could not be read is logged, never fatal;
// only an unreadable tls block or a failed bundle write fails the step.
func (r *run) capture(ctx context.Context, phase spec.SnapshotPhase) (target.Result, error) {
	targetRef, upstreamRefs, err := r.targetRefs()
	if err != nil {
		return target.Result{}, err
	}
	result := target.Capture(ctx, target.Request{
		Phase: phase, Kube: r.kube, Target: targetRef, Upstreams: upstreamRefs, Now: r.opts.Now,
	})
	if result.KubeSkipReason != "" {
		r.logger.Info("kubernetes part of the snapshot skipped", "phase", phase, "reason", result.KubeSkipReason)
	}
	for _, problem := range result.Problems {
		r.logger.Warn("snapshot could not read everything", "phase", phase, "problem", problem)
	}
	name := bundle.SnapshotPreJSON
	if phase == spec.SnapshotPhasePost {
		name = bundle.SnapshotPostJSON
	}
	if err := r.writer.WriteDocument(ctx, name, spec.SchemaTargetSnapshot, result.Snapshot); err != nil {
		return result, err
	}
	documents := make([]string, 0, len(result.ConfigDocuments))
	for documentName := range result.ConfigDocuments {
		documents = append(documents, documentName)
	}
	sort.Strings(documents)
	for _, documentName := range documents {
		file := bundle.TargetConfigFile(string(phase), documentName)
		if err := r.writer.WriteFile(ctx, file, result.ConfigDocuments[documentName]); err != nil {
			return result, err
		}
	}
	return result, nil
}

// targetRefs builds the workloads to capture. Every upstream's client is built
// from that upstream's own tls block (never the target's), and an unknown mode
// is an error. A hop whose Secret files are not mounted does not fail the
// snapshot: its client fails every request, which lands in the problems.
func (r *run) targetRefs() (target.TargetRef, []target.TargetRef, error) {
	runTarget := r.runSpec.Target
	authorization, err := r.secrets.authorization(runTarget)
	if err != nil {
		if !errors.Is(err, errSecretUnavailable) {
			return target.TargetRef{}, nil, err
		}
		r.logger.Warn("target credentials unavailable; the snapshot asks the target without them", "reason", err)
		authorization = ""
	}
	client, err := r.hopClient("target", runTarget.Tls, runTarget.Endpoint)
	if err != nil {
		return target.TargetRef{}, nil, err
	}
	upstreamClient := func(upstream spec.Upstream) (*http.Client, error) {
		return r.hopClient("upstream "+upstream.Name, upstream.Tls, *upstream.Endpoint)
	}
	return target.RefsFromRunSpec(runTarget, client, authorization, upstreamClient)
}

// hopClient builds the HTTP client of one hop from its declared tls block.
func (r *run) hopClient(name string, tls spec.Tls, url string) (*http.Client, error) {
	hop, err := r.secrets.hop(name, tls, url)
	if errors.Is(err, errSecretUnavailable) {
		return &http.Client{Transport: unavailableTransport{err: err}}, nil
	}
	if err != nil {
		return nil, err
	}
	return tlsconfig.NewHTTPClient(hop, targetRequestTimeout)
}

// unavailableTransport fails every request with the reason the hop has no
// client, so the snapshot records a problem instead of skipping silently.
type unavailableTransport struct{ err error }

func (t unavailableTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// probeOptionalCatalogs checks, before the load, which optional catalogs the
// cluster has. Without a usable Prometheus every optional catalog is recorded
// as an error with the reason.
func (r *run) probeOptionalCatalogs(ctx context.Context) error {
	_, optional, err := r.catalogNames()
	if err != nil || len(optional) == 0 {
		return err
	}
	if r.client == nil || r.prometheusProblem != "" {
		reason := r.prometheusProblem
		if reason == "" {
			reason = "no prometheus section in the RunSpec"
		}
		for _, name := range optional {
			detail := "not probed: " + reason
			r.optionalCatalogs = append(r.optionalCatalogs, spec.OptionalCatalogResult{Name: name, Result: spec.CatalogResultError, Detail: &detail})
		}
		return nil
	}
	selectors, err := r.selectors()
	if err != nil {
		for _, name := range optional {
			detail := "not probed: " + err.Error()
			r.optionalCatalogs = append(r.optionalCatalogs, spec.OptionalCatalogResult{Name: name, Result: spec.CatalogResultError, Detail: &detail})
		}
		return nil
	}
	results, err := prometheus.ProbeOptionalCatalogs(ctx, r.client, optional, prometheus.RenderInput{
		Selectors: selectors, Interval: r.resolution.Interval, StageWindow: time.Minute,
		CadvisorJob: r.resolution.JobOf(spec.JobRoleCadvisor),
	}, r.opts.Now())
	if err != nil {
		return validate.WrapParseError(err)
	}
	r.optionalCatalogs = results
	return nil
}
