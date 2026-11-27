// Package bifrost is the Bifrost advisor (rules B1 to B9): it reads Bifrost's own metrics and config at each usable stage and
// says what to change on the router, or that the router is not the problem.
package bifrost

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/histogram"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

// Catalog entries the advisor reads (bench/internal/prometheus/catalogs/bifrost.yaml).
var (
	upstreamLatency  = input.SeriesRef{Catalog: spec.CatalogNameBifrost, Name: "upstream_latency_seconds"}
	overheadLatency  = input.SeriesRef{Catalog: spec.CatalogNameBifrost, Name: "overhead_latency_microseconds"}
	requestRetries   = input.SeriesRef{Catalog: spec.CatalogNameBifrost, Name: "request_retries"}
	upstreamRequests = input.SeriesRef{Catalog: spec.CatalogNameBifrost, Name: "upstream_requests_total"}
	errorRequests    = input.SeriesRef{Catalog: spec.CatalogNameBifrost, Name: "error_requests_total"}
)

// microsecond converts Bifrost's overhead histogram to seconds.
const microsecond = 1e-6

// StageView is what Bifrost's metrics say about one usable stage.
type StageView struct {
	Index int
	RPS   float64
	// ClientE2EMean is the client's mean E2E, the whole path.
	ClientE2EMean float64
	// Upstream is Bifrost's attempt wall time; Overhead is Bifrost's own cost.
	// Seconds.
	UpstreamMean, UpstreamP95 float64
	OverheadMean, OverheadP95 float64
	HasUpstream, HasOverhead  bool
	// RetriesPerAttempt is retries used over attempts; FallbackShare is the
	// share of upstream attempts with fallback_index > 0.
	RetriesPerAttempt, FallbackShare float64
	HasRetries, HasFallbacks         bool
	// ActiveFraction is the busiest pod's peak active requests over the
	// concurrency knob (the RouterConcurrency signal).
	ActiveFraction float64
	HasActive      bool
	// Dropped is how many requests Bifrost rejected with a full queue
	// (error_type="bifrost_dropped"); HasDropped is false without the label.
	Dropped    float64
	HasDropped bool
}

