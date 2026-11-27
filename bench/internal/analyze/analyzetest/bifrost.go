package analyzetest

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// The synthetic Bifrost router runs two pods in front of one mock upstream pod.
const (
	bifrostNamespace  = "servtuner-bifrost"
	upstreamNamespace = "servtuner-mock"
	routerContainer   = "bifrost"
	upstreamContainer = "mock"
	// UpstreamPod is the mock upstream's one pod.
	UpstreamPod = "mock-0"
)

// RouterPods are the Bifrost router's pods. The second does a share of the
// first's work (routerSecondPodShare), so a per-pod peak must take the first.
var RouterPods = []string{"bifrost-0", "bifrost-1"}

const routerSecondPodShare = 0.8

// Histogram bounds of the rendered latencies, as Bifrost's own buckets are
// spread: seconds for the upstream, microseconds for the overhead.
var (
	upstreamBounds = []float64{0.05, 0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10}
	overheadBounds = []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000}
)

// BifrostTarget makes a scenario a Bifrost router in
// front of one upstream. Per-stage lists work like Scenario.Signals: a stage
// beyond the end takes the last value. Latencies are spread uniformly over
// ±20% of their mean, so a p95 is about 1.18 × the mean.
type BifrostTarget struct {
	// UpstreamSeconds is the mean upstream attempt time.
	UpstreamSeconds []float64
	// OverheadMicroseconds is Bifrost's mean own overhead; nil leaves the
	// histogram out, as Bifrost v1.5 does.
	OverheadMicroseconds []float64
	// RetriesPerAttempt renders request_retries; nil leaves it out.
	RetriesPerAttempt []float64
	// ActivePeak is the busiest router pod's peak in-flight requests.
	ActivePeak []float64
	// CPUCores, Throttle and WorkingSetBytes are the busiest router pod's
	// usage. WorkingSetGrowth is added per second of each stage, starting
	// again from the stage's base at the next stage.
	CPUCores, Throttle, WorkingSetBytes []float64
	WorkingSetGrowth                    float64
	// CPULimitCores and MemoryLimitBytes are the router container's limits.
	CPULimitCores, MemoryLimitBytes float64
	// Concurrency and BufferSize are the provider's queue knobs.
	Concurrency, BufferSize int
	// Logging is client_config.enable_logging; DropExcess is drop_excess_requests.
	Logging, DropExcess bool
	// DroppedShare is the share of each stage's requests rejected with a full
	// queue, rendered as error_requests_total{error_type="bifrost_dropped"};
	// nil leaves errors unlabelled, as v1.5 does.
	DroppedShare []float64
}

func (b *BifrostTarget) targetDocument() map[string]any {
	return map[string]any{
		"type": "bifrost", "endpoint": "http://bifrost.invalid:8080/v1", "model": "mock/mock-model", "tls": map[string]any{"enabled": false},
		"workload": map[string]any{"namespace": bifrostNamespace, "selector": map[string]any{"app": "bifrost"}},
		"upstreams": []any{map[string]any{
			"name": "mock", "endpoint": "http://mock.invalid:8000/v1", "tls": map[string]any{"enabled": false},
			"workload": map[string]any{"namespace": upstreamNamespace, "selector": map[string]any{"app": "mock"}},
		}},
	}
}

// knobs are what the pipeline reads from Bifrost's endpoints into environment.json.
func (b *BifrostTarget) knobs() spec.EnvironmentKnobs {
	return spec.EnvironmentKnobs{
		"bifrost.concurrency": float64(b.Concurrency),
		"bifrost.buffer_size": float64(b.BufferSize),
	}
}

// configDocuments are the redacted config and providers documents.
func (b *BifrostTarget) configDocuments() (map[string][]byte, error) {
	documents := map[string]any{
		target.BifrostConfigDocument: map[string]any{"client_config": map[string]any{
			"enable_logging": b.Logging, "disable_content_logging": !b.Logging, "drop_excess_requests": b.DropExcess,
		}},
		target.BifrostProvidersDocument: map[string]any{"providers": []any{map[string]any{
			"name":                        "mock",
			"concurrency_and_buffer_size": map[string]any{"concurrency": b.Concurrency, "buffer_size": b.BufferSize},
			"network_config":              map[string]any{"max_retries": 0},
			"send_back_raw_request":       false, "send_back_raw_response": false, "store_raw_request_response": false,
		}}},
	}
	files := map[string][]byte{}
	for name, document := range documents {
		encoded, err := json.Marshal(document)
		if err != nil {
			return nil, err
		}
		files[bundle.TargetConfigFile("pre", name)] = encoded
	}
	return files, nil
}

