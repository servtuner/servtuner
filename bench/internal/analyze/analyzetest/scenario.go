// Package analyzetest builds synthetic runs for the analyzer's tests: in memory
// for the property tests, and as bundle directories written at test time for
// the golden tests. Everything is generated from a Scenario by integer and
// rounded arithmetic only, so a scenario always produces the same bundle and
// only the scenario and its expected analysis.json are checked in.
package analyzetest

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/loadgen/pacer"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// RunID is the run id of every synthetic bundle; it seeds the bootstrap.
const RunID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// seriesStep is the spacing of generated series samples and the step recorded
// in queries.json.
const seriesStep = 5 * time.Second

// StartTime is when the first stage of every synthetic run begins.
var StartTime = time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)

// SyntheticRequestBytes is every synthetic request's body size.
const SyntheticRequestBytes = 600

// Stage describes one load stage and the behaviour of the synthetic target in it.
type Stage struct {
	OfferedRPS      float64
	DurationSeconds int
	// AchievedRatio is the share of the offered rate that was really sent; 0 means 1.
	AchievedRatio float64
	// TTFT and E2E are the base latencies in seconds. Each request varies by up
	// to +30% around it, so the p95 is about 1.285 x the base.
	TTFT, E2E float64
	// LagBase is the send lag of every request; LagGrowth is added linearly
	// across the stage, from none at its start to the full value at its end.
	LagBase, LagGrowth float64
	// ErrorEvery makes every n-th request fail with a 500; 0 means none.
	ErrorEvery int
	// UnknownLag makes every record report no schedule, as inference-perf does:
	// a NaN send lag and a zero scheduled_at. UnknownLagEvery does it to every
	// n-th record only.
	UnknownLag      bool
	UnknownLagEvery int
}

// Scenario is one synthetic run.
type Scenario struct {
	Name string
	// ID is the bundle's run id; empty is RunID. Comparisons need several.
	ID     string
	Stages []Stage
	// SLOs and Knobs go into the RunSpec as they are.
	SLOs  map[string]any
	Knobs map[string]any
	// Pacing is the load.pacing of the RunSpec; empty is constant. Records follow
	// the pacer's own count, so a linear or sine stage offers what its pacer says.
	Pacing spec.Pacing
	// SineAmplitude and SinePeriodSeconds fill load.sine for sine pacing.
	SineAmplitude     float64
	SinePeriodSeconds int
	// Validity overrides analysis.stage_validity when non-nil.
	Validity map[string]any
	// Targets is prometheus/targets.json; nil leaves it out.
	Targets []spec.ScrapedJob
	// Signals gives a signal's value in each stage; a stage beyond the end takes
	// the last value. Each is rendered as the real catalog entries it is derived
	// from, spread over several pods (see raw.go), and is collected over the
	// whole run. Preemptions is the increase during each stage, reached
	// gradually through it, so it falls inside the stage and not on its trimmed
	// edge.
	Signals map[input.Signal][]float64
	// QueueMeanSeconds is the LiteLLM mean queue time in each stage, rendered as
	// a cumulative sum and count histogram.
	QueueMeanSeconds []float64
	// Gaps drops the samples of a catalog entry between two offsets from the run start.
	Gaps map[input.SeriesRef][2]time.Duration
	// Queries is prometheus/queries.json; nil leaves it out.
	Queries  *spec.PrometheusQueries
	Pre      *spec.TargetSnapshot
	Post     *spec.TargetSnapshot
	Warnings []spec.StageLengthWarning
	// Bifrost makes the target a Bifrost router in front of a mock upstream
	// (bifrost.go); nil is the vLLM target.
	Bifrost *BifrostTarget
}

// stageWindow returns the window of stage i: stages follow each other with no gap.
func (s Scenario) stageWindow(index int) input.Stage {
	start := StartTime
	for _, earlier := range s.Stages[:index] {
		start = start.Add(time.Duration(earlier.DurationSeconds) * time.Second)
	}
	return input.Stage{
		Index: index, OfferedRPS: s.Stages[index].OfferedRPS,
		Start: start, End: start.Add(time.Duration(s.Stages[index].DurationSeconds) * time.Second),
	}
}

