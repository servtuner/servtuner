package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/common/model"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Job names that map to a role. Defined once:
// the catalogs select TargetJob, and the role mapping below recognises it.
const (
	// TargetJob is the dedicated 5s job for targets under test. Operator
	// setups name their monitors after it too (a pool such as
	// "serviceMonitor/<namespace>/servtuner-targets-<type>/0").
	TargetJob = "servtuner-targets"
	// ExternalJob scrapes targets outside the cluster, whose static targets
	// carry namespace and pod labels (deploy/prometheus/servtuner-external.values.yaml,
	// RunSpec target.metrics). It is a target job like TargetJob.
	ExternalJob = "servtuner-external"

	cadvisorMetricsPath = "/metrics/cadvisor"
	dcgmJobMarker       = "dcgm"
	kubeStateJobMarker  = "kube-state-metrics"
)

// activeTarget is the part of /api/v1/targets that discovery reads.
// client_golang's ActiveTarget has no scrape interval, so the endpoint is read
// directly. Prometheus reports scrapeInterval and scrapeTimeout for every
// active target as duration strings such as "5s".
type activeTarget struct {
	ScrapePool     string            `json:"scrapePool"`
	ScrapeURL      string            `json:"scrapeUrl"`
	Labels         map[string]string `json:"labels"`
	ScrapeInterval string            `json:"scrapeInterval"`
	ScrapeTimeout  string            `json:"scrapeTimeout"`
}

type targetsEnvelope struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ActiveTargets []activeTarget `json:"activeTargets"`
	} `json:"data"`
}

// DiscoverTargets reads the effective scrape interval and timeout of every
// scraped job ("Discover, don't assume"). The result is the document
// prometheus/targets.json.
func (c *Client) DiscoverTargets(ctx context.Context) (spec.PrometheusTargets, error) {
	body, err := c.doGet(ctx, "/api/v1/targets", map[string]string{"state": "active"})
	if err != nil {
		return spec.PrometheusTargets{}, fmt.Errorf("discover scrape targets: %w", err)
	}
	var envelope targetsEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return spec.PrometheusTargets{}, fmt.Errorf("discover scrape targets: decode response: %w", err)
	}
	if envelope.Status != "success" {
		return spec.PrometheusTargets{}, fmt.Errorf("discover scrape targets: prometheus answered %s: %s", envelope.ErrorType, envelope.Error)
	}
	jobs, err := jobsOf(envelope.Data.ActiveTargets)
	if err != nil {
		return spec.PrometheusTargets{}, fmt.Errorf("discover scrape targets: %w", err)
	}
	return spec.PrometheusTargets{FetchedAt: spec.NewUTCTimestamp(c.options.Now()), Targets: jobs}, nil
}

// jobsOf folds the active targets into one entry per scrape pool, named by the
// job label its series carry (the pool's name when a target has none). Several
// pools can share a job label, as the Operator's kubelet pools do (/metrics and
// /metrics/cadvisor at different intervals); each keeps its own role and
// interval. Within a pool, if targets disagree the slowest interval wins,
// because the slowest limits resolution. Sorted by job, then pool, so the
// document is stable.
func jobsOf(targets []activeTarget) ([]spec.ScrapedJob, error) {
	byPool := map[string]spec.ScrapedJob{}
	for _, target := range targets {
		interval, err := parseScrapeDuration(target, "scrapeInterval", target.ScrapeInterval)
		if err != nil {
			return nil, err
		}
		timeout, err := parseScrapeDuration(target, "scrapeTimeout", target.ScrapeTimeout)
		if err != nil {
			return nil, err
		}
		// The job label is what the series carry and the queries select; under the Prometheus
		// Operator it differs from the pool ("serviceMonitor/monitoring/kps-kubelet/1" is
		// job="kubelet"), so keying by pool selected nothing there.
		name := target.Labels["job"]
		if name == "" {
			name = target.ScrapePool
		}
		job := spec.ScrapedJob{
			Job:                   name,
			Role:                  RoleOf(target.ScrapePool, target.Labels["job"], target.ScrapeURL),
			ScrapeIntervalSeconds: interval.Seconds(),
			ScrapeTimeoutSeconds:  timeout.Seconds(),
		}
		if known, seen := byPool[target.ScrapePool]; seen {
			job.ScrapeIntervalSeconds = max(job.ScrapeIntervalSeconds, known.ScrapeIntervalSeconds)
			job.ScrapeTimeoutSeconds = max(job.ScrapeTimeoutSeconds, known.ScrapeTimeoutSeconds)
			if known.Role != spec.JobRoleOther {
				job.Role = known.Role
			}
		}
		byPool[target.ScrapePool] = job
	}
	pools := slices.Collect(maps.Keys(byPool))
	slices.SortFunc(pools, func(a, b string) int {
		if byJob := strings.Compare(byPool[a].Job, byPool[b].Job); byJob != 0 {
			return byJob
		}
		return strings.Compare(a, b)
	})
	jobs := make([]spec.ScrapedJob, 0, len(pools))
	for _, pool := range pools {
		jobs = append(jobs, byPool[pool])
	}
	return jobs, nil
}

func parseScrapeDuration(target activeTarget, field, value string) (time.Duration, error) {
	if value == "" {
		return 0, fmt.Errorf("pool %q does not report %s: Prometheus is too old to expose scrape settings in /api/v1/targets", target.ScrapePool, field)
	}
	parsed, err := model.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("pool %q: parse %s %q: %w", target.ScrapePool, field, value, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("pool %q: %s %q must be positive", target.ScrapePool, field, value)
	}
	return time.Duration(parsed), nil
}

// RoleOf maps a scrape pool to the role the stage-length rules care about.
// The rules apply in this order, and the first
// match wins:
//
//  1. a scrape URL path ending in /metrics/cadvisor is cadvisor, whatever the
//     pool is called (kube-prometheus-stack names it after the kubelet);
//  2. a pool or job name containing "servtuner-targets" or "servtuner-external" is
//     target;
//  3. one containing "dcgm" is dcgm;
//  4. one containing "kube-state-metrics" is kube_state_metrics;
//  5. everything else is other.
func RoleOf(scrapePool, jobLabel, scrapeURL string) spec.JobRole {
	if parsed, err := url.Parse(scrapeURL); err == nil && strings.HasSuffix(parsed.Path, cadvisorMetricsPath) {
		return spec.JobRoleCadvisor
	}
	names := strings.ToLower(scrapePool + " " + jobLabel)
	switch {
	case strings.Contains(names, TargetJob), strings.Contains(names, ExternalJob):
		return spec.JobRoleTarget
	case strings.Contains(names, dcgmJobMarker):
		return spec.JobRoleDcgm
	case strings.Contains(names, kubeStateJobMarker):
		return spec.JobRoleKubeStateMetrics
	default:
		return spec.JobRoleOther
	}
}
