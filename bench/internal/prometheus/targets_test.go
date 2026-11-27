package prometheus

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/prometheus/prometheustest"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// clusterTargets mixes the two supported setups: the community chart's
// dedicated jobs, and kube-prometheus-stack's per-endpoint pools.
func clusterTargets() []prometheustest.Target {
	return []prometheustest.Target{
		{Pool: "servtuner-targets", Job: "servtuner-targets", URL: "http://192.0.2.10:8000/metrics", Interval: "5s", Timeout: "4s"},
		{Pool: "servtuner-targets", Job: "servtuner-targets", URL: "http://192.0.2.11:8000/metrics", Interval: "5s", Timeout: "4s"},
		{Pool: "kubernetes-nodes-cadvisor", Job: "kubernetes-nodes-cadvisor", URL: "https://192.0.2.1:443/api/v1/nodes/n1/proxy/metrics/cadvisor", Interval: "1m", Timeout: "10s"},
		{Pool: "servtuner-cadvisor", Job: "servtuner-cadvisor", URL: "https://192.0.2.1:443/api/v1/nodes/n1/proxy/metrics/cadvisor", Interval: "10s", Timeout: "10s"},
		{Pool: "serviceMonitor/monitoring/stack-kubelet/1", Job: "kubelet", URL: "https://192.0.2.20:10250/metrics/cadvisor", Interval: "10s", Timeout: "10s"},
		{Pool: "serviceMonitor/monitoring/stack-kubelet/0", Job: "kubelet", URL: "https://192.0.2.20:10250/metrics", Interval: "30s", Timeout: "10s"},
		{Pool: "serviceMonitor/gpu/nvidia-dcgm-exporter/0", Job: "nvidia-dcgm-exporter", URL: "http://192.0.2.30:9400/metrics", Interval: "15s", Timeout: "10s"},
		{Pool: "kube-state-metrics", Job: "kube-state-metrics", URL: "http://192.0.2.40:8080/metrics", Interval: "30s", Timeout: "10s"},
		{Pool: "kubernetes-pods", Job: "kubernetes-pods", URL: "http://192.0.2.10:8000/metrics", Interval: "1m", Timeout: "10s"},
	}
}

func TestDiscoverTargetsFoldsPoolsAndAssignsRoles(t *testing.T) {
	fake := &prometheustest.Server{Targets: clusterTargets()}
	client := newPlatformClient(t, fake, nil)
	fixedNow := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	client.options.Now = func() time.Time { return fixedNow }

	discovered, err := client.DiscoverTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if discovered.FetchedAt != "2026-10-28T12:00:00Z" {
		t.Errorf("fetched_at = %s", discovered.FetchedAt)
	}
	want := []spec.ScrapedJob{
		{Job: "kube-state-metrics", Role: spec.JobRoleKubeStateMetrics, ScrapeIntervalSeconds: 30, ScrapeTimeoutSeconds: 10},
		// The Operator's pools are named by their job label, which their series carry (and the
		// queries select); each pool keeps its own role and interval.
		{Job: "kubelet", Role: spec.JobRoleOther, ScrapeIntervalSeconds: 30, ScrapeTimeoutSeconds: 10},
		{Job: "kubelet", Role: spec.JobRoleCadvisor, ScrapeIntervalSeconds: 10, ScrapeTimeoutSeconds: 10},
		{Job: "kubernetes-nodes-cadvisor", Role: spec.JobRoleCadvisor, ScrapeIntervalSeconds: 60, ScrapeTimeoutSeconds: 10},
		{Job: "kubernetes-pods", Role: spec.JobRoleOther, ScrapeIntervalSeconds: 60, ScrapeTimeoutSeconds: 10},
		{Job: "nvidia-dcgm-exporter", Role: spec.JobRoleDcgm, ScrapeIntervalSeconds: 15, ScrapeTimeoutSeconds: 10},
		{Job: "servtuner-cadvisor", Role: spec.JobRoleCadvisor, ScrapeIntervalSeconds: 10, ScrapeTimeoutSeconds: 10},
		{Job: "servtuner-targets", Role: spec.JobRoleTarget, ScrapeIntervalSeconds: 5, ScrapeTimeoutSeconds: 4},
	}
	if len(discovered.Targets) != len(want) {
		t.Fatalf("targets = %+v", discovered.Targets)
	}
	for i := range want {
		if discovered.Targets[i] != want[i] {
			t.Errorf("target %d = %+v, want %+v", i, discovered.Targets[i], want[i])
		}
	}
}

func TestDiscoveredTargetsValidateAgainstTheSchema(t *testing.T) {
	client := newPlatformClient(t, &prometheustest.Server{Targets: clusterTargets()}, nil)
	discovered, err := client.DiscoverTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaPrometheusTargets, mustMarshal(t, discovered)); err != nil {
		t.Errorf("targets.json is invalid: %v", err)
	}
}

func TestDiscoverTargetsOverMTLS(t *testing.T) {
	client := newMTLSClient(t, &prometheustest.Server{Targets: clusterTargets()})
	if _, err := client.DiscoverTargets(context.Background()); err != nil {
		t.Fatalf("mtls discovery failed: %v", err)
	}
}