// Phases returns phases.json's content: a warmup, then the stages.
func (s Scenario) Phases() spec.Phases {
	warmupStart := StartTime.Add(-30 * time.Second)
	phases := spec.Phases{Phases: []spec.Phase{{
		Name: "warmup", Kind: spec.PhaseKindWarmup,
		Start: spec.NewUTCTimestamp(warmupStart), End: spec.NewUTCTimestamp(StartTime),
	}}, Warnings: s.Warnings}
	for i := range s.Stages {
		window := s.stageWindow(i)
		index, offered := i, window.OfferedRPS
		phases.Phases = append(phases.Phases, spec.Phase{
			Name: fmt.Sprintf("stage-%d", i), Kind: spec.PhaseKindStage, StageIndex: &index, OfferedRps: &offered,
			Start: spec.NewUTCTimestamp(window.Start), End: spec.NewUTCTimestamp(window.End),
		})
	}
	return phases
}

// RunSpecDocument returns spec.resolved.json's content.
func (s Scenario) RunSpecDocument() map[string]any {
	stages := make([]map[string]any, len(s.Stages))
	for i, stage := range s.Stages {
		stages[i] = map[string]any{"rate": stage.OfferedRPS, "duration": fmt.Sprintf("%ds", stage.DurationSeconds)}
	}
	document := map[string]any{
		"apiVersion": "servtuner.dev/v2", "kind": "RunSpec", "name": s.Name,
		// A Kubernetes target, as the snapshot (two vLLM pods in namespace servtuner) says.
		"target": map[string]any{
			"type": "vllm", "endpoint": "http://target.invalid:8000", "model": "example/model-8b", "tls": map[string]any{"enabled": false},
			"workload": map[string]any{"namespace": "servtuner", "selector": map[string]any{"app": "vllm"}},
		},
		"engine": map[string]any{"type": "loadgen"},
		"workload": map[string]any{"streaming": true, "dataset": map[string]any{
			"type": "synthetic", "input_tokens": map[string]any{"mean": 128}, "output_tokens": map[string]any{"mean": 64},
		}},
		"load": map[string]any{"stages": stages},
	}
	if s.Bifrost != nil {
		document["target"] = s.Bifrost.targetDocument()
	}
	if s.SLOs != nil {
		document["slos"] = s.SLOs
	}
	if s.Knobs != nil {
		document["knobs"] = s.Knobs
	}
	if s.Pacing != "" {
		load := document["load"].(map[string]any)
		load["pacing"] = string(s.Pacing)
		if s.Pacing == spec.PacingSine {
			load["sine"] = map[string]any{"amplitude_ratio": s.SineAmplitude, "period": fmt.Sprintf("%ds", s.SinePeriodSeconds)}
		}
	}
	if s.Validity != nil {
		document["analysis"] = map[string]any{"stage_validity": s.Validity}
	}
	return document
}

// RunSpec parses RunSpecDocument.
func (s Scenario) RunSpec() (spec.RunSpec, error) {
	encoded, err := json.Marshal(s.RunSpecDocument())
	if err != nil {
		return spec.RunSpec{}, err
	}
	return spec.ParseRunSpec(encoded)
}

// Records returns the request records of the run, ordered by stage and time.
func (s Scenario) Records() []engine.RequestRow {
	runSpec, err := s.RunSpec()
	if err != nil {
		panic(fmt.Sprintf("scenario %s: %v", s.Name, err))
	}
	pacers, err := pacer.ForLoad(runSpec.Load, 0)
	if err != nil {
		panic(fmt.Sprintf("scenario %s: %v", s.Name, err))
	}
	var rows []engine.RequestRow
	for i, stage := range s.Stages {
		rows = append(rows, s.stageRecords(i, stage, pacer.OfferedHits(pacers[i]))...)
	}
	return rows
}

