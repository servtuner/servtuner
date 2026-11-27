package histogram

import (
	"math"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
)

var start = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// counter is a cumulative series sampled every 5s from start−10s.
func counter(name string, labels map[string]string, values ...float64) input.Series {
	all := map[string]string{nameLabel: name}
	for key, value := range labels {
		all[key] = value
	}
	samples := make([]input.Sample, len(values))
	for i, value := range values {
		samples[i] = input.Sample{At: start.Add(time.Duration(i*5-10) * time.Second), Value: value}
	}
	return input.Series{Labels: all, Samples: samples}
}

func TestIncreaseSumsSeriesAndSurvivesAReset(t *testing.T) {
	series := []input.Series{
		// Samples at −10s, −5s, 0, 5s and 10s; the value at the start is the baseline.
		counter("x_count", map[string]string{"pod": "a"}, 100, 110, 120, 130, 140), // +20
		counter("x_count", map[string]string{"pod": "b"}, 50, 60, 70, 5, 15),       // reset inside: +5 +10
		counter("x_sum", nil, 0, 1000, 2000, 3000, 4000),                           // another suffix
	}
	got, ok := Increase(series, countSuffix, start, start.Add(15*time.Second))
	if !ok || got != 20+15 {
		t.Errorf("increase = %v, %v; want 35", got, ok)
	}
}

func TestASeriesBornInsideTheWindowCountsFromZero(t *testing.T) {
	born := input.Series{Labels: map[string]string{nameLabel: "x_count"}, Samples: []input.Sample{
		{At: start.Add(5 * time.Second), Value: 7}, {At: start.Add(10 * time.Second), Value: 9},
	}}
	if got, _ := Increase([]input.Series{born}, countSuffix, start, start.Add(time.Minute)); got != 9 {
		t.Errorf("increase = %v, want 9", got)
	}
}

func TestMeanAndQuantile(t *testing.T) {
	bucket := func(bound string, values ...float64) input.Series {
		return counter("lat_bucket", map[string]string{"le": bound}, values...)
	}
	// 100 observations in the window: 50 ≤ 0.1s, 40 in (0.1, 0.5], 10 in (0.5, 1].
	series := []input.Series{
		bucket("0.1", 0, 0, 0, 50), bucket("0.5", 0, 0, 0, 90), bucket("1", 0, 0, 0, 100), bucket("+Inf", 0, 0, 0, 100),
		counter("lat_sum", nil, 0, 0, 0, 25), counter("lat_count", nil, 0, 0, 0, 100),
	}
	window := start.Add(time.Minute)
	if mean, ok := Mean(series, start, window); !ok || mean != 0.25 {
		t.Errorf("mean = %v, %v", mean, ok)
	}
	for q, want := range map[float64]float64{0.5: 0.1, 0.7: 0.3, 0.95: 0.75} {
		if got, ok := Quantile(series, q, start, window); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("q%v = %v, %v; want %v", q, got, ok, want)
		}
	}
	if count, _ := Count(series, start, window); count != 100 {
		t.Errorf("count = %v", count)
	}
}

func TestNoObservationsMeansNoStatistic(t *testing.T) {
	flat := []input.Series{counter("lat_bucket", map[string]string{"le": "+Inf"}, 5, 5, 5), counter("lat_count", nil, 5, 5, 5), counter("lat_sum", nil, 1, 1, 1)}
	if _, ok := Mean(flat, start, start.Add(time.Minute)); ok {
		t.Error("a mean of nothing")
	}
	if _, ok := Quantile(flat, 0.9, start, start.Add(time.Minute)); ok {
		t.Error("a quantile of nothing")
	}
}
