package target

import (
	"reflect"
	"testing"
)

func TestSamplesOfParsesLabelsWithEscapes(t *testing.T) {
	samples := samplesOf(readFixture(t, "vllm-metrics.txt"), vllmCacheConfigMetric)
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want 2", len(samples))
	}
	if got := samples[1]["note"]; got != `a "quoted" value` {
		t.Errorf("escaped label = %q", got)
	}
	if got := samples[1]["engine"]; got != "0" {
		t.Errorf("engine = %q", got)
	}
}

func TestCacheConfigLabelsPrefersAnEngineRowAndDropsTheEngineLabel(t *testing.T) {
	samples := samplesOf(readFixture(t, "vllm-metrics.txt"), vllmCacheConfigMetric)
	labels := cacheConfigLabels(samples)
	if labels["kv_cache_size_tokens"] != "65536" {
		t.Errorf("did not choose the engine row: %v", labels)
	}
	if _, has := labels["engine"]; has {
		t.Error("engine label was kept")
	}
	fallback := cacheConfigLabels(samples[:1])
	if !reflect.DeepEqual(fallback["num_gpu_blocks"], "4096") {
		t.Errorf("engine-less fallback = %v", fallback)
	}
	if cacheConfigLabels(nil) != nil {
		t.Error("no samples must give nil")
	}
}