func (s Scenario) stageRecords(index int, stage Stage, offeredHits float64) []engine.RequestRow {
	window := s.stageWindow(index)
	ratio := stage.AchievedRatio
	if ratio == 0 {
		ratio = 1
	}
	count := int(math.Round(offeredHits * ratio))
	period := time.Duration(int64(time.Duration(stage.DurationSeconds) * time.Second / time.Duration(max(count, 1))))
	rows := make([]engine.RequestRow, 0, count)
	for i := 0; i < count; i++ {
		scheduled := window.Start.Add(time.Duration(i) * period)
		lag := stage.LagBase + stage.LagGrowth*float64(i)/float64(count)
		sent := scheduled.Add(time.Duration(math.Round(lag*1e6)) * time.Microsecond)
		ttft := microseconds(stage.TTFT * (1 + variation(i, 1)))
		e2e := microseconds(stage.E2E * (1 + variation(i, 2)))
		inputTokens, outputTokens, requestBytes := int64(128), int64(64), int64(SyntheticRequestBytes)
		sendLag, stageIndex := microseconds(lag), int64(index)
		row := engine.RequestRow{
			ScheduledAt: &scheduled, SentAt: sent, SendLagSeconds: &sendLag,
			TTFTSeconds: &ttft, E2ESeconds: e2e, InputTokens: &inputTokens, OutputTokens: &outputTokens, RequestBytes: &requestBytes,
			Status: 200, Phase: engine.RowPhaseStage, StageIndex: &stageIndex,
		}
		if stage.UnknownLag || (stage.UnknownLagEvery > 0 && (i+1)%stage.UnknownLagEvery == 0) {
			row.ScheduledAt, row.SendLagSeconds = nil, nil
		}
		if stage.ErrorEvery > 0 && (i+1)%stage.ErrorEvery == 0 {
			class := "upstream_error"
			row.Status, row.Error, row.TTFTSeconds = 500, &class, nil
		}
		rows = append(rows, row)
	}
	return rows
}

// variation is a deterministic spread in [0, 0.3): a golden-ratio sequence,
// which covers the interval evenly without a random generator.
func variation(i, salt int) float64 {
	fraction := math.Mod(float64(i+1)*0.6180339887498949*float64(salt)+float64(salt)*0.137, 1)
	return 0.3 * fraction
}

func microseconds(seconds float64) float64 { return math.Round(seconds*1e6) / 1e6 }

// Run assembles the scenario as the in-memory input of the analyzer.
func (s Scenario) Run() (input.Run, error) {
	runSpec, err := s.RunSpec()
	if err != nil {
		return input.Run{}, err
	}
	run := input.Run{
		ID: s.RunID(), Spec: runSpec, Phases: s.Phases(), Records: input.SliceRecords(s.Records()),
		Matrix: s.Matrix(), BenchPod: BenchPod, Queries: s.Queries, PreSnapshot: s.Pre, PostSnapshot: s.Post,
	}
	if s.Pre != nil {
		if run.CapturedKnobs, err = capturedKnobs(runSpec, s.Pre); err != nil {
			return input.Run{}, err
		}
	}
	if s.Targets != nil {
		run.Targets = &spec.PrometheusTargets{FetchedAt: spec.NewUTCTimestamp(StartTime), Targets: s.Targets}
	}
	return run, nil
}

// valueAt is a per-stage list evaluated at a time. A counter's values are the
// increase during each stage; the result is the cumulative value, rising
// linearly through each stage.
func (s Scenario) valueAt(values []float64, at time.Time, isCounter bool) float64 {
	index := 0
	for i := range s.Stages {
		if !at.Before(s.stageWindow(i).Start) {
			index = i
		}
	}
	valueOf := func(stage int) float64 { return values[min(stage, len(values)-1)] }
	if !isCounter {
		return valueOf(index)
	}
	var total float64
	for stage := 0; stage < index; stage++ {
		total += valueOf(stage)
	}
	window := s.stageWindow(index)
	fraction := min(at.Sub(window.Start).Seconds()/window.Duration().Seconds(), 1)
	return total + valueOf(index)*fraction
}

// RunID is the scenario's run id: ID, or the shared RunID.
func (s Scenario) RunID() string {
	if s.ID != "" {
		return s.ID
	}
	return RunID
}
