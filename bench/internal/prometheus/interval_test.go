package prometheus

import (
	"slices"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func job(name string, role spec.JobRole, seconds float64) spec.ScrapedJob {
	return spec.ScrapedJob{Job: name, Role: role, ScrapeIntervalSeconds: seconds, ScrapeTimeoutSeconds: 1}
}

func TestResolveScrapeIntervalTakesTheSlowestRoleAndTheFastestJobOfARole(t *testing.T) {
	targets := spec.PrometheusTargets{Targets: []spec.ScrapedJob{
		job("servtuner-targets", spec.JobRoleTarget, 5),
		job("kubernetes-nodes-cadvisor", spec.JobRoleCadvisor, 60),
		job("servtuner-cadvisor", spec.JobRoleCadvisor, 10),
		job("dcgm", spec.JobRoleDcgm, 30),
		job("kube-state-metrics", spec.JobRoleKubeStateMetrics, 120),
	}}
	roles := []spec.JobRole{spec.JobRoleTarget, spec.JobRoleCadvisor}
	got := ResolveScrapeInterval(targets, roles)
	if got.Interval != 10*time.Second || got.Job != "servtuner-cadvisor" || got.Source != spec.ScrapeIntervalSourceDiscovered {
		t.Errorf("without dcgm: %+v", got.ScrapeInterval)
	}
	if len(got.MissingRoles) != 0 {
		t.Errorf("missing roles = %v", got.MissingRoles)
	}

	withDCGM := ResolveScrapeInterval(targets, append(roles, spec.JobRoleDcgm))
	if withDCGM.Interval != 30*time.Second || withDCGM.Job != "dcgm" {
		t.Errorf("with dcgm requested: %+v", withDCGM.ScrapeInterval)
	}
	if withDCGM.For(spec.JobRoleTarget).Interval != 5*time.Second || withDCGM.For(spec.JobRoleCadvisor).Interval != 10*time.Second {
		t.Errorf("per role = %+v", withDCGM.PerRole)
	}
	if got.JobOf(spec.JobRoleCadvisor) != "servtuner-cadvisor" {
		t.Errorf("cadvisor job = %q, want the fastest one", got.JobOf(spec.JobRoleCadvisor))
	}
}

func TestJobOfNeverBorrowsAnotherRolesJob(t *testing.T) {
	targets := spec.PrometheusTargets{Targets: []spec.ScrapedJob{job("servtuner-targets", spec.JobRoleTarget, 5)}}
	got := ResolveScrapeInterval(targets, []spec.JobRole{spec.JobRoleTarget, spec.JobRoleCadvisor})
	if job := got.JobOf(spec.JobRoleCadvisor); job != "" {
		t.Errorf("cadvisor job = %q, want empty: the target job does not scrape cAdvisor", job)
	}
}

func TestResolveScrapeIntervalReportsMissingRoles(t *testing.T) {
	targets := spec.PrometheusTargets{Targets: []spec.ScrapedJob{job("servtuner-targets", spec.JobRoleTarget, 5)}}
	got := ResolveScrapeInterval(targets, []spec.JobRole{spec.JobRoleTarget, spec.JobRoleCadvisor})
	if got.Interval != 5*time.Second || len(got.MissingRoles) != 1 || got.MissingRoles[0] != spec.JobRoleCadvisor {
		t.Errorf("resolution = %+v", got)
	}
	// A role with no job falls back to the slowest relevant interval.
	if got.For(spec.JobRoleCadvisor).Interval != 5*time.Second {
		t.Errorf("fallback = %+v", got.For(spec.JobRoleCadvisor))
	}
}

func TestResolveScrapeIntervalAssumesWhenNothingRelevantIsScraped(t *testing.T) {
	targets := spec.PrometheusTargets{Targets: []spec.ScrapedJob{job("prometheus", spec.JobRoleOther, 15)}}
	got := ResolveScrapeInterval(targets, []spec.JobRole{spec.JobRoleTarget})
	if got.ScrapeInterval != AssumedInterval || got.Source != spec.ScrapeIntervalSourceAssumed || got.Job != "" {
		t.Errorf("resolution = %+v", got)
	}
	if AssumedInterval.Interval != 5*time.Second {
		t.Errorf("assumed interval = %s", AssumedInterval.Interval)
	}
}

func TestRelevantRoles(t *testing.T) {
	roles, err := RelevantRoles(true)
	if err != nil || len(roles) != 2 {
		t.Fatalf("roles = %v, err = %v", roles, err)
	}
	roles, err = RelevantRoles(true, spec.CatalogNameVllm, spec.CatalogNameDcgm, spec.CatalogNameKubernetes)
	if err != nil || len(roles) != 3 || roles[2] != spec.JobRoleDcgm {
		t.Errorf("roles = %v, err = %v", roles, err)
	}
	// Off the cluster cAdvisor has none of the target's series, so it never
	// limits the run, even with the kubernetes catalog requested.
	roles, err = RelevantRoles(false, spec.CatalogNameVllm, spec.CatalogNameKubernetes)
	if err != nil || !slices.Equal(roles, []spec.JobRole{spec.JobRoleTarget}) {
		t.Errorf("off-cluster roles = %v, err = %v", roles, err)
	}
	if _, err := RelevantRoles(true, spec.CatalogNameExtra); err == nil {
		t.Error("extra is not a catalog")
	}
}

func TestKubeStateIsFoundButDoesNotLimitTheRun(t *testing.T) {
	targets := spec.PrometheusTargets{Targets: []spec.ScrapedJob{
		job("servtuner-targets", spec.JobRoleTarget, 5),
		job("servtuner-cadvisor", spec.JobRoleCadvisor, 10),
		job("kube-state-metrics", spec.JobRoleKubeStateMetrics, 30),
	}}
	got := ResolveScrapeInterval(targets, []spec.JobRole{spec.JobRoleTarget, spec.JobRoleCadvisor})
	if got.Interval != 10*time.Second {
		t.Errorf("kube-state-metrics must not set the run interval: %+v", got.ScrapeInterval)
	}
	if got.For(spec.JobRoleKubeStateMetrics).Interval != 30*time.Second {
		t.Errorf("per role = %+v", got.PerRole)
	}
}

func TestInKubernetes(t *testing.T) {
	if InKubernetes(spec.Target{}) {
		t.Error("a target with no workload anywhere is off the cluster")
	}
	if !InKubernetes(spec.Target{Workload: &spec.WorkloadSelector{Namespace: "llm"}}) {
		t.Error("a target workload is in Kubernetes")
	}
	if !InKubernetes(spec.Target{Upstreams: []spec.Upstream{{Workload: &spec.WorkloadSelector{Namespace: "llm"}}}}) {
		t.Error("an upstream workload puts the system under test in Kubernetes")
	}
}