// BifrostSnapshot returns the router and its upstream, every pod ready.
func BifrostSnapshot(b *BifrostTarget, phase spec.SnapshotPhase) *spec.TargetSnapshot {
	restarts, upstreamIndex := 0, 0
	routerPods := make([]spec.Pod, len(RouterPods))
	for i, name := range RouterPods {
		routerPods[i] = bifrostPod(name, routerContainer, &restarts,
			spec.ResourceQuantities{"cpu": "250m", "memory": "256Mi"},
			spec.ResourceQuantities{"cpu": strconv.FormatFloat(b.CPULimitCores, 'f', -1, 64), "memory": strconv.FormatFloat(b.MemoryLimitBytes, 'f', -1, 64)})
	}
	return &spec.TargetSnapshot{
		Phase: phase, CapturedAt: spec.NewUTCTimestamp(StartTime),
		Workloads: []spec.Workload_2{
			{Role: spec.WorkloadRoleTarget, TargetType: spec.TargetTypeBifrost, Pods: routerPods, Replicas: &spec.Replicas{Desired: 2, Ready: 2}},
			{
				Role: spec.WorkloadRoleUpstream, TargetType: spec.TargetTypeOpenaiCompatible, UpstreamIndex: &upstreamIndex,
				Pods: []spec.Pod{bifrostPod(UpstreamPod, upstreamContainer, &restarts,
					spec.ResourceQuantities{"cpu": "1", "memory": "128Mi"}, spec.ResourceQuantities{"cpu": "2", "memory": "512Mi"})},
				Replicas: &spec.Replicas{Desired: 1, Ready: 1},
			},
		},
	}
}

func bifrostPod(name, container string, restarts *int, requests, limits spec.ResourceQuantities) spec.Pod {
	count := *restarts
	return spec.Pod{Name: name, Containers: []spec.Container{{
		Name: container, Image: "registry.invalid/" + container + ":v0.0.0", Env: []spec.EnvVar{},
		Requests: requests, Limits: limits, RestartCount: &count,
	}}}
}

// bifrostEntries renders the router's and the upstream's series.
func (s Scenario) bifrostEntries(entry func(input.SeriesRef) *rawEntry) {
	b := s.Bifrost
	stageValue := func(values []float64) func(time.Time) float64 {
		return func(at time.Time) float64 { return s.valueAt(values, at, false) }
	}
	share := func(value func(time.Time) float64, fraction float64) func(time.Time) float64 {
		return func(at time.Time) float64 { return value(at) * fraction }
	}
	for i, pod := range RouterPods {
		fraction := 1.0
		if i > 0 {
			fraction = routerSecondPodShare
		}
		labels := func() map[string]string {
			return map[string]string{"namespace": bifrostNamespace, "pod": pod, "container": routerContainer}
		}
		entry(ref(spec.CatalogNameKubernetes, "cpu_cores")).add(labels(), s.sampled(share(stageValue(b.CPUCores), fraction)))
		entry(ref(spec.CatalogNameKubernetes, "cpu_limit_cores")).add(labels(), s.sampled(constant(b.CPULimitCores)))
		if b.Throttle != nil {
			entry(ref(spec.CatalogNameKubernetes, "cpu_throttle_ratio")).add(labels(), s.sampled(share(stageValue(b.Throttle), fraction)))
		}
		entry(ref(spec.CatalogNameKubernetes, "memory_working_set_bytes")).add(labels(), s.sampled(share(s.workingSet, fraction)))
		entry(ref(spec.CatalogNameKubernetes, "pod_ready")).add(map[string]string{"namespace": bifrostNamespace, "pod": pod}, s.sampled(constant(1)))
		entry(ref(spec.CatalogNameBifrost, "active_requests")).add(map[string]string{"namespace": bifrostNamespace, "pod": pod}, s.sampled(share(stageValue(b.ActivePeak), fraction)))
		// Requests split over the pods in proportion to their work.
		requestShare := fraction / (1 + routerSecondPodShare)
		histogramLabels := map[string]string{"namespace": bifrostNamespace, "pod": pod, "fallback_index": "0", "model": "mock-model"}
		s.renderHistogram(entry(ref(spec.CatalogNameBifrost, "upstream_latency_seconds")), "bifrost_upstream_latency_seconds", histogramLabels, b.UpstreamSeconds, upstreamBounds, requestShare)
		if b.OverheadMicroseconds != nil {
			s.renderHistogram(entry(ref(spec.CatalogNameBifrost, "overhead_latency_microseconds")), "bifrost_overhead_latency_microseconds", histogramLabels, b.OverheadMicroseconds, overheadBounds, requestShare)
		}
		if b.RetriesPerAttempt != nil {
			retries := entry(ref(spec.CatalogNameBifrost, "request_retries"))
			retries.add(withName(histogramLabels, "bifrost_request_retries_count"), s.sampled(s.cumulative(func(int) float64 { return requestShare })))
			retries.add(withName(histogramLabels, "bifrost_request_retries_sum"), s.sampled(s.cumulative(func(stage int) float64 {
				return requestShare * b.RetriesPerAttempt[min(stage, len(b.RetriesPerAttempt)-1)]
			})))
		}
		if b.DroppedShare != nil {
			dropped := withName(histogramLabels, "bifrost_error_requests_total")
			dropped["error_type"] = "bifrost_dropped"
			entry(ref(spec.CatalogNameBifrost, "error_requests_total")).add(dropped, s.sampled(s.cumulative(func(stage int) float64 {
				return requestShare * b.DroppedShare[min(stage, len(b.DroppedShare)-1)]
			})))
		}
		entry(ref(spec.CatalogNameBifrost, "upstream_requests_total")).add(withName(histogramLabels, "bifrost_upstream_requests_total"), s.sampled(s.cumulative(func(int) float64 { return requestShare })))
	}
	upstream := map[string]string{"namespace": upstreamNamespace, "pod": UpstreamPod, "container": upstreamContainer}
	entry(ref(spec.CatalogNameKubernetes, "cpu_cores")).add(upstream, s.sampled(constant(0.2)))
	entry(ref(spec.CatalogNameKubernetes, "cpu_limit_cores")).add(upstream, s.sampled(constant(2)))
	entry(ref(spec.CatalogNameKubernetes, "memory_working_set_bytes")).add(upstream, s.sampled(constant(64<<20)))
	entry(ref(spec.CatalogNameKubernetes, "pod_ready")).add(map[string]string{"namespace": upstreamNamespace, "pod": UpstreamPod}, s.sampled(constant(1)))
}

