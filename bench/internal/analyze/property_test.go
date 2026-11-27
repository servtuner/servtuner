package analyze_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// randomLadder builds a ladder of 3 to 6 stages with random latencies and
// random server-side signals, and decides which stages to make invalid.
// invalid stages carry a cause the quality gate must catch: a short rate or a
// high send lag.
func randomLadder(random *rand.Rand) (scenario analyzetest.Scenario, invalid []int) {
	stageCount := 3 + random.IntN(4)
	scenario = analyzetest.Scenario{
		Name:    "property",
		SLOs:    map[string]any{"ttft_p95": "500ms", "e2e_p95": "5s", "max_error_rate": 0.02},
		Targets: []spec.ScrapedJob{{Job: "targets", Role: spec.JobRoleTarget, ScrapeIntervalSeconds: 5, ScrapeTimeoutSeconds: 4}},
		Signals: map[input.Signal][]float64{input.ReadyPods: {2}, input.BenchCPUUsage: {0.3}},
		Pre:     analyzetest.Snapshot(spec.SnapshotPhasePre), Post: analyzetest.Snapshot(spec.SnapshotPhasePost),
	}
	for i := range stageCount {
		scenario.Stages = append(scenario.Stages, analyzetest.Stage{OfferedRPS: float64(4 + 2*i), DurationSeconds: 60})
	}
	for index := range scenario.Stages {
		if random.IntN(3) == 0 && len(invalid) < stageCount-1 {
			invalid = append(invalid, index)
		}
	}
	randomizeStages(random, &scenario, nil)
	randomizeSeries(random, &scenario, nil)
	for _, index := range invalid {
		applyInvalidCause(random, &scenario.Stages[index])
	}
	return scenario, invalid
}

func applyInvalidCause(random *rand.Rand, stage *analyzetest.Stage) {
	if random.IntN(2) == 0 {
		stage.AchievedRatio = 0.5 + random.Float64()*0.4
	} else {
		stage.LagBase = 0.1 + random.Float64()*0.3
	}
}

// randomizeStages draws latencies and error rates for the stages in only, or
// for every stage when only is nil.
func randomizeStages(random *rand.Rand, scenario *analyzetest.Scenario, only []int) {
	for index := range scenario.Stages {
		if only != nil && !slices.Contains(only, index) {
			continue
		}
		stage := &scenario.Stages[index]
		stage.TTFT = 0.15 + random.Float64()*0.45
		stage.E2E = 1 + random.Float64()*3
		stage.ErrorEvery = 0
		if random.IntN(4) == 0 {
			stage.ErrorEvery = 5 + random.IntN(30)
		}
	}
}

// randomizeSeries draws the server-side signals of the stages in only, or of
// every stage when only is nil.
func randomizeSeries(random *rand.Rand, scenario *analyzetest.Scenario, only []int) {
	count := len(scenario.Stages)
	if only == nil {
		scenario.QueueMeanSeconds = make([]float64, count)
	}
	for index := range scenario.QueueMeanSeconds {
		if only == nil || slices.Contains(only, index) {
			scenario.QueueMeanSeconds[index] = random.Float64() * 0.2
		}
	}
	for _, ref := range []input.Signal{
		input.KVCacheUsage, input.GPUEngineActive, input.RequestsWaiting, input.Preemptions, input.TargetCPUUsage,
	} {
		values := scenario.Signals[ref]
		if values == nil {
			values = make([]float64, count)
		}
		for index := range values {
			if only != nil && !slices.Contains(only, index) {
				continue
			}
			switch ref {
			case input.RequestsWaiting:
				values[index] = float64(random.IntN(3)) * float64(random.IntN(2))
			case input.Preemptions:
				values[index] = float64(random.IntN(2)) * float64(random.IntN(10))
			default:
				values[index] = random.Float64()
			}
		}
		scenario.Signals[ref] = values
	}
}

func marshal(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func analyzeScenario(t *testing.T, scenario analyzetest.Scenario) spec.AnalysisResult {
	t.Helper()
	run, err := scenario.Run()
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyze.Analyze(run)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestExcludedStagesNeverContribute is the analysis's core property:
// whatever an excluded stage measured, whether latencies, errors or
// server-side signals, the capacity model and every other stage's numbers are
// the same. Each trial analyses a random ladder, redraws everything the
// excluded stages measured (keeping them excluded), and compares.
func TestExcludedStagesNeverContribute(t *testing.T) {
	const trials = 60
	var trialsWithExclusion, trialsWithKnee, trialsWithSLOMax int
	for trial := range trials {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			random := rand.New(rand.NewPCG(uint64(trial), 99))
			scenario, invalid := randomLadder(random)
			if invalid == nil {
				invalid = []int{}
			}
			before := analyzeScenario(t, scenario)

			// The gate must have excluded exactly the invalid stages.
			var excluded []int
			for _, stage := range before.Quality.Stages {
				if stage.IsExcluded {
					excluded = append(excluded, stage.StageIndex)
				}
			}
			if !slices.Equal(excluded, invalid) {
				t.Fatalf("excluded stages = %v, want %v", excluded, invalid)
			}

			changed := scenario
			changed.Stages = slices.Clone(scenario.Stages)
			changed.Signals = map[input.Signal][]float64{}
			for ref, values := range scenario.Signals {
				changed.Signals[ref] = slices.Clone(values)
			}
			changed.QueueMeanSeconds = slices.Clone(scenario.QueueMeanSeconds)
			randomizeStages(random, &changed, invalid)
			randomizeSeries(random, &changed, invalid)
			after := analyzeScenario(t, changed)

			if marshal(t, before.Capacity) != marshal(t, after.Capacity) {
				t.Errorf("capacity changed when only excluded stages changed:\nbefore %s\nafter  %s", marshal(t, before.Capacity), marshal(t, after.Capacity))
			}
			for i, stage := range before.Stages {
				if !slices.Contains(invalid, stage.StageIndex) && marshal(t, stage) != marshal(t, after.Stages[i]) {
					t.Errorf("stage %d aggregate changed", stage.StageIndex)
				}
			}
			if len(invalid) > 0 {
				trialsWithExclusion++
			}
			if before.Capacity.Knee != nil {
				trialsWithKnee++
			}
			if before.Capacity.SloMax != nil {
				trialsWithSLOMax++
			}
		})
	}
	// A property test whose trials never exercise the thing is worthless: make
	// sure the random ladders did exclude stages and did produce answers.
	if trialsWithExclusion < trials/4 || trialsWithKnee < trials/4 || trialsWithSLOMax < trials/4 {
		t.Errorf("weak trials: %d with exclusions, %d with a knee, %d with an slo_max of %d", trialsWithExclusion, trialsWithKnee, trialsWithSLOMax, trials)
	}
}
