package analyzetest

import (
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Scenarios are the golden bundles: one per quality check,
// the quality-independent parts of the capacity model, and a clean ladder. Each name is the
// bundle's directory under bench/testdata/analysis.
func Scenarios() []Scenario {
	scenarios := []Scenario{
		cleanLadder(), achievedRPSLow(), sendLagHigh(), clientBoundLagGrowth(), clientBoundCPU(),
		lowResolution(), insufficientSamples(), configDriftRestart(), configDriftUnknownTime(),
		oomRestart(), knobs(), coverage(), kneeQueueing(), kneePreemptionsWeakGPU(),
		excludedStageIgnored(), errorRateSLO(),
		bifrostConcurrencyBound(), bifrostQueueDrops(), bifrostUpstreamBound(), bifrostThrottled(), bifrostMemoryLeak(),
	}
	for i := range scenarios {
		if scenarios[i].Queries == nil && (len(scenarios[i].Signals) > 0 || scenarios[i].Bifrost != nil) {
			scenarios[i].Queries = QueriesFor(scenarios[i])
		}
	}
	return scenarios
}

// ScenarioNamed returns the scenario with the given name.
func ScenarioNamed(name string) Scenario {
	for _, scenario := range Scenarios() {
		if scenario.Name == name {
			return scenario
		}
	}
	panic("no scenario named " + name)
}

var (
	// defaultSLOs are met by a healthy target at about 0.3s TTFT.
	defaultSLOs = map[string]any{"ttft_p95": "500ms", "e2e_p95": "5s", "max_error_rate": 0.01}

	benchCPU      = input.BenchCPUUsage
	benchThrottle = input.BenchCPUThrottle
)

// jobs are the scrape jobs of the synthetic cluster: 5s everywhere, so a 60s
// stage satisfies the stage length rule.
func jobs() []spec.ScrapedJob {
	return []spec.ScrapedJob{
		{Job: "servtuner-targets", Role: spec.JobRoleTarget, ScrapeIntervalSeconds: 5, ScrapeTimeoutSeconds: 4},
		{Job: "servtuner-cadvisor", Role: spec.JobRoleCadvisor, ScrapeIntervalSeconds: 5, ScrapeTimeoutSeconds: 4},
	}
}

// healthy is a scenario of identical, healthy stages with every check's
// evidence present: bench series, snapshots, scrape targets and SLOs.
func healthy(name string, stages ...Stage) Scenario {
	return Scenario{
		Name: name, Stages: stages, SLOs: defaultSLOs, Targets: jobs(),
		Signals: map[input.Signal][]float64{benchCPU: {0.3}, benchThrottle: {0}},
		Pre:     Snapshot(spec.SnapshotPhasePre), Post: Snapshot(spec.SnapshotPhasePost),
	}
}

// stage60 is a 60s stage at the given rate, with healthy latencies.
func stage60(rate float64) Stage {
	return Stage{OfferedRPS: rate, DurationSeconds: 60, TTFT: 0.2, E2E: 2}
}

func cleanLadder() Scenario {
	ttft := []float64{0.20, 0.21, 0.22, 0.24, 0.42, 0.80}
	e2e := []float64{2.0, 2.0, 2.1, 2.2, 2.6, 3.5}
	var stages []Stage
	for i := range ttft {
		stages = append(stages, Stage{OfferedRPS: float64(4 + i), DurationSeconds: 60, TTFT: ttft[i], E2E: e2e[i]})
	}
	s := healthy("clean-ladder", stages...)
	s.Knobs = map[string]any{"vllm.max_num_seqs": 128, "vllm.enable_prefix_caching": true}
	s.Signals[benchCPU] = []float64{0.20, 0.25, 0.30, 0.35, 0.40, 0.45}
	s.Signals[input.TargetCPUUsage] = []float64{0.3, 0.35, 0.4, 0.5, 0.6, 0.7}
	s.Signals[input.TargetCPUThrottle] = []float64{0}
	s.Signals[input.KVCacheUsage] = []float64{0.2, 0.3, 0.4, 0.5, 0.93, 0.99}
	s.Signals[input.GPUEngineActive] = []float64{0.3, 0.4, 0.5, 0.6, 0.8, 0.9}
	s.Signals[input.RequestsWaiting] = []float64{0}
	s.Signals[input.Preemptions] = []float64{0}
	s.Signals[input.ReadyPods] = []float64{2}
	s.Queries = QueriesFor(s, spec.OptionalCatalogResult{Name: spec.CatalogNameDcgm, Result: spec.CatalogResultCollected})
	return s
}

func achievedRPSLow() Scenario {
	s := healthy("achieved-rps-low", stage60(4), stage60(6), stage60(8))
	s.Stages[1].AchievedRatio = 0.9
	s.Signals = nil // no Prometheus: client_bound rests on the records alone
	return s
}

func sendLagHigh() Scenario {
	s := healthy("send-lag-high", stage60(4), stage60(4))
	s.Stages[1].LagBase = 0.08
	return s
}

func clientBoundLagGrowth() Scenario {
	s := healthy("client-bound-lag-growth", stage60(4), stage60(4))
	s.Stages[1].LagGrowth = 0.03
	return s
}

func clientBoundCPU() Scenario {
	s := healthy("client-bound-cpu", stage60(4), stage60(4), stage60(4))
	s.Signals[benchCPU] = []float64{0.3, 0.95, 0.3}
	s.Signals[benchThrottle] = []float64{0, 0, 0.2}
	return s
}

func lowResolution() Scenario {
	s := healthy("low-resolution", Stage{OfferedRPS: 8, DurationSeconds: 30, TTFT: 0.2, E2E: 2}, Stage{OfferedRPS: 8, DurationSeconds: 30, TTFT: 0.2, E2E: 2})
	s.Targets = []spec.ScrapedJob{
		{Job: "servtuner-targets", Role: spec.JobRoleTarget, ScrapeIntervalSeconds: 5, ScrapeTimeoutSeconds: 4},
		{Job: "servtuner-cadvisor", Role: spec.JobRoleCadvisor, ScrapeIntervalSeconds: 10, ScrapeTimeoutSeconds: 8},
	}
	stage := 0
	job := "servtuner-cadvisor"
	s.Warnings = []spec.StageLengthWarning{{
		StageIndex: &stage, Rule: spec.StageLengthRuleMinStageDuration, RequiredDurationSeconds: 100, ActualDurationSeconds: 30,
		ScrapeIntervalSeconds: 10, ScrapeIntervalSource: spec.ScrapeIntervalSourceDiscovered, Job: &job,
		Message: "stage 0 lasts 30s, 100s needed at the 10s interval of job servtuner-cadvisor",
	}}
	return s
}

func insufficientSamples() Scenario {
	return healthy("insufficient-samples",
		Stage{OfferedRPS: 5, DurationSeconds: 30, TTFT: 0.2, E2E: 2}, Stage{OfferedRPS: 5, DurationSeconds: 30, TTFT: 0.2, E2E: 2})
}

func configDriftRestart() Scenario {
	s := healthy("config-drift-restart", stage60(4), stage60(4), stage60(4), stage60(4))
	s.Signals[input.ReadyPods] = []float64{2, 2, 1, 1}
	s.Post.Workloads[0].Replicas = &spec.Replicas{Desired: 2, Ready: 1}
	return s
}

func configDriftUnknownTime() Scenario {
	s := healthy("config-drift-unknown-time", stage60(4), stage60(4))
	s.Post.Workloads[0].Pods[0].Containers[0].Image = "registry.invalid/vllm:v0.0.1"
	return s
}

func oomRestart() Scenario {
	s := healthy("oom-restart", stage60(4), stage60(4), stage60(4))
	restarts, reason := 1, "OOMKilled"
	container := &s.Post.Workloads[0].Pods[0].Containers[0]
	container.RestartCount, container.LastTerminationReason = &restarts, &reason
	s.Signals[input.Restarts] = []float64{0, 0, 1}
	return s
}

func knobs() Scenario {
	s := healthy("knobs", stage60(4), stage60(4))
	s.Knobs = map[string]any{
		"vllm.max_num_seqs":          256,  // the target runs 128: a mismatch
		"vllm.enable_prefix_caching": true, // --enable-prefix-caching: verified
		"vllm.block_size":            16,   // cache_config_info: verified
		"bifrost.concurrency":        1000, // not in the snapshot: unverified
	}
	return s
}

func coverage() Scenario {
	s := healthy("coverage", stage60(4), stage60(4))
	s.Signals[input.KVCacheUsage] = []float64{0.3}
	kvCache := ref(spec.CatalogNameVllm, "kv_cache_usage")
	s.Gaps = map[input.SeriesRef][2]time.Duration{kvCache: {40 * time.Second, 100 * time.Second}}
	window := spec.TimeWindow{Start: spec.NewUTCTimestamp(StartTime), End: spec.NewUTCTimestamp(StartTime.Add(120 * time.Second))}
	detail := "no DCGM exporter pods found"
	failure := "range query failed: context deadline exceeded"
	query := func(entry input.SeriesRef, result spec.QueryResult, detail *string) spec.Query {
		return spec.Query{Catalog: entry.Catalog, Name: spec.MetricName(entry.Name), Query: "synthetic_" + entry.Name, StepSeconds: 5, Window: window, Result: result, Detail: detail}
	}
	s.Queries = &spec.PrometheusQueries{
		Queries: []spec.Query{
			query(ref(spec.CatalogNameKubernetes, "cpu_cores"), spec.QueryResultCollected, nil),
			query(kvCache, spec.QueryResultCollected, nil),
			query(ref(spec.CatalogNameVllm, "num_requests_waiting"), spec.QueryResultError, &failure),
		},
		OptionalCatalogs: []spec.OptionalCatalogResult{{Name: spec.CatalogNameDcgm, Result: spec.CatalogResultAbsent, Detail: &detail}},
	}
	return s
}

func kneeQueueing() Scenario {
	s := healthy("knee-queueing", stage60(4), stage60(5), stage60(6), stage60(7))
	s.Signals[input.RequestsWaiting] = []float64{0, 0, 0, 3}
	s.Signals[input.KVCacheUsage] = []float64{0.3, 0.4, 0.5, 0.7}
	s.Signals[input.GPUEngineActive] = []float64{0.3, 0.4, 0.5, 0.6}
	return s
}

func kneePreemptionsWeakGPU() Scenario {
	s := healthy("knee-preemptions-weak-gpu", stage60(4), stage60(5), stage60(6), stage60(7))
	s.Signals[input.Preemptions] = []float64{0, 0, 0, 12}
	s.Signals[input.GPUUtil] = []float64{0.4, 0.5, 0.6, 0.97} // no profiling metrics: weak signal
	return s
}

func excludedStageIgnored() Scenario {
	s := healthy("excluded-stage-ignored", stage60(4), stage60(6), Stage{OfferedRPS: 8, DurationSeconds: 60, TTFT: 3, E2E: 9, LagBase: 0.2}, stage60(10))
	s.Signals[input.RequestsWaiting] = []float64{0, 0, 9, 0}
	s.Signals[input.KVCacheUsage] = []float64{0.3, 0.35, 0.99, 0.4}
	return s
}

func errorRateSLO() Scenario {
	s := healthy("error-rate-slo", stage60(4), stage60(6), stage60(8))
	s.Stages[1].ErrorEvery = 20
	return s
}

// bifrostScenario is a healthy Bifrost run: four 120s stages from 20 to 80
// RPS, where the client's E2E is the upstream's time, Bifrost's overhead and
// 2 ms of network.
// recordSkew is the mean of 1 + variation: a record's latency over its base.
const recordSkew = 1.15

func bifrostScenario(name string, target BifrostTarget, rates ...float64) Scenario {
	if len(rates) == 0 {
		rates = []float64{20, 40, 60, 80}
	}
	stages := make([]Stage, len(rates))
	for i, rate := range rates {
		e2e := target.UpstreamSeconds[min(i, len(target.UpstreamSeconds)-1)] + 0.002
		if target.OverheadMicroseconds != nil {
			e2e += target.OverheadMicroseconds[min(i, len(target.OverheadMicroseconds)-1)] / 1e6
		}
		// 120s: cpu_cores is a 1-minute rate, so a 60s stage holds only the previous one.
		// Records vary 0 to +30% around the base, so the base is the mean / 1.15.
		stages[i] = Stage{OfferedRPS: rate, DurationSeconds: 120, TTFT: e2e / 4 / recordSkew, E2E: e2e / recordSkew}
	}
	if target.CPULimitCores == 0 {
		target.CPULimitCores = 1
	}
	if target.MemoryLimitBytes == 0 {
		target.MemoryLimitBytes = 1073741824 // 1 GiB
	}
	if target.CPUCores == nil {
		target.CPUCores = []float64{0.1, 0.2, 0.3, 0.4}
	}
	if target.WorkingSetBytes == nil {
		target.WorkingSetBytes = []float64{120 << 20, 140 << 20, 160 << 20, 180 << 20}
	}
	return Scenario{
		Name: name, Stages: stages, SLOs: map[string]any{"ttft_p95": "1s", "e2e_p95": "5s", "max_error_rate": 0.05}, Targets: jobs(),
		Signals: map[input.Signal][]float64{benchCPU: {0.3}, benchThrottle: {0}},
		Pre:     BifrostSnapshot(&target, spec.SnapshotPhasePre), Post: BifrostSnapshot(&target, spec.SnapshotPhasePost),
		Bifrost: &target,
	}
}

// bifrostConcurrencyBound (B1): the upstream holds at 0.5s while Bifrost's
// overhead climbs and its busiest pod runs at its concurrency knob.
func bifrostConcurrencyBound() Scenario {
	return bifrostScenario("bifrost-concurrency-bound", BifrostTarget{
		UpstreamSeconds: []float64{0.5}, OverheadMicroseconds: []float64{300, 2000, 8000, 20000},
		ActivePeak: []float64{6, 12, 18, 23.5}, Concurrency: 25, BufferSize: 100,
	})
}

// bifrostQueueDrops (B2): Bifrost drops what does not fit its queue, and the
// top stage sheds 5% of requests.
func bifrostQueueDrops() Scenario {
	scenario := bifrostScenario("bifrost-queue-drops", BifrostTarget{
		UpstreamSeconds: []float64{0.5, 0.5, 0.52, 0.55}, OverheadMicroseconds: []float64{300},
		ActivePeak: []float64{6, 12, 18, 24}, Concurrency: 1000, BufferSize: 0, DropExcess: true,
		DroppedShare: []float64{0, 0, 0, 0.05},
	})
	scenario.Stages[3].ErrorEvery = 20
	return scenario
}

// bifrostUpstreamBound (B3): the upstream slows from 0.5s to 1.4s while
// Bifrost's overhead stays at 300µs.
func bifrostUpstreamBound() Scenario {
	return bifrostScenario("bifrost-upstream-bound", BifrostTarget{
		UpstreamSeconds: []float64{0.5, 0.7, 1, 1.4}, OverheadMicroseconds: []float64{300},
		ActivePeak: []float64{6, 16, 34, 63}, Concurrency: 1000, BufferSize: 5000,
	})
}

// bifrostThrottled (B6): CPU reaches the 1-core limit and is throttled 15% at
// the top stage. Request logging is on (B8). This Bifrost exports no overhead
// histogram, as v1.5 does not.
func bifrostThrottled() Scenario {
	return bifrostScenario("bifrost-throttled", BifrostTarget{
		UpstreamSeconds: []float64{0.5}, ActivePeak: []float64{6, 12, 18, 24}, Concurrency: 1000, BufferSize: 5000,
		CPUCores: []float64{0.3, 0.5, 0.75, 0.95}, Throttle: []float64{0, 0, 0.02, 0.15}, Logging: true,
	})
}

// bifrostMemoryLeak: the working set grows 50 kB/s at constant load through
// two 6-minute stages (memory sizing needs at least two 5-minute stages).
func bifrostMemoryLeak() Scenario {
	scenario := bifrostScenario("bifrost-memory-leak", BifrostTarget{
		UpstreamSeconds: []float64{0.5}, ActivePeak: []float64{6}, Concurrency: 1000, BufferSize: 5000,
		CPUCores: []float64{0.2}, WorkingSetBytes: []float64{200 << 20}, WorkingSetGrowth: 50_000,
	}, 20, 20)
	for i := range scenario.Stages {
		scenario.Stages[i].DurationSeconds = 360
	}
	return scenario
}
