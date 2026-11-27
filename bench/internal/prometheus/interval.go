package prometheus

import (
	"fmt"
	"slices"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// AssumedScrapeInterval is the interval used when Prometheus cannot be asked:
// the 5s of the servtuner-targets job.
const AssumedScrapeInterval = 5 * time.Second

// ScrapeInterval is the interval a stage-length rule or a query step was
// derived from, and where it came from.
type ScrapeInterval struct {
	Interval time.Duration
	Source   spec.ScrapeIntervalSource
	// Job names the job that sets Interval. It is empty when the interval was
	// assumed.
	Job string
}

// AssumedInterval is the ScrapeInterval for a run with no Prometheus.
var AssumedInterval = ScrapeInterval{Interval: AssumedScrapeInterval, Source: spec.ScrapeIntervalSourceAssumed}

// Resolution is the effective scrape interval of a run.
type Resolution struct {
	// ScrapeInterval is the slowest interval among the relevant roles.
	ScrapeInterval
	// PerRole is the interval of every role found in Prometheus, relevant or
	// not. Only the relevant ones decide ScrapeInterval; the others, such as
	// kube-state-metrics, only set the windows of their own queries.
	PerRole map[spec.JobRole]ScrapeInterval
	// MissingRoles are relevant roles with no job in Prometheus. Their data may
	// be scraped at a slower interval than reported, or not at all.
	MissingRoles []spec.JobRole
}

// For returns the interval of one role, falling back to the slowest relevant
// interval when the role has no job, so a query is never finer than the data.
func (r Resolution) For(role spec.JobRole) ScrapeInterval {
	if perRole, found := r.PerRole[role]; found {
		return perRole
	}
	return r.ScrapeInterval
}

// JobOf names the job discovered for a role, or "" when the role has none.
// Unlike For it never falls back to another role's job.
func (r Resolution) JobOf(role spec.JobRole) string {
	return r.PerRole[role].Job
}

// AssumedResolution is the Resolution when Prometheus cannot be asked.
func AssumedResolution() Resolution {
	return Resolution{ScrapeInterval: AssumedInterval}
}

// ResolveScrapeInterval derives the effective interval from targets.json.
//
//   - Within one role the fastest job wins: the cluster-wide cAdvisor job at 1m
//     and the dedicated servtuner-cadvisor job at 10s are both listed, and servtuner
//     reads the finer data.
//   - Across the relevant roles the slowest wins: the weakest source limits
//     what the run can claim ("I is the slowest relevant interval").
//   - With no job in any relevant role there is nothing to discover, so the
//     assumed interval is returned.
func ResolveScrapeInterval(targets spec.PrometheusTargets, relevantRoles []spec.JobRole) Resolution {
	perRole := map[spec.JobRole]ScrapeInterval{}
	for _, job := range targets.Targets {
		if job.Role == spec.JobRoleOther {
			continue
		}
		candidate := ScrapeInterval{
			Interval: time.Duration(job.ScrapeIntervalSeconds * float64(time.Second)),
			Source:   spec.ScrapeIntervalSourceDiscovered,
			Job:      job.Job,
		}
		if known, seen := perRole[job.Role]; !seen || candidate.Interval < known.Interval {
			perRole[job.Role] = candidate
		}
	}
	resolution := Resolution{PerRole: perRole}
	foundRelevant := false
	for _, role := range relevantRoles {
		perRoleInterval, found := perRole[role]
		if !found {
			resolution.MissingRoles = append(resolution.MissingRoles, role)
			continue
		}
		foundRelevant = true
		if perRoleInterval.Interval > resolution.Interval {
			resolution.ScrapeInterval = perRoleInterval
		}
	}
	if !foundRelevant {
		return Resolution{ScrapeInterval: AssumedInterval, PerRole: perRole, MissingRoles: resolution.MissingRoles}
	}
	return resolution
}

// RelevantRoles lists the roles whose interval limits a run: the targets under
// test always, cAdvisor when the target runs in Kubernetes, plus the role of
// every other catalog the run asks for (dcgm when it is requested). A DCGM job
// on the cluster does not limit a run that does not use it, and cAdvisor does
// not limit a run against a target outside the cluster: it holds none of the
// target's series, only the bench pod's CPU for client_bound.
func RelevantRoles(inKubernetes bool, requested ...spec.CatalogName) ([]spec.JobRole, error) {
	roles := []spec.JobRole{spec.JobRoleTarget}
	if inKubernetes {
		roles = append(roles, spec.JobRoleCadvisor)
	}
	for _, name := range requested {
		catalog, err := CatalogFor(name)
		if err != nil {
			return nil, fmt.Errorf("relevant roles: %w", err)
		}
		if catalog.JobRole == spec.JobRoleCadvisor && !inKubernetes {
			continue
		}
		if !slices.Contains(roles, catalog.JobRole) {
			roles = append(roles, catalog.JobRole)
		}
	}
	return roles, nil
}

// InKubernetes reports whether the target or any upstream is a Kubernetes
// workload, so that cAdvisor holds series of the system under test.
func InKubernetes(target spec.Target) bool {
	if target.Workload != nil {
		return true
	}
	for _, upstream := range target.Upstreams {
		if upstream.Workload != nil {
			return true
		}
	}
	return false
}
