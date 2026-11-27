package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// assumedScrapeTimeout is the scrape timeout of the servtuner-targets job,
// used when the jobs were not discovered.
const assumedScrapeTimeout = 4 * time.Second

// collect waits for the last samples to land, then queries Prometheus over the
// run window and stores the exact queries and the raw series. A missing or
// unreachable Prometheus is recorded and never fails the run.
func (r *run) collect(ctx context.Context) (string, error) {
	if r.runSpec.Prometheus == nil {
		return "the RunSpec has no prometheus section", nil
	}
	if len(r.windows) == 0 {
		return "no phase ran, so there is no window to query", nil
	}
	if r.client != nil && r.prometheusProblem != "" {
		// It was unreachable at the start; try once more now.
		roles, err := r.relevantRoles()
		if err != nil {
			return "", err
		}
		if err := r.readTargets(ctx, roles); err != nil {
			return "", err
		}
	}
	if r.client == nil || r.prometheusProblem != "" {
		return r.recordUnavailable(ctx)
	}
	selectors, err := r.selectors()
	if err != nil {
		reason := "collection skipped: " + err.Error()
		r.logger.Warn("prometheus collection skipped", "reason", reason)
		return r.writeQueries(ctx, spec.PrometheusQueries{Queries: []spec.Query{}, OptionalCatalogs: r.optionalOrEmpty(reason)}, reason)
	}
	if err := r.settle(ctx); err != nil {
		return "", err
	}

	required, _, err := r.catalogNames()
	if err != nil {
		return "", err
	}
	runWindow := prometheus.Window{Start: r.windows[0].Start, End: r.windows[len(r.windows)-1].End}
	request := prometheus.CollectRequest{
		Window: runWindow, StageWindow: r.shortestStage(), Intervals: r.resolution,
		Selectors: selectors, Required: required, Optional: r.optionalCatalogs,
		ExtraQueries: r.runSpec.Prometheus.ExtraQueries,
	}
	if r.runSpec.Prometheus.Step != "" {
		if request.StepOverride, err = r.runSpec.Prometheus.Step.Std(); err != nil {
			return "", fmt.Errorf("prometheus.step: %w", err)
		}
	}
	collection, collectErr := prometheus.Collect(ctx, r.client, request)
	// Store what was gathered even when collection stopped early.
	storeErr := r.storeCollection(ctx, collection)
	if collectErr != nil {
		collectErr = fmt.Errorf("collect prometheus series: %w", collectErr)
	}
	return "", errors.Join(collectErr, storeErr)
}

func (r *run) relevantRoles() ([]spec.JobRole, error) {
	required, optional, err := r.catalogNames()
	if err != nil {
		return nil, err
	}
	return prometheus.RelevantRoles(prometheus.InKubernetes(r.runSpec.Target), append(required, optional...)...)
}

// recordUnavailable writes queries.json with no queries and every optional
// catalog marked as an error carrying the reason.
func (r *run) recordUnavailable(ctx context.Context) (string, error) {
	reason := r.prometheusProblem
	if reason == "" {
		reason = "prometheus client unavailable"
	}
	r.logger.Warn("prometheus collection skipped", "reason", reason)
	document := spec.PrometheusQueries{Queries: []spec.Query{}, OptionalCatalogs: r.optionalOrEmpty("not collected: " + reason)}
	return r.writeQueries(ctx, document, reason)
}

// optionalOrEmpty is the probed optional catalogs, or when none were probed,
// every configured one marked as an error with detail.
func (r *run) optionalOrEmpty(detail string) []spec.OptionalCatalogResult {
	if len(r.optionalCatalogs) > 0 {
		return r.optionalCatalogs
	}
	_, optional, _ := r.catalogNames()
	results := []spec.OptionalCatalogResult{}
	for _, name := range optional {
		text := detail
		results = append(results, spec.OptionalCatalogResult{Name: name, Result: spec.CatalogResultError, Detail: &text})
	}
	return results
}

func (r *run) writeQueries(ctx context.Context, document spec.PrometheusQueries, skipReason string) (string, error) {
	return skipReason, r.writer.WriteDocument(ctx, bundle.PrometheusQueriesJSON, spec.SchemaPrometheusQueries, document)
}

// storeCollection writes queries.json and each series as the range query's
// data object.
func (r *run) storeCollection(ctx context.Context, collection prometheus.Collection) error {
	if len(collection.Document.Queries) == 0 && collection.Document.OptionalCatalogs == nil {
		return nil
	}
	if err := r.writer.WriteDocument(ctx, bundle.PrometheusQueriesJSON, spec.SchemaPrometheusQueries, collection.Document); err != nil {
		return err
	}
	for _, series := range collection.Series {
		if err := r.writer.WriteFile(ctx, bundle.PrometheusSeriesFile(string(series.Catalog), string(series.Name)), series.Raw); err != nil {
			return err
		}
	}
	return nil
}

