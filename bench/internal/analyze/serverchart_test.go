package analyze

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestTheServerChartDrawsTheSignalsAndEachContainerAgainstItsLimits(t *testing.T) {
	dir := t.TempDir()
	if err := analyzetest.ScenarioNamed("bifrost-concurrency-bound").WriteBundle(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := AnalyzeBundle(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(bundle.ChartServerJSON)))
	if err != nil {
		t.Fatal(err)
	}
	chart, err := spec.Parse[spec.ServerChart](spec.SchemaChartServer, raw)
	if err != nil {
		t.Fatal(err)
	}
	panels := map[string]spec.ServerPanel{}
	for _, panel := range chart.Panels {
		panels[panel.Id] = panel
	}
	if _, ok := panels[string(input.RouterConcurrency)]; !ok {
		t.Errorf("a Bifrost run draws its router concurrency; panels: %v", keys(panels))
	}
	var cpu *spec.ServerPanel
	for id, panel := range panels {
		if panel.Group == spec.PanelGroupResources && panel.Unit == spec.PanelUnitCores && len(id) > 4 && id[:4] == "cpu/" {
			cpu = &panel
		}
	}
	if cpu == nil || len(cpu.Series[0].Points) == 0 {
		t.Fatalf("each container's CPU is drawn; panels: %v", keys(panels))
	}
	if len(cpu.ReferenceLines) == 0 {
		t.Errorf("the captured request or limit is a reference line: %+v", cpu)
	}
	if _, ok := panels[string(input.KVCacheUsage)]; ok {
		t.Error("a Bifrost run has no KV cache series: the panel must be absent, not zero")
	}
}

func keys(panels map[string]spec.ServerPanel) []string {
	var names []string
	for name := range panels {
		names = append(names, name)
	}
	return names
}

func TestTheBusiestPodWinsEachBucket(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	series := []input.Series{
		{Labels: map[string]string{"pod": "a", "container": "c"}, Samples: []input.Sample{{At: start, Value: 1}, {At: start.Add(10 * time.Second), Value: 5}}},
		{Labels: map[string]string{"pod": "b", "container": "c"}, Samples: []input.Sample{{At: start, Value: 3}, {At: start.Add(10 * time.Second), Value: 2}}},
		{Labels: map[string]string{"pod": "other", "container": "c"}, Samples: []input.Sample{{At: start, Value: 99}}},
	}
	points := busiestPerBucket(series, map[string]bool{"a": true, "b": true}, "c", start, start.Add(20*time.Second), 5)
	encoded, _ := json.Marshal(points)
	if len(points) != 2 || points[0].Mean != 3 || points[1].Mean != 5 {
		t.Errorf("bucket 0 is pod b's 3, bucket 2 is pod a's 5, and a pod outside the workload never counts: %s", encoded)
	}
}

// An off-cluster vLLM on a consumer GPU: vLLM counters and histograms plus the
// DCGM series such a card exports (no profiling metrics), all without pod labels.
func TestTheServerChartDrawsVLLMRatesAndTheGPUSeriesThatWereCollected(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	every5s := func(value func(i int) float64) []input.Sample {
		var samples []input.Sample
		for i := 0; i <= 12; i++ {
			samples = append(samples, input.Sample{At: start.Add(time.Duration(i*5) * time.Second), Value: value(i)})
		}
		return samples
	}
	one := func(labels map[string]string, value func(i int) float64) []input.Series {
		return []input.Series{{Labels: labels, Samples: every5s(value)}}
	}
	none := map[string]string{}
	histogram := func(name string, meanSeconds float64) []input.Series {
		return []input.Series{
			{Labels: map[string]string{"__name__": name + "_count"}, Samples: every5s(func(i int) float64 { return float64(i * 50) })},
			{Labels: map[string]string{"__name__": name + "_sum"}, Samples: every5s(func(i int) float64 { return float64(i*50) * meanSeconds })},
		}
	}
	matrix := input.MapMatrix{
		{Catalog: spec.CatalogNameVllm, Name: "generation_tokens_total"}:    one(none, func(i int) float64 { return float64(i * 5000) }),
		{Catalog: spec.CatalogNameVllm, Name: "prompt_tokens_total"}:        one(none, func(i int) float64 { return float64(i * 10000) }),
		{Catalog: spec.CatalogNameVllm, Name: "prefix_cache_queries_total"}: one(none, func(i int) float64 { return float64(i * 10000) }),
		{Catalog: spec.CatalogNameVllm, Name: "prefix_cache_hits_total"}:    one(none, func(i int) float64 { return float64(i * 2500) }),
		{Catalog: spec.CatalogNameVllm, Name: "queue_time_seconds"}:         histogram("vllm:request_queue_time_seconds", 0.02),
		{Catalog: spec.CatalogNameDcgm, Name: "fb_used_bytes"}:              one(none, func(int) float64 { return 6e9 }),
		{Catalog: spec.CatalogNameDcgm, Name: "fb_capacity_bytes"}:          one(none, func(int) float64 { return 8e9 }),
		{Catalog: spec.CatalogNameDcgm, Name: "board_power_watts"}:          one(none, func(i int) float64 { return 100 + float64(i) }),
		{Catalog: spec.CatalogNameDcgm, Name: "sm_clock_hertz"}:             one(none, func(int) float64 { return 1.9e9 }),
	}
	chart, err := buildServerChart(input.Run{Matrix: matrix}, []input.Stage{{Index: 0, Start: start, End: start.Add(65 * time.Second)}})
	if err != nil {
		t.Fatal(err)
	}
	panels := map[string]spec.ServerPanel{}
	for _, panel := range chart.Panels {
		panels[panel.Id] = panel
	}
	mean := func(id string) float64 {
		t.Helper()
		panel, ok := panels[id]
		if !ok {
			t.Fatalf("panel %s missing; panels: %v", id, keys(panels))
		}
		return panel.Series[0].Points[len(panel.Series[0].Points)-1].Mean
	}
	if got := mean(string(input.GenerationTokens)); got != 1000 {
		t.Errorf("5000 output tokens every 5 s is 1000 tokens/s, got %v", got)
	}
	if got := mean(string(input.PrefixCacheHits)); got != 0.25 {
		t.Errorf("2500 hits of 10000 queried tokens is a 0.25 hit rate, got %v", got)
	}
	if got := mean(string(input.VLLMQueueSecondsSum)); math.Abs(got-0.02) > 1e-9 {
		t.Errorf("the histogram's sum over count is the 0.02 s mean queue time, got %v", got)
	}
	if panels[string(input.GPUMemoryUsedBytes)].ReferenceLines[0].Value != 8e9 {
		t.Errorf("GPU memory is drawn against the card's capacity: %+v", panels[string(input.GPUMemoryUsedBytes)])
	}
	for _, id := range []input.Signal{input.GPUPowerWatts, input.GPUSMClockHertz} {
		if _, ok := panels[string(id)]; !ok {
			t.Errorf("collected series %s is drawn; panels: %v", id, keys(panels))
		}
	}
	for _, id := range []input.Signal{input.GPUSMActive, input.GPUTensorActive, input.Preemptions, input.VLLMDecodeSecondsSum} {
		if _, ok := panels[string(id)]; ok {
			t.Errorf("%s was not collected: the panel must be absent, not zero", id)
		}
	}
}
