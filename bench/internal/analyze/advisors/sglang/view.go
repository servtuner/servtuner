// Package sglang is the SGLang advisor (rules S1 to S4). It reads SGLang's own
// metrics (bench/internal/prometheus/catalogs/sglang.yaml) at each usable stage
// and says which server flag holds requests back: the KV pool, the running cap,
// retraction, or prefix reuse. Metric names follow the SGLang v0.5.23 source.
package sglang

import (
	"fmt"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/histogram"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Catalog entries the advisor reads.
var (
	runningReqs     = input.SeriesRef{Catalog: spec.CatalogNameSglang, Name: "num_running_reqs"}
	queueReqs       = input.SeriesRef{Catalog: spec.CatalogNameSglang, Name: "num_queue_reqs"}
	tokenUsage      = input.SeriesRef{Catalog: spec.CatalogNameSglang, Name: "token_usage"}
	retractedTotal  = input.SeriesRef{Catalog: spec.CatalogNameSglang, Name: "num_retracted_requests_total"}
	promptTokens    = input.SeriesRef{Catalog: spec.CatalogNameSglang, Name: "prompt_tokens_total"}
	cachedTokens    = input.SeriesRef{Catalog: spec.CatalogNameSglang, Name: "cached_tokens_total"}
	podLabel        = "pod"
	dpRankLabel     = "dp_rank"
	seriesSeparator = "\x00"
)

// StageView is what SGLang's metrics say about one usable stage.
type StageView struct {
	Index int
	RPS   float64
	// RunningPeak and QueuePeak are the most requests running and waiting at
	// once, summed over pods and DP groups (the max over TP ranks within a
	// group, which report the same batch).
	RunningPeak, QueuePeak float64
	HasScheduler           bool
	// TokenUsagePeak is the fullest any KV pool got, 0 to 1.
	TokenUsagePeak float64
	// Retracted is how many running requests were sent back to the queue.
	Retracted    float64
	HasRetracted bool
	// PrefixReuse is cached prompt tokens over prompt tokens in the stage.
	PrefixReuse    float64
	HasPrefixReuse bool
}

// ViewStage reads one stage's SGLang series for the target's pods (all pods
// when the set is empty, as off the cluster).
func ViewStage(matrix input.MatrixSource, pods map[string]bool, stage input.Stage, aggregate spec.StageAggregate) (StageView, error) {
	view := StageView{Index: stage.Index, RPS: aggregate.AchievedRps}
	read := func(ref input.SeriesRef) ([]input.Series, error) {
		all, _, err := matrix.Matrix(ref)
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", ref.Catalog, ref.Name, err)
		}
		var mine []input.Series
		for _, series := range all {
			if len(pods) == 0 || pods[series.Labels[podLabel]] {
				mine = append(mine, series)
			}
		}
		return mine, nil
	}
	running, err := read(runningReqs)
	if err != nil {
		return StageView{}, err
	}
	queue, err := read(queueReqs)
	if err != nil {
		return StageView{}, err
	}
	usage, err := read(tokenUsage)
	if err != nil {
		return StageView{}, err
	}
	var hasRunning, hasQueue bool
	view.RunningPeak, hasRunning = groupPeak(running, stage.Start, stage.End, true)
	view.QueuePeak, hasQueue = groupPeak(queue, stage.Start, stage.End, true)
	view.TokenUsagePeak, _ = groupPeak(usage, stage.Start, stage.End, false)
	view.HasScheduler = hasRunning && hasQueue
	retracted, err := read(retractedTotal)
	if err != nil {
		return StageView{}, err
	}
	view.Retracted, view.HasRetracted = histogram.Increase(dedupeRanks(retracted), "", stage.Start, stage.End)
	prompt, err := read(promptTokens)
	if err != nil {
		return StageView{}, err
	}
	cached, err := read(cachedTokens)
	if err != nil {
		return StageView{}, err
	}
	if total, ok := histogram.Increase(prompt, "", stage.Start, stage.End); ok && total > 0 {
		if hits, ok := histogram.Increase(cached, "", stage.Start, stage.End); ok {
			view.PrefixReuse, view.HasPrefixReuse = hits/total, true
		}
	}
	return view, nil
}

// groupKey is one scheduler group: a pod and its DP rank. TP ranks of a group
// report the same values (enable_metrics_for_all_schedulers), so they are one.
func groupKey(labels map[string]string) string {
	return labels[podLabel] + seriesSeparator + labels[dpRankLabel]
}

// groupPeak is each group's highest sample inside the stage, then the sum over
// groups (sum) or the highest group (not sum, for fractions). Summing group
// peaks bounds the peak of the sum from above; the groups peak together under
// steady load.
func groupPeak(series []input.Series, start, end time.Time, sum bool) (float64, bool) {
	peaks := map[string]float64{}
	for _, one := range series {
		key := groupKey(one.Labels)
		for _, sample := range one.Samples {
			if sample.At.Before(start) || !sample.At.Before(end) {
				continue
			}
			if current, seen := peaks[key]; !seen || sample.Value > current {
				peaks[key] = sample.Value
			}
		}
	}
	if len(peaks) == 0 {
		return 0, false
	}
	result := 0.0
	for _, peak := range peaks {
		if sum {
			result += peak
		} else if peak > result {
			result = peak
		}
	}
	return result, true
}

// dedupeRanks keeps one series per group, so a counter every TP rank exports
// is counted once.
func dedupeRanks(series []input.Series) []input.Series {
	seen := map[string]bool{}
	var kept []input.Series
	for _, one := range series {
		key := groupKey(one.Labels)
		if seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, one)
	}
	return kept
}
