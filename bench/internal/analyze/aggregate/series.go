package aggregate

import (
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// ScrapeInterval is the effective scrape interval I.
type ScrapeInterval struct {
	Duration time.Duration
	Source   spec.ScrapeIntervalSource
	// Job names the job that limits resolution; empty when the interval was assumed.
	Job string
}

// ResolveInterval reads I from prometheus/targets.json with the same rule the
// collector used for its query steps (prometheus.ResolveScrapeInterval): the
// slowest of the relevant roles, which are the target and cAdvisor plus the
// role of every other catalog the run queried. Without targets.json the
// interval is assumed.
func ResolveInterval(targets *spec.PrometheusTargets, queries *spec.PrometheusQueries, inKubernetes bool) (ScrapeInterval, error) {
	if targets == nil {
		return fromPrometheus(prometheus.AssumedInterval), nil
	}
	var requested []spec.CatalogName
	if queries != nil {
		seen := map[spec.CatalogName]bool{}
		for _, query := range queries.Queries {
			if query.Catalog != spec.CatalogNameExtra && !seen[query.Catalog] {
				seen[query.Catalog] = true
				requested = append(requested, query.Catalog)
			}
		}
	}
	roles, err := prometheus.RelevantRoles(inKubernetes, requested...)
	if err != nil {
		return ScrapeInterval{}, err
	}
	return fromPrometheus(prometheus.ResolveScrapeInterval(*targets, roles).ScrapeInterval), nil
}

func fromPrometheus(interval prometheus.ScrapeInterval) ScrapeInterval {
	return ScrapeInterval{Duration: interval.Interval, Source: interval.Source, Job: interval.Job}
}

// SeriesKind says how a series is attributed to a stage, which decides how its
// edges are trimmed.
type SeriesKind int

const (
	// Gauge series (memory, KV cache, queue length, ratios): the first I of the
	// stage still holds the previous stage's load, so it is dropped.
	Gauge SeriesKind = iota
	// Derived series (rates, histogram means, quantiles): they are attributed
	// by request finish time, so one mean E2E is trimmed from each edge
	// (Rule B).
	Derived
)

// TrimWindow returns the part of a stage that server-side statistics are
// computed over. ok is false when trimming leaves nothing, so a stage too
// short for the series yields no statistic instead of a misleading one.
func TrimWindow(stage input.Stage, kind SeriesKind, interval, meanE2E time.Duration) (start, end time.Time, ok bool) {
	start, end = stage.Start, stage.End
	switch kind {
	case Gauge:
		start = start.Add(interval)
	case Derived:
		start = start.Add(meanE2E)
		end = end.Add(-meanE2E)
	}
	return start, end, end.After(start)
}

// WindowStats summarises the samples of one series inside a window.
type WindowStats struct {
	Count int
	Mean  float64
	P95   float64
	Min   float64
	Max   float64
	// PositiveFraction is the share of samples above zero.
	PositiveFraction float64
}

// StatsInWindow returns the statistics of the samples in [start, end). The end
// is exclusive because a sample taken at the instant a stage ends belongs to the
// next stage.
func StatsInWindow(samples []input.Sample, start, end time.Time) (WindowStats, bool) {
	var values []float64
	var positive int
	for _, sample := range samples {
		if sample.At.Before(start) || !sample.At.Before(end) {
			continue
		}
		values = append(values, sample.Value)
		if sample.Value > 0 {
			positive++
		}
	}
	if len(values) == 0 {
		return WindowStats{}, false
	}
	sorted := stats.SortedCopy(values)
	return WindowStats{
		Count:            len(values),
		Mean:             Mean(values),
		P95:              stats.NearestRank(sorted, 0.95),
		Min:              sorted[0],
		Max:              sorted[len(sorted)-1],
		PositiveFraction: float64(positive) / float64(len(values)),
	}, true
}

// CounterIncrease is how much a cumulative counter grew inside [start, end).
// The last sample before start is the baseline, and a drop is a counter reset,
// after which the new value counts from zero. ok is false with fewer than two
// usable points.
func CounterIncrease(samples []input.Sample, start, end time.Time) (float64, bool) {
	var points []input.Sample
	var baseline *input.Sample
	for i, sample := range samples {
		switch {
		case sample.At.Before(start):
			baseline = &samples[i]
		case sample.At.Before(end):
			points = append(points, sample)
		}
	}
	if baseline != nil {
		points = append([]input.Sample{*baseline}, points...)
	}
	if len(points) < 2 {
		return 0, false
	}
	var increase float64
	for i := 1; i < len(points); i++ {
		delta := points[i].Value - points[i-1].Value
		if delta < 0 {
			delta = points[i].Value
		}
		increase += delta
	}
	return increase, true
}