func TestDiscoverTargetsRetriesServerErrors(t *testing.T) {
	fake := &prometheustest.Server{Targets: clusterTargets()}
	fake.FailNext(http.StatusServiceUnavailable)
	client := newPlatformClient(t, fake, nil)
	if _, err := client.DiscoverTargets(context.Background()); err != nil {
		t.Fatalf("discovery should survive a 503: %v", err)
	}
	if got := len(fake.Requests()); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

func TestDiscoverTargetsNeedsScrapeSettings(t *testing.T) {
	fake := &prometheustest.Server{Targets: []prometheustest.Target{{Pool: "old", Job: "old", URL: "http://192.0.2.9/metrics"}}}
	client := newPlatformClient(t, fake, nil)
	_, err := client.DiscoverTargets(context.Background())
	if err == nil || !strings.Contains(err.Error(), "too old") {
		t.Errorf("err = %v", err)
	}
}

func TestJobsOfTakesTheSlowestIntervalOfAPool(t *testing.T) {
	jobs, err := jobsOf([]activeTarget{
		{ScrapePool: "servtuner-targets", ScrapeInterval: "5s", ScrapeTimeout: "4s"},
		{ScrapePool: "servtuner-targets", ScrapeInterval: "10s", ScrapeTimeout: "5s"},
	})
	if err != nil || len(jobs) != 1 || jobs[0].ScrapeIntervalSeconds != 10 || jobs[0].ScrapeTimeoutSeconds != 5 {
		t.Errorf("jobs = %+v, err = %v", jobs, err)
	}
	if _, err := jobsOf([]activeTarget{{ScrapePool: "bad", ScrapeInterval: "soon", ScrapeTimeout: "1s"}}); err == nil {
		t.Error("an unparseable interval must fail")
	}
}

func TestRoleOf(t *testing.T) {
	cases := []struct {
		pool, job, url string
		want           spec.JobRole
	}{
		{"servtuner-targets", "servtuner-targets", "http://192.0.2.1/metrics", spec.JobRoleTarget},
		{"serviceMonitor/llm/servtuner-targets-litellm/0", "litellm", "http://192.0.2.1/metrics", spec.JobRoleTarget},
		{"kubelet", "kubelet", "https://192.0.2.1:10250/metrics/cadvisor", spec.JobRoleCadvisor},
		{"anything", "anything", "https://192.0.2.1/api/v1/nodes/n/proxy/metrics/cadvisor", spec.JobRoleCadvisor},
		{"dcgm-exporter", "dcgm-exporter", "http://192.0.2.1:9400/metrics", spec.JobRoleDcgm},
		{"kube-state-metrics", "kube-state-metrics", "http://192.0.2.1/metrics", spec.JobRoleKubeStateMetrics},
		{"kubelet", "kubelet", "https://192.0.2.1:10250/metrics", spec.JobRoleOther},
		{"prometheus", "prometheus", "http://192.0.2.1:9090/metrics", spec.JobRoleOther},
	}
	for _, c := range cases {
		if got := RoleOf(c.pool, c.job, c.url); got != c.want {
			t.Errorf("RoleOf(%q, %q, %q) = %s, want %s", c.pool, c.job, c.url, got, c.want)
		}
	}
}

// TestJobsAreKeyedByTheJobLabel: under the Prometheus Operator a pool's name differs from the job
// label its series carry; queries select the label, so the label is the job (found on kind, where
// keying by pool left every kubelet query empty).
func TestJobsAreKeyedByTheJobLabel(t *testing.T) {
	jobs, err := jobsOf([]activeTarget{
		{ScrapePool: "serviceMonitor/monitoring/kps-kubelet/0", Labels: map[string]string{"job": "kubelet"},
			ScrapeURL: "https://192.0.2.2:10250/metrics", ScrapeInterval: "30s", ScrapeTimeout: "10s"},
		{ScrapePool: "serviceMonitor/monitoring/kps-kubelet/1", Labels: map[string]string{"job": "kubelet"},
			ScrapeURL: "https://192.0.2.2:10250/metrics/cadvisor", ScrapeInterval: "15s", ScrapeTimeout: "10s"},
		{ScrapePool: "servtuner-targets", ScrapeInterval: "5s", ScrapeTimeout: "4s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []spec.ScrapedJob{
		{Job: "kubelet", Role: spec.JobRoleOther, ScrapeIntervalSeconds: 30, ScrapeTimeoutSeconds: 10},
		{Job: "kubelet", Role: spec.JobRoleCadvisor, ScrapeIntervalSeconds: 15, ScrapeTimeoutSeconds: 10},
		{Job: "servtuner-targets", Role: spec.JobRoleTarget, ScrapeIntervalSeconds: 5, ScrapeTimeoutSeconds: 4}, // no label: the pool
	}
	if !slices.Equal(jobs, want) {
		t.Errorf("jobs = %+v, want %+v", jobs, want)
	}
}