// workingSet is the busiest router pod's working set: the stage's base plus
// WorkingSetGrowth per second since the stage started.
func (s Scenario) workingSet(at time.Time) float64 {
	b := s.Bifrost
	base := s.valueAt(b.WorkingSetBytes, at, false)
	for i := range s.Stages {
		window := s.stageWindow(i)
		if !at.Before(window.Start) && at.Before(window.End) {
			return base + b.WorkingSetGrowth*at.Sub(window.Start).Seconds()
		}
	}
	return base
}

func withName(labels map[string]string, name string) map[string]string {
	named := map[string]string{"__name__": name}
	for key, value := range labels {
		named[key] = value
	}
	return named
}

// cumulative is a counter that grows by perRequest(stage) × the stage's
// offered rate for every second of each stage.
func (s Scenario) cumulative(perRequest func(stage int) float64) func(time.Time) float64 {
	return func(at time.Time) float64 {
		var total float64
		for i, stage := range s.Stages {
			window := s.stageWindow(i)
			if !at.After(window.Start) {
				break
			}
			overlap := math.Min(at.Sub(window.Start).Seconds(), window.Duration().Seconds())
			total += perRequest(i) * stage.OfferedRPS * overlap
		}
		return total
	}
}

// renderHistogram writes a cumulative histogram of the stage means, each
// stage's observations uniform over ±20% of its mean.
func (s Scenario) renderHistogram(into *rawEntry, name string, labels map[string]string, means, bounds []float64, requestShare float64) {
	meanOf := func(stage int) float64 { return means[min(stage, len(means)-1)] }
	into.add(withName(labels, name+"_count"), s.sampled(s.cumulative(func(int) float64 { return requestShare })))
	into.add(withName(labels, name+"_sum"), s.sampled(s.cumulative(func(stage int) float64 { return requestShare * meanOf(stage) })))
	for _, bound := range append(append([]float64{}, bounds...), math.Inf(1)) {
		bucket := withName(labels, name+"_bucket")
		bucket["le"] = strconv.FormatFloat(bound, 'g', -1, 64)
		if math.IsInf(bound, 1) {
			bucket["le"] = "+Inf"
		}
		into.add(bucket, s.sampled(s.cumulative(func(stage int) float64 {
			low, high := 0.8*meanOf(stage), 1.2*meanOf(stage)
			return requestShare * math.Max(0, math.Min(1, (bound-low)/(high-low)))
		})))
	}
}

// bifrostFiles are the Bifrost-only bundle files: the captured config.
func (s Scenario) bifrostFiles() (map[string][]byte, error) {
	if s.Bifrost == nil {
		return nil, nil
	}
	files, err := s.Bifrost.configDocuments()
	if err != nil {
		return nil, fmt.Errorf("scenario %s: %w", s.Name, err)
	}
	return files, nil
}