// ViewStage reads one stage's Bifrost series for the router's pods.
func ViewStage(matrix input.MatrixSource, pods map[string]bool, stage input.Stage, aggregate spec.StageAggregate, activeFraction *float64) (StageView, error) {
	view := StageView{Index: stage.Index, RPS: aggregate.AchievedRps}
	if aggregate.E2E != nil && aggregate.E2E.MeanSeconds != nil {
		view.ClientE2EMean = *aggregate.E2E.MeanSeconds
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
	upstream, err := read(upstreamLatency)
	if err != nil {
		return StageView{}, err
	}
	view.UpstreamMean, view.UpstreamP95, view.HasUpstream = meanAndP95(upstream, stage.Start, stage.End, 1)
	overhead, err := read(overheadLatency)
	if err != nil {
		return StageView{}, err
	}
	view.OverheadMean, view.OverheadP95, view.HasOverhead = meanAndP95(overhead, stage.Start, stage.End, microsecond)
	retries, err := read(requestRetries)
	if err != nil {
		return StageView{}, err
	}
	// _sum is the retries used, _count the attempts (bifrost-metrics.md, Retries).
	view.RetriesPerAttempt, view.HasRetries = histogram.Mean(retries, stage.Start, stage.End)
	attempts, err := read(upstreamRequests)
	if err != nil {
		return StageView{}, err
	}
	view.FallbackShare, view.HasFallbacks = fallbackShare(attempts, stage.Start, stage.End)
	errors, err := read(errorRequests)
	if err != nil {
		return StageView{}, err
	}
	view.Dropped, view.HasDropped = dropped(errors, stage.Start, stage.End)
	if activeFraction != nil {
		view.ActiveFraction, view.HasActive = *activeFraction, true
	}
	return view, nil
}

func meanAndP95(series []input.Series, start, end time.Time, scale float64) (mean, p95 float64, ok bool) {
	mean, hasMean := histogram.Mean(series, start, end)
	p95, hasP95 := histogram.Quantile(series, 0.95, start, end)
	return mean * scale, p95 * scale, hasMean && hasP95
}

func fallbackShare(attempts []input.Series, start, end time.Time) (float64, bool) {
	var fallbacks []input.Series
	for _, series := range attempts {
		if index := series.Labels["fallback_index"]; index != "" && index != "0" {
			fallbacks = append(fallbacks, series)
		}
	}
	total, ok := histogram.Increase(attempts, "", start, end)
	if !ok || total <= 0 {
		return 0, false
	}
	extra, _ := histogram.Increase(fallbacks, "", start, end)
	return extra / total, true
}

// dropped counts the stage's queue-full rejections. Releases that do not label
// errors by error_type (v1.5) give no answer rather than zero.
func dropped(errors []input.Series, start, end time.Time) (float64, bool) {
	var drops []input.Series
	labelled := false
	for _, series := range errors {
		errorType, has := series.Labels["error_type"]
		labelled = labelled || has
		if errorType == "bifrost_dropped" {
			drops = append(drops, series)
		}
	}
	if !labelled {
		return 0, false
	}
	count, _ := histogram.Increase(drops, "", start, end)
	return count, true
}

// Config is what the advisor reads from Bifrost's captured config.
type Config struct {
	EnableLogging         *bool
	DisableContentLogging *bool
	DropExcessRequests    *bool
	Providers             []ProviderConfig
}

// ProviderConfig is one provider's settings that rules look at.
type ProviderConfig struct {
	Name                    string
	MaxRetries              *float64
	SendBackRawRequest      *bool
	SendBackRawResponse     *bool
	StoreRawRequestResponse *bool
	Concurrency, BufferSize *float64
}

// ParseConfig reads the redacted config and providers documents
// (target/config/pre-bifrost-config.json and -providers.json). A document that
// is absent leaves its fields unset.
func ParseConfig(documents map[string][]byte) (Config, error) {
	var config Config
	if raw, ok := documents[target.BifrostConfigDocument]; ok {
		var document struct {
			ClientConfig struct {
				EnableLogging         *bool `json:"enable_logging"`
				DisableContentLogging *bool `json:"disable_content_logging"`
				DropExcessRequests    *bool `json:"drop_excess_requests"`
			} `json:"client_config"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", target.BifrostConfigDocument, err)
		}
		config.EnableLogging = document.ClientConfig.EnableLogging
		config.DisableContentLogging = document.ClientConfig.DisableContentLogging
		config.DropExcessRequests = document.ClientConfig.DropExcessRequests
	}
	if raw, ok := documents[target.BifrostProvidersDocument]; ok {
		var document struct {
			Providers []struct {
				Name                    string `json:"name"`
				SendBackRawRequest      *bool  `json:"send_back_raw_request"`
				SendBackRawResponse     *bool  `json:"send_back_raw_response"`
				StoreRawRequestResponse *bool  `json:"store_raw_request_response"`
				Sizing                  struct {
					Concurrency *float64 `json:"concurrency"`
					BufferSize  *float64 `json:"buffer_size"`
				} `json:"concurrency_and_buffer_size"`
				Network struct {
					MaxRetries *float64 `json:"max_retries"`
				} `json:"network_config"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", target.BifrostProvidersDocument, err)
		}
		for _, provider := range document.Providers {
			config.Providers = append(config.Providers, ProviderConfig{
				Name: provider.Name, MaxRetries: provider.Network.MaxRetries,
				SendBackRawRequest: provider.SendBackRawRequest, SendBackRawResponse: provider.SendBackRawResponse,
				StoreRawRequestResponse: provider.StoreRawRequestResponse,
				Concurrency:             provider.Sizing.Concurrency, BufferSize: provider.Sizing.BufferSize,
			})
		}
	}
	return config, nil
}
