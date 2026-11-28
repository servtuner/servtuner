package signals

import (
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func samples(values ...float64) []input.Sample {
	result := make([]input.Sample, len(values))
	for i, value := range values {
		result[i] = input.Sample{At: start.Add(time.Duration(i*5) * time.Second), Value: value}
	}
	return result
}

func labelled(pod, container string, values ...float64) input.Series {
	return input.Series{Labels: map[string]string{"pod": pod, "container": container}, Samples: samples(values...)}
}

func derive(matrix input.MapMatrix, mutate ...func(*Config)) *Deriver {
	config := Config{Matrix: matrix, BenchPod: "bench-0", TargetPods: map[string]bool{"vllm-0": true, "vllm-1": true}}
	for _, change := range mutate {
		change(&config)
	}
	return New(config)
}

func signal(t *testing.T, d *Deriver, which input.Signal) []float64 {
	t.Helper()
	got, found, err := d.Signal(which)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return nil
	}
	values := make([]float64, len(got))
	for i, sample := range got {
		values[i] = sample.Value
	}
	return values
}

func equal(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBenchAndTargetCPUAreSplitByPod(t *testing.T) {
	matrix := input.MapMatrix{
		CPUCores: {
			labelled("bench-0", "bench", 1.8, 0.6), labelled("vllm-0", "vllm", 3, 1), labelled("vllm-1", "vllm", 1, 2),
			labelled("other-0", "app", 99, 99),
		},
		CPULimitCores: {
			labelled("bench-0", "bench", 2, 2), labelled("vllm-0", "vllm", 4, 4), labelled("vllm-1", "vllm", 4, 4), labelled("other-0", "app", 100, 100),
		},
	}
	d := derive(matrix)
	if got := signal(t, d, input.BenchCPUUsage); !equal(got, []float64{0.9, 0.3}) {
		t.Errorf("bench cpu usage = %v, want [0.9 0.3]", got)
	}
	// The busiest target pod: 3/4 then 2/4. The stranger pod and the bench pod are not target.
	if got := signal(t, d, input.TargetCPUUsage); !equal(got, []float64{0.75, 0.5}) {
		t.Errorf("target cpu usage = %v, want [0.75 0.5]", got)
	}
}

func TestTargetCPUFallsBackToTheRequestWithoutALimit(t *testing.T) {
	matrix := input.MapMatrix{
		CPUCores:        {labelled("vllm-0", "vllm", 2)},
		CPURequestCores: {labelled("vllm-0", "vllm", 4)},
	}
	d := derive(matrix)
	if got := signal(t, d, input.TargetCPUUsage); !equal(got, []float64{0.5}) {
		t.Errorf("target cpu usage = %v", got)
	}
	if got := signal(t, d, input.BenchCPUUsage); got != nil {
		t.Errorf("the bench pod has no series, so there is no evidence: %v", got)
	}
}

func TestLimitIsCarriedForwardBetweenItsSamples(t *testing.T) {
	cores := labelled("bench-0", "bench", 1, 1, 1)
	limit := input.Series{Labels: cores.Labels, Samples: []input.Sample{{At: start, Value: 2}}}
	d := derive(input.MapMatrix{CPUCores: {cores}, CPULimitCores: {limit}})
	if got := signal(t, d, input.BenchCPUUsage); !equal(got, []float64{0.5, 0.5, 0.5}) {
		t.Errorf("usage = %v", got)
	}
}

func TestThrottleIsMaxedPerRole(t *testing.T) {
	matrix := input.MapMatrix{CPUThrottle: {
		labelled("bench-0", "bench", 0.1), labelled("vllm-0", "vllm", 0.2), labelled("vllm-1", "vllm", 0.6), labelled("other-0", "app", 1),
	}}
	d := derive(matrix)
	if got := signal(t, d, input.BenchCPUThrottle); !equal(got, []float64{0.1}) {
		t.Errorf("bench throttle = %v", got)
	}
	if got := signal(t, d, input.TargetCPUThrottle); !equal(got, []float64{0.6}) {
		t.Errorf("target throttle = %v", got)
	}
}

func TestWithoutABenchPodEverythingElseIsTarget(t *testing.T) {
	matrix := input.MapMatrix{CPUThrottle: {labelled("a", "c", 0.2), labelled("b", "c", 0.4)}}
	d := derive(matrix, func(c *Config) { c.BenchPod = ""; c.TargetPods = nil })
	if got := signal(t, d, input.TargetCPUThrottle); !equal(got, []float64{0.4}) {
		t.Errorf("target throttle = %v", got)
	}
	if got := signal(t, d, input.BenchCPUThrottle); got != nil {
		t.Errorf("no bench pod: %v", got)
	}
}

func TestReplicasAreCombinedByTheRightOperator(t *testing.T) {
	pod := func(name string, values ...float64) input.Series {
		return input.Series{Labels: map[string]string{"pod": name}, Samples: samples(values...)}
	}
	matrix := input.MapMatrix{
		VLLMWaiting:      {pod("vllm-0", 1, 2), pod("vllm-1", 3, 0), pod("other-0", 50, 50)},
		VLLMKVCacheUsage: {pod("vllm-0", 0.2, 0.9), pod("vllm-1", 0.6, 0.3), pod("other-0", 1, 1)},
		PodReady:         {pod("vllm-0", 1, 1), pod("vllm-1", 1, 0), labelled("bench-0", "bench", 1, 1)},
		PodRestarts:      {labelled("vllm-0", "vllm", 0, 1), labelled("vllm-1", "vllm", 0, 2), labelled("bench-0", "bench", 0, 7)},
	}
	d := derive(matrix)
	if got := signal(t, d, input.RequestsWaiting); !equal(got, []float64{4, 2}) {
		t.Errorf("waiting is a total queue, summed over the target replicas: %v", got)
	}
	if got := signal(t, d, input.KVCacheUsage); !equal(got, []float64{0.6, 0.9}) {
		t.Errorf("KV cache is a per-pod peak, maxed: %v", got)
	}
	if got := signal(t, d, input.ReadyPods); !equal(got, []float64{2, 1}) {
		t.Errorf("ready pods are the target's ready pods, without the bench pod: %v", got)
	}
	if got := signal(t, d, input.Restarts); !equal(got, []float64{0, 3}) {
		t.Errorf("restarts are summed over the target only: %v", got)
	}
}

func TestPreemptionsSumPerPodTotalsAndSurviveACounterReset(t *testing.T) {
	pod := func(name string, values ...float64) input.Series {
		return input.Series{Labels: map[string]string{"pod": name}, Samples: samples(values...)}
	}
	// vllm-0 restarts after 20: its counter falls to 2, which is 2 new preemptions.
	matrix := input.MapMatrix{VLLMPreemptions: {pod("vllm-0", 10, 20, 2, 5), pod("vllm-1", 0, 1, 1, 4)}}
	if got := signal(t, derive(matrix), input.Preemptions); !equal(got, []float64{0, 11, 13, 19}) {
		t.Errorf("cumulative preemptions = %v, want [0 11 13 19]", got)
	}
}

func TestQueueHistogramSumAndCountAreSplitByTheNameLabel(t *testing.T) {
	series := func(name, pod string, values ...float64) input.Series {
		return input.Series{Labels: map[string]string{"__name__": name, "pod": pod}, Samples: samples(values...)}
	}
	matrix := input.MapMatrix{LiteLLMQueueTime: {
		series("litellm_request_queue_time_seconds_sum", "vllm-0", 0, 1), series("litellm_request_queue_time_seconds_sum", "vllm-1", 0, 3),
		series("litellm_request_queue_time_seconds_count", "vllm-0", 0, 10), series("litellm_request_queue_time_seconds_count", "vllm-1", 0, 30),
		series("litellm_request_queue_time_seconds_bucket", "vllm-0", 0, 99),
	}}
	d := derive(matrix)
	if got := signal(t, d, input.QueueSecondsSum); !equal(got, []float64{0, 4}) {
		t.Errorf("queue seconds sum = %v", got)
	}
	if got := signal(t, d, input.QueueCount); !equal(got, []float64{0, 40}) {
		t.Errorf("queue count = %v", got)
	}
}

func TestGPUSignalsKeepTargetGPUsAndConvertPercent(t *testing.T) {
	gpu := func(pod string, values ...float64) input.Series {
		labels := map[string]string{"gpu": "0"}
		if pod != "" {
			labels["pod"] = pod
		}
		return input.Series{Labels: labels, Samples: samples(values...)}
	}
	matrix := input.MapMatrix{
		DCGMEngineActive: {gpu("vllm-0", 0.3), gpu("vllm-0", 0.7), gpu("other-0", 1)},
		DCGMGPUUtil:      {gpu("vllm-0", 0.4), gpu("vllm-1", 0.9), gpu("other-0", 1)},
	}
	d := derive(matrix)
	if got := signal(t, d, input.GPUEngineActive); !equal(got, []float64{0.7}) {
		t.Errorf("engine activity = %v", got)
	}
	if got := signal(t, d, input.GPUUtil); !equal(got, []float64{0.9}) {
		t.Errorf("gpu util must be a 0 to 1 ratio: %v", got)
	}
	nodeLevel := input.MapMatrix{DCGMEngineActive: {gpu("", 0.5)}}
	if got := signal(t, derive(nodeLevel), input.GPUEngineActive); !equal(got, []float64{0.5}) {
		t.Errorf("a node-level exporter carries no pod label: %v", got)
	}
}

func TestGPUAttributionFollowsExportedPodWhenTheExporterOwnsPod(t *testing.T) {
	gpu := func(labels map[string]string, value float64) input.Series {
		return input.Series{Labels: labels, Samples: samples(value)}
	}
	// Stock exporter: pod is the exporter's own pod, the workload is exported_pod.
	stock := input.MapMatrix{DCGMEngineActive: {
		gpu(map[string]string{"pod": "dcgm-exporter-x", "exported_pod": "vllm-0"}, 0.6),
		gpu(map[string]string{"pod": "dcgm-exporter-x", "exported_pod": "other-0"}, 1),
	}}
	if got := signal(t, derive(stock), input.GPUEngineActive); !equal(got, []float64{0.6}) {
		t.Errorf("stock exporter layout = %v, want the vllm-0 GPU only", got)
	}
	// honor_labels: pod is the workload.
	honoured := input.MapMatrix{DCGMEngineActive: {
		gpu(map[string]string{"pod": "vllm-1"}, 0.4), gpu(map[string]string{"pod": "other-0"}, 1),
	}}
	if got := signal(t, derive(honoured), input.GPUEngineActive); !equal(got, []float64{0.4}) {
		t.Errorf("honor_labels layout = %v, want the vllm-1 GPU only", got)
	}
}

func TestRouterConcurrencyNeedsTheKnob(t *testing.T) {
	matrix := input.MapMatrix{BifrostActive: {labelled("bifrost-0", "bifrost", 250, 500), labelled("bifrost-1", "bifrost", 100, 100)}}
	withKnob := derive(matrix, func(c *Config) { c.BifrostConcurrency = 1000; c.TargetPods = nil })
	if got := signal(t, withKnob, input.RouterConcurrency); !equal(got, []float64{0.25, 0.5}) {
		t.Errorf("router concurrency = %v", got)
	}
	if got := signal(t, derive(matrix, func(c *Config) { c.TargetPods = nil }), input.RouterConcurrency); got != nil {
		t.Errorf("without the concurrency knob there is no ratio: %v", got)
	}
}

func TestNothingCollectedMeansNoEvidence(t *testing.T) {
	d := derive(input.MapMatrix{})
	for name := range Table {
		if _, found, err := d.Signal(name); found || err != nil {
			t.Errorf("%s: found %v, err %v", name, found, err)
		}
	}
	if _, _, err := d.Signal("bogus"); err == nil {
		t.Error("an unknown signal is an error")
	}
}

func TestPodsOfCollectsEveryWorkload(t *testing.T) {
	snapshot := &spec.TargetSnapshot{Workloads: []spec.Workload_2{
		{Pods: []spec.Pod{{Name: "a"}, {Name: "b"}}}, {Pods: []spec.Pod{{Name: "c"}}},
	}}
	pods := PodsOf(snapshot, nil)
	if len(pods) != 3 || !pods["a"] || !pods["c"] {
		t.Errorf("pods = %v", pods)
	}
}

// TestTableNamesRealCatalogEntries keeps the signals honest: every catalog
// entry they read must exist in the embedded catalogs, so an invented or
// renamed entry fails here.
func TestTableNamesRealCatalogEntries(t *testing.T) {
	catalogs, err := prometheus.Catalogs()
	if err != nil {
		t.Fatal(err)
	}
	for signal, refs := range Table {
		for _, entry := range refs {
			isEntry := false
			for _, query := range catalogs[entry.Catalog].Queries {
				isEntry = isEntry || string(query.Name) == entry.Name
			}
			if !isEntry {
				t.Errorf("signal %s reads %s/%s, which is not a catalog entry", signal, entry.Catalog, entry.Name)
			}
		}
	}
}

// A router's upstream pods are targets (their CPU counts) but not replicas of
// the router: per-replica capacity divides by the router's pods only.
func TestReadyPodsCountOnlyTheWorkloadUnderTest(t *testing.T) {
	ready := func(name string) input.Series {
		return input.Series{Labels: map[string]string{"pod": name}, Samples: samples(1, 1)}
	}
	matrix := input.MapMatrix{PodReady: {ready("bifrost-0"), ready("mock-0"), ready("bench-0")}}
	d := derive(matrix, func(config *Config) {
		config.TargetPods = map[string]bool{"bifrost-0": true, "mock-0": true}
		config.ReplicaPods = map[string]bool{"bifrost-0": true}
	})
	if got := signal(t, d, input.ReadyPods); !equal(got, []float64{1, 1}) {
		t.Errorf("ready replicas = %v, want the router's one pod", got)
	}
}

func TestReplicaPodsOfLeavesOutUpstreams(t *testing.T) {
	snapshot := &spec.TargetSnapshot{Workloads: []spec.Workload_2{
		{Role: spec.WorkloadRoleTarget, Pods: []spec.Pod{{Name: "bifrost-0"}}},
		{Role: spec.WorkloadRoleUpstream, Pods: []spec.Pod{{Name: "mock-0"}}},
	}}
	if got := ReplicaPodsOf(snapshot); len(got) != 1 || !got["bifrost-0"] {
		t.Errorf("replica pods = %v", got)
	}
	if got := PodsOf(snapshot); len(got) != 2 {
		t.Errorf("target pods = %v", got)
	}
}

func TestAShardPodCountsAsBenchSoItsSaturationShows(t *testing.T) {
	matrix := input.MapMatrix{
		CPUCores:      {labelled("bench-0", "bench", 0.4, 0.4), labelled("bench-1", "bench", 1.9, 1.0), labelled("vllm-0", "vllm", 1, 1)},
		CPULimitCores: {labelled("bench-0", "bench", 2, 2), labelled("bench-1", "bench", 2, 2), labelled("vllm-0", "vllm", 4, 4)},
	}
	d := derive(matrix, func(config *Config) {
		config.ShardPods = []string{"bench-1"}
		config.TargetPods = nil // every non-bench pod is target
	})
	// The busiest bench pod: shard 1 at 1.9/2, then 1.0/2.
	if got := signal(t, d, input.BenchCPUUsage); !equal(got, []float64{0.95, 0.5}) {
		t.Errorf("bench cpu usage = %v, want shard 1's [0.95 0.5]", got)
	}
	if got := signal(t, d, input.TargetCPUUsage); !equal(got, []float64{0.25, 0.25}) {
		t.Errorf("target cpu usage = %v: a shard pod is not target", got)
	}
}

func TestFramebufferFreeIsOfTheFullestTargetGPU(t *testing.T) {
	gpu := func(pod, index string, values ...float64) input.Series {
		return input.Series{Labels: map[string]string{"pod": pod, "gpu": index}, Samples: samples(values...)}
	}
	const gib = 1073741824 // bytes per GiB
	matrix := input.MapMatrix{
		DCGMFBUsed: {gpu("vllm-0", "0", 6*gib, 7*gib), gpu("vllm-0", "1", 2*gib, 2*gib), gpu("other-0", "0", 8*gib, 8*gib)},
		DCGMFBFree: {gpu("vllm-0", "0", 2*gib, 1*gib), gpu("vllm-0", "1", 6*gib, 6*gib), gpu("other-0", "0", 0, 0)},
	}
	d := derive(matrix)
	if got := signal(t, d, input.GPUMemoryFreeFraction); !equal(got, []float64{0.25, 0.125}) {
		t.Errorf("free fraction = %v, want GPU 0 of vllm-0: [0.25 0.125]", got)
	}
	if got := signal(t, d, input.GPUMemoryUsedBytes); !equal(got, []float64{6 * gib, 7 * gib}) {
		t.Errorf("used = %v, want the fullest target GPU", got)
	}
}

// SGLang's series stand in for vLLM's when a run has no vLLM series. TP ranks
// of one group report the same batch and count once; DP groups and pods add.
func TestSGLangSeriesStandInForVLLMsAndCountEachGroupOnce(t *testing.T) {
	rank := func(pod, dp, tp string, values ...float64) input.Series {
		return input.Series{Labels: map[string]string{"pod": pod, "dp_rank": dp, "tp_rank": tp}, Samples: samples(values...)}
	}
	plain := func(pod string, values ...float64) input.Series {
		return input.Series{Labels: map[string]string{"pod": pod}, Samples: samples(values...)}
	}
	matrix := input.MapMatrix{
		SGLangRunning: {rank("vllm-0", "0", "1", 4, 6), rank("vllm-0", "0", "0", 4, 6), rank("vllm-0", "1", "2", 1, 2), rank("vllm-1", "", "0", 3, 3), rank("other-0", "", "0", 90, 90)},
		SGLangQueue:   {rank("vllm-0", "0", "0", 0, 5), rank("vllm-0", "0", "1", 0, 5)},
		SGLangTokenUsage: {
			rank("vllm-0", "0", "0", 0.2, 0.9), rank("vllm-1", "", "0", 0.6, 0.3),
		},
		SGLangRetracted: {rank("vllm-0", "0", "0", 0, 3), rank("vllm-0", "0", "1", 0, 3)},
		SGLangPrompt:    {plain("vllm-0", 0, 1000)},
		SGLangCached:    {plain("vllm-0", 0, 600)},
	}
	d := derive(matrix)
	if got := signal(t, d, input.RequestsRunning); !equal(got, []float64{8, 11}) {
		t.Errorf("running = %v, want [8 11] (one TP rank per group, groups and pods summed)", got)
	}
	if got := signal(t, d, input.RequestsWaiting); !equal(got, []float64{0, 5}) {
		t.Errorf("waiting = %v", got)
	}
	if got := signal(t, d, input.KVCacheUsage); !equal(got, []float64{0.6, 0.9}) {
		t.Errorf("KV usage = %v", got)
	}
	if got := signal(t, d, input.Preemptions); !equal(got, []float64{0, 3}) {
		t.Errorf("retractions = %v, want [0 3] (counted once, not per rank)", got)
	}
	if hits, queries := signal(t, d, input.PrefixCacheHits), signal(t, d, input.PrefixCacheQueries); !equal(hits, []float64{0, 600}) || !equal(queries, []float64{0, 1000}) {
		t.Errorf("prefix hits %v over queries %v", hits, queries)
	}
}

func TestVLLMSeriesWinOverSGLangs(t *testing.T) {
	pod := func(name string, values ...float64) input.Series {
		return input.Series{Labels: map[string]string{"pod": name}, Samples: samples(values...)}
	}
	matrix := input.MapMatrix{VLLMWaiting: {pod("vllm-0", 1, 1)}, SGLangQueue: {pod("vllm-0", 9, 9)}}
	if got := signal(t, derive(matrix), input.RequestsWaiting); !equal(got, []float64{1, 1}) {
		t.Errorf("waiting = %v, want vLLM's", got)
	}
}
