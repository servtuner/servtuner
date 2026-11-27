// Package litellm is the LiteLLM advisor (rules L1 to L5 and X1). It reads LiteLLM's own metrics at each usable stage and says
// what to change on the proxy, or that the proxy is not the problem. Metric
// and knob names were verified on a live v1.108.3 proxy.
package litellm

import (
	"fmt"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/histogram"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Catalog entries the advisor reads (bench/internal/prometheus/catalogs/litellm.yaml).
var (
	queueTime          = input.SeriesRef{Catalog: spec.CatalogNameLitellm, Name: "request_queue_time_seconds"}
	overheadLatency    = input.SeriesRef{Catalog: spec.CatalogNameLitellm, Name: "overhead_latency_seconds"}
	upstreamLatency    = input.SeriesRef{Catalog: spec.CatalogNameLitellm, Name: "llm_api_latency_seconds"}
	proxyRequests      = input.SeriesRef{Catalog: spec.CatalogNameLitellm, Name: "proxy_total_requests_total"}
	deploymentRequests = input.SeriesRef{Catalog: spec.CatalogNameLitellm, Name: "deployment_total_requests_total"}
)

// StageView is what LiteLLM's metrics say about one usable stage.
type StageView struct {
	Index int
	RPS   float64
	// ClientE2EMean is the client's mean E2E; ClientErrorRate the share of
	// the stage's requests that failed, from the client's records.
	ClientE2EMean, ClientErrorRate float64
	// QueueP95 is litellm_request_queue_time_seconds: arrival to the start of
	// pre-call processing (auth and ASGI queueing). Seconds.
	QueueP95 float64
	HasQueue bool
	// Overhead is LiteLLM's own processing (seconds, whatever its help text
	// says); it is observed for non-streaming requests only.
	OverheadMean float64
	HasOverhead  bool
	// UpstreamMean is the LLM API call's duration.
	UpstreamMean float64
	HasUpstream  bool
	// AttemptsPerRequest is upstream deployment attempts over the requests
	// the proxy received: retries and fallbacks push it above 1.
	AttemptsPerRequest float64
	HasAttempts        bool
}

// ViewStage reads one stage's LiteLLM series for the proxy's pods.
func ViewStage(matrix input.MatrixSource, pods map[string]bool, stage input.Stage, aggregate spec.StageAggregate) (StageView, error) {
	view := StageView{Index: stage.Index, RPS: aggregate.AchievedRps}
	if aggregate.E2E != nil && aggregate.E2E.MeanSeconds != nil {
		view.ClientE2EMean = *aggregate.E2E.MeanSeconds
	}
	if aggregate.Requests > 0 {
		view.ClientErrorRate = float64(aggregate.Errors) / float64(aggregate.Requests)
	}
	read := func(ref input.SeriesRef) ([]input.Series, error) {
		all, _, err := matrix.Matrix(ref)
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", ref.Catalog, ref.Name, err)
		}
		var mine []input.Series
		for _, series := range all {
			if len(pods) == 0 || pods[series.Labels["pod"]] {
				mine = append(mine, series)
			}
		}
		return mine, nil
	}
	queue, err := read(queueTime)
	if err != nil {
		return StageView{}, err
	}
	view.QueueP95, view.HasQueue = histogram.Quantile(queue, 0.95, stage.Start, stage.End)
	overhead, err := read(overheadLatency)
	if err != nil {
		return StageView{}, err
	}
	view.OverheadMean, view.HasOverhead = histogram.Mean(overhead, stage.Start, stage.End)
	upstream, err := read(upstreamLatency)
	if err != nil {
		return StageView{}, err
	}
	view.UpstreamMean, view.HasUpstream = histogram.Mean(upstream, stage.Start, stage.End)
	received, err := read(proxyRequests)
	if err != nil {
		return StageView{}, err
	}
	attempts, err := read(deploymentRequests)
	if err != nil {
		return StageView{}, err
	}
	view.AttemptsPerRequest, view.HasAttempts = ratioOfIncreases(attempts, received, stage.Start, stage.End)
	return view, nil
}

func ratioOfIncreases(numerator, denominator []input.Series, start, end time.Time) (float64, bool) {
	below, ok := histogram.Increase(denominator, "", start, end)
	if !ok || below <= 0 {
		return 0, false
	}
	above, ok := histogram.Increase(numerator, "", start, end)
	if !ok {
		return 0, false
	}
	return above / below, true
}
