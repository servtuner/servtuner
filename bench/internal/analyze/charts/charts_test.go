package charts

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func row(stage int, sentAfter time.Duration, e2e float64, ttft *float64, status int64) engine.RequestRow {
	index := int64(stage)
	return engine.RequestRow{Phase: engine.RowPhaseStage, StageIndex: &index, SentAt: t0.Add(sentAfter), E2ESeconds: e2e, TTFTSeconds: ttft, Status: status, ITLSeconds: []float64{0.02, 0.03}}
}

func ptr(value float64) *float64 { return &value }

func twoStages() []Stage {
	return []Stage{
		{Window: input.Stage{Index: 0, OfferedRPS: 2, Start: t0, End: t0.Add(3 * time.Second)}, IsValid: true},
		{Window: input.Stage{Index: 1, OfferedRPS: 4, Start: t0.Add(3 * time.Second), End: t0.Add(5 * time.Second)}, IsExcluded: true},
	}
}

func validate(t *testing.T, schema string, document any) {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(schema, encoded); err != nil {
		t.Fatalf("%s breaks its schema: %v\n%s", schema, err, encoded)
	}
}

func TestBucketsCoverTheStagesWithoutGaps(t *testing.T) {
	warmup := row(0, 100*time.Millisecond, 9, nil, 200)
	warmup.Phase = engine.RowPhaseWarmup
	records := input.SliceRecords{
		row(0, 100*time.Millisecond, 0.5, ptr(0.1), 200),
		row(0, 900*time.Millisecond, 0.7, ptr(0.3), 500),
		warmup,
		row(1, 3500*time.Millisecond, 0.9, nil, 200),
	}
	documents, err := Build(twoStages(), records, "test")
	series, histograms := documents.Timeseries, documents.Histograms
	if err != nil {
		t.Fatal(err)
	}
	validate(t, spec.SchemaChartTimeseries, series)
	validate(t, spec.SchemaChartHistograms, histograms)
	if series.BucketSeconds != 1 || len(series.Buckets) != 5 {
		t.Fatalf("width %d, %d buckets; want 1 s and 5 covering 5 s", series.BucketSeconds, len(series.Buckets))
	}
	first := series.Buckets[0]
	if first.Requests != 2 || first.Errors != 1 || first.AchievedRps != 2 {
		t.Errorf("bucket 0: %+v (the warmup row must not count)", first)
	}
	if first.TtftSeconds == nil || first.TtftSeconds.Samples != 2 || first.TtftSeconds.P50 != 0.1 || first.TtftSeconds.P99 != 0.3 {
		t.Errorf("bucket 0 TTFT: %+v", first.TtftSeconds)
	}
	if empty := series.Buckets[1]; empty.Requests != 0 || empty.TtftSeconds != nil || empty.StageIndex == nil || *empty.StageIndex != 0 {
		t.Errorf("an empty bucket stays, in its stage: %+v", empty)
	}
	if b := series.Buckets[3]; b.StageIndex == nil || *b.StageIndex != 1 || b.Requests != 1 || b.TtftSeconds != nil {
		t.Errorf("bucket 3: %+v", b)
	}
	if band := series.Stages[1]; band.StartOffsetSeconds != 3 || band.EndOffsetSeconds != 5 || !band.IsExcluded || band.OfferedRps != 4 {
		t.Errorf("stage band: %+v", band)
	}
	if h := histograms.Stages[1]; h.TtftCounts != nil || sum(h.E2ECounts) != 1 || sum(h.ItlCounts) != 2 {
		t.Errorf("stage 1 histograms: %+v", h)
	}
	if sum(histograms.Stages[0].E2ECounts) != 2 {
		t.Errorf("stage 0 has two E2E observations: %v", histograms.Stages[0].E2ECounts)
	}
}

func sum(counts []int) int {
	total := 0
	for _, c := range counts {
		total += c
	}
	return total
}

func TestBucketWidth(t *testing.T) {
	cases := map[time.Duration]int{
		10 * time.Second:    1,
		600 * time.Second:   1,
		601 * time.Second:   5,
		10000 * time.Second: 5,
		20000 * time.Second: 10,
		20001 * time.Second: 11,
	}
	for duration, want := range cases {
		if got := BucketSeconds(duration); got != want {
			t.Errorf("BucketSeconds(%s) = %d, want %d", duration, got, want)
		}
	}
}

func TestEdgesAndBins(t *testing.T) {
	edges := Edges()
	if len(edges) != 121 || edges[0] != 0.001 || edges[len(edges)-1] < 999.99 || edges[20] < 0.00999 || edges[20] > 0.01001 {
		t.Fatalf("edges: %d from %g to %g", len(edges), edges[0], edges[len(edges)-1])
	}
	if bin(edges, 0.0005) != 0 || bin(edges, edges[0]) != 1 || bin(edges, 5000) != len(edges) {
		t.Errorf("below the first edge is 0, at an edge is the next bucket, above the last is the overflow")
	}
}

func TestRowsBeforeTheFirstStageAreLeftOut(t *testing.T) {
	early := row(0, -2*time.Second, 0.5, nil, 200)
	documents, err := Build(twoStages(), input.SliceRecords{early}, "test")
	series := documents.Timeseries
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range series.Buckets {
		if b.Requests != 0 {
			t.Fatalf("a row sent before the first stage was bucketed: %+v", b)
		}
	}
}

func TestInFlightFollowsLittlesLaw(t *testing.T) {
	stage := []Stage{{Window: input.Stage{Index: 0, OfferedRPS: 10, Start: t0, End: t0.Add(60 * time.Second)}, IsValid: true}}
	var records input.SliceRecords
	for i := range 600 { // 10 RPS, each request 0.5 s
		records = append(records, row(0, time.Duration(i)*100*time.Millisecond, 0.5, nil, 200))
	}
	documents, err := Build(stage, records, "test")
	if err != nil {
		t.Fatal(err)
	}
	validate(t, spec.SchemaChartInFlight, documents.InFlight)
	check := documents.InFlight.Stages[0]
	if check.ArrivalRps != 10 || check.MeanE2ESeconds == nil || *check.MeanE2ESeconds != 0.5 {
		t.Fatalf("λ %v, mean E2E %v", check.ArrivalRps, check.MeanE2ESeconds)
	}
	if check.MeanInFlight == nil || math.Abs(*check.MeanInFlight-5) > 0.5 || *check.LittleError > 0.1 {
		t.Errorf("L = %v (want ~5 = 10 × 0.5), error %v", *check.MeanInFlight, *check.LittleError)
	}
	if documents.InFlight.BucketSeconds != 1 || len(documents.InFlight.Points) != 61 {
		t.Errorf("one point per second over 60 s plus the end: width %d, %d points", documents.InFlight.BucketSeconds, len(documents.InFlight.Points))
	}
}