// settleDelay is how long after the last stage the final samples need to reach
// Prometheus: twice the slowest relevant scrape interval plus the scrape
// timeout.
func (r *run) settleDelay() time.Duration {
	return 2*r.resolution.Interval + r.scrapeTimeout()
}

// scrapeTimeout is the largest timeout among the discovered jobs of the roles
// that set the resolution, or the assumed one.
func (r *run) scrapeTimeout() time.Duration {
	if r.targets == nil || r.resolution.Source != spec.ScrapeIntervalSourceDiscovered {
		return assumedScrapeTimeout
	}
	var longest time.Duration
	roles, err := r.relevantRoles()
	if err != nil {
		return assumedScrapeTimeout
	}
	for _, job := range r.targets.Targets {
		for _, role := range roles {
			if job.Role == role {
				longest = max(longest, time.Duration(job.ScrapeTimeoutSeconds*float64(time.Second)))
			}
		}
	}
	if longest == 0 {
		return assumedScrapeTimeout
	}
	return longest
}

// settle sleeps until the settle delay has passed since the load ended.
func (r *run) settle(ctx context.Context) error {
	until := r.windows[len(r.windows)-1].End.Add(r.settleDelay())
	if wait := until.Sub(r.opts.Now()); wait > 0 {
		r.logger.Info("waiting for the last samples to be scraped", "wait", wait.String())
		return r.opts.Sleep(ctx, wait)
	}
	return nil
}

// shortestStage is the duration of the shortest stage that ran, or of the
// shortest planned one when none did.
func (r *run) shortestStage() time.Duration {
	var shortest time.Duration
	for _, window := range r.windows {
		if window.Phase.Kind != spec.PhaseKindStage {
			continue
		}
		duration := window.End.Sub(window.Start)
		if shortest == 0 || duration < shortest {
			shortest = duration
		}
	}
	return max(shortest, time.Second)
}

// selectors say which pods the queries cover: the target and upstream pods seen
// in the snapshots, plus the bench pod, in their namespaces. Without any target
// pod (no cluster) there is nothing to select.
func (r *run) selectors() (prometheus.Selectors, error) {
	return targetSelectors(r.runSpec.Target, []*spec.TargetSnapshot{r.preSnapshot, r.postSnapshot}, r.opts.Getenv, r.shardPods)
}

// targetSelectors are the Prometheus selectors of a target: its workload's
// namespace and the snapshot's pods and nodes, or the labels target.metrics
// names for a target outside Kubernetes, plus the bench pods (from POD_NAME,
// POD_NAMESPACE and the shards) so client_bound can read bench's own CPU, even
// when no target pod is known.
// getenv must not be nil; servtuner check-target passes one that returns "".
func targetSelectors(runTarget spec.Target, snapshots []*spec.TargetSnapshot, getenv func(string) string, shardPods []string) (prometheus.Selectors, error) {
	namespaces := map[string]bool{}
	pods := map[string]bool{}
	nodes := map[string]bool{}
	if workload := runTarget.Workload; workload != nil {
		namespaces[workload.Namespace] = true
	}
	for _, upstream := range runTarget.Upstreams {
		if upstream.Workload != nil {
			namespaces[upstream.Workload.Namespace] = true
		}
	}
	for _, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		for _, workload := range snapshot.Workloads {
			for _, pod := range workload.Pods {
				pods[pod.Name] = true
				if pod.Node != nil {
					nodes[*pod.Node] = true
				}
			}
		}
	}
	job := ""
	if metrics := runTarget.Metrics; metrics != nil {
		// A target outside Kubernetes: its scrape job labels its series with a
		// namespace and pod of its own (RunSpec target.metrics).
		namespaces[metrics.Namespace] = true
		pods[metrics.Pod] = true
		job = metrics.Job
		if metrics.Node != nil {
			// Its GPU exporter's host label (the dcgm catalog's node).
			nodes[*metrics.Node] = true
		}
	}
	// Bench's own pods: without target pods (a target outside the cluster with no target.metrics)
	// they are still worth selecting, since client_bound reads bench's CPU.
	if namespace := getenv("POD_NAMESPACE"); namespace != "" {
		namespaces[namespace] = true
	}
	if pod := getenv("POD_NAME"); pod != "" {
		pods[pod] = true
	}
	for _, pod := range shardPods {
		pods[pod] = true
	}
	if len(pods) == 0 {
		return prometheus.Selectors{}, errors.New("no target pods are known (no cluster snapshot and no target.metrics) and bench runs outside a pod, so the queries cannot select any")
	}
	if len(namespaces) == 0 {
		return prometheus.Selectors{}, errors.New("no namespace is known: the RunSpec names no workload and POD_NAMESPACE is unset")
	}
	return prometheus.Selectors{
		Job:            job,
		NamespaceRegex: prometheus.AnyOf(sortedKeys(namespaces)...),
		PodRegex:       prometheus.AnyOf(sortedKeys(pods)...),
		NodeRegex:      prometheus.AnyOf(sortedKeys(nodes)...),
	}, nil
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
