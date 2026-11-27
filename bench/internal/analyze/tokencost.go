package analyze

import (
	"fmt"
	"math"

	"github.com/servtuner/servtuner/bench/internal/analyze/histogram"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/signals"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/pricing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Token cost thresholds.
const (
	// A divergence between Bifrost's own cost and servtuner's calculation above
	// this share of the router's figure is reported.
	tokenCostDivergence = 0.05
	perThousand         = 1000
	perMillion          = 1e6
)

// The router's own cost counter per target type: Bifrost's bifrost_cost_total
// and LiteLLM's litellm_spend_metric_total, both USD.
var routerCostSeries = map[spec.TargetType]input.SeriesRef{
	spec.TargetTypeBifrost: {Catalog: spec.CatalogNameBifrost, Name: "cost_total"},
	spec.TargetTypeLitellm: {Catalog: spec.CatalogNameLitellm, Name: "spend_total"},
}

// tokenCostApproximations lists what servtuner's own calculation leaves out.
var tokenCostApproximations = []string{
	"Base input and output prices only: cache reads, long-context tiers, batch, flex and priority prices are not modelled.",
}

// buildTokenCost prices the requests at the SLO-max stage. It returns
// nil when the RunSpec's token_pricing is none.
func buildTokenCost(run input.Run, analysis spec.AnalysisResult, stages []input.Stage) (*spec.TokenCost, error) {
	if run.Spec.Analysis == nil || run.Spec.Analysis.TokenPricing == nil {
		return nil, nil
	}
	policy := run.Spec.Analysis.TokenPricing
	if policy.Source != spec.TokenPricingPolicySourceDatasheet && policy.Source != spec.TokenPricingPolicySourceManual {
		return nil, nil
	}
	cost := &spec.TokenCost{Model: run.Spec.Target.Model, Approximations: append([]string{}, tokenCostApproximations...)}
	unpriced := func(reason string) (*spec.TokenCost, error) {
		cost.Reason = &reason
		return cost, nil
	}
	var price pricing.ModelPrice
	method := spec.TokenCostMethodDatasheet
	if policy.Source == spec.TokenPricingPolicySourceManual {
		// The RunSpec's own prices: every model the requests name costs the same.
		if policy.Manual == nil {
			return unpriced("token_pricing is manual but names no prices")
		}
		price = pricing.ModelPrice{
			InputPerToken:  policy.Manual.UsdPer1MInputTokens / perMillion,
			OutputPerToken: policy.Manual.UsdPer1MOutputTokens / perMillion,
		}
		method = spec.TokenCostMethodManual
	} else {
		if run.Datasheet == nil {
			return unpriced("the run has no pricing/datasheet.json: servtuner run fetches it at the start, and its fetch failed or was skipped (an offline run never fetches the public one: upload a datasheet in the price book)")
		}
		sheet, err := pricing.ParseDatasheet(run.Datasheet)
		if err != nil {
			return unpriced(fmt.Sprintf("the bundled datasheet does not parse: %v", err))
		}
		found, ok := sheet.Lookup(run.Spec.Target.Model)
		if !ok {
			return unpriced(fmt.Sprintf("%q has no token prices in the datasheet: a self-hosted or custom model is unpriced, never free", run.Spec.Target.Model))
		}
		price = found
		cost.DatasheetKey = &price.Key
		if meta := run.TokenPricing; meta != nil {
			fetchedAt, source := meta.FetchedAt, spec.TokenCostDatasheetSource(meta.UrlBasis)
			cost.DatasheetFetchedAt, cost.DatasheetSource = &fetchedAt, &source
		}
	}
	sloMax := analysis.Capacity.SloMax
	if sloMax == nil {
		return unpriced("no stage is valid and meets the SLOs, so there is no stage to price")
	}
	index := sloMax.ValidStageIndex
	cost.StageIndex = &index
	usage, err := stageUsage(run.Records, index)
	if err != nil {
		return nil, err
	}
	if usage.successes == 0 {
		return unpriced(fmt.Sprintf("stage %d has no successful request", index))
	}
	if usage.withUsage == 0 {
		return unpriced(fmt.Sprintf("no successful request in stage %d reported token usage", index))
	}
	if usage.withUsage < usage.successes {
		cost.Approximations = append(cost.Approximations, fmt.Sprintf(
			"%d of %d successful requests in stage %d reported no usage; they are priced at the others' mean", usage.successes-usage.withUsage, usage.successes, index))
	}
	// Requests without usage are priced at the mean of those with it.
	scale := float64(usage.successes) / float64(usage.withUsage)
	datasheetUSD := (float64(usage.inputTokens)*price.InputPerToken + float64(usage.outputTokens)*price.OutputPerToken) * scale
	outputTokens := float64(usage.outputTokens) * scale
	perRequests := func(usd float64) float64 { return usd / float64(usage.successes) * perThousand }
	datasheetPer1K := perRequests(datasheetUSD)
	cost.IsPriced, cost.Method = true, &method
	cost.UsdPer1KRequests = &datasheetPer1K
	if outputTokens > 0 {
		perMillionTokens := datasheetUSD / outputTokens * perMillion
		cost.UsdPer1MOutputTokens = &perMillionTokens
	}
	routerUSD, hasRouter, err := routerCostIn(run, stages, index)
	if err != nil {
		return nil, err
	}
	if !hasRouter || routerUSD <= 0 {
		return cost, nil
	}
	// The router's own figure is authoritative: it applies its full pricing
	// logic (tiers, cache reads, retries and fallbacks).
	routerMethod := spec.TokenCostMethodRouterMetric
	routerPer1K := perRequests(routerUSD)
	divergence := math.Abs(routerUSD-datasheetUSD) / routerUSD
	retryAdded := perRequests(routerUSD - datasheetUSD)
	cost.Method, cost.UsdPer1KRequests, cost.DatasheetUsdPer1KRequests = &routerMethod, &routerPer1K, &datasheetPer1K
	cost.Divergence, cost.RetryAddedUsdPer1KRequests = &divergence, &retryAdded
	if outputTokens > 0 {
		perMillionTokens := routerUSD / outputTokens * perMillion
		cost.UsdPer1MOutputTokens = &perMillionTokens
	}
	if divergence > tokenCostDivergence {
		cost.Approximations = append(cost.Approximations, fmt.Sprintf(
			"the router's own cost and servtuner's calculation differ by %.1f%%: retries, fallbacks or prices servtuner does not model", 100*divergence))
	}
	return cost, nil
}

type usageTotals struct {
	successes, withUsage      int
	inputTokens, outputTokens int64
}

// stageUsage sums the token usage of a stage's successful requests.
func stageUsage(records input.RecordSource, stageIndex int) (usageTotals, error) {
	var totals usageTotals
	if records == nil {
		return totals, nil
	}
	for row, err := range records.Records() {
		if err != nil {
			return usageTotals{}, fmt.Errorf("read request records: %w", err)
		}
		if row.Phase != engine.RowPhaseStage || row.StageIndex == nil || int(*row.StageIndex) != stageIndex || !isSuccess(row) {
			continue
		}
		totals.successes++
		if row.InputTokens == nil || row.OutputTokens == nil {
			continue
		}
		totals.withUsage++
		totals.inputTokens += *row.InputTokens
		totals.outputTokens += *row.OutputTokens
	}
	return totals, nil
}

// isSuccess is a request the provider would bill: a 2xx with no error.
func isSuccess(row engine.RequestRow) bool {
	return row.Error == nil && row.Status >= 200 && row.Status < 300
}

// routerCostIn is the increase of bifrost_cost_total over the stage, across
// the target's pods.
func routerCostIn(run input.Run, stages []input.Stage, index int) (float64, bool, error) {
	routerCost, ok := routerCostSeries[run.Spec.Target.Type]
	if !ok || run.Matrix == nil {
		return 0, false, nil
	}
	var window *input.Stage
	for i := range stages {
		if stages[i].Index == index {
			window = &stages[i]
		}
	}
	if window == nil {
		return 0, false, nil
	}
	all, _, err := run.Matrix.Matrix(routerCost)
	if err != nil {
		return 0, false, fmt.Errorf("read %s/%s: %w", routerCost.Catalog, routerCost.Name, err)
	}
	// Only the router emits the series, so the run's workload pods select it.
	pods := signals.PodsOf(run.PreSnapshot, run.PostSnapshot)
	var mine []input.Series
	for _, series := range all {
		if len(pods) == 0 || pods[series.Labels["pod"]] {
			mine = append(mine, series)
		}
	}
	usd, ok := histogram.Increase(mine, "", window.Start, window.End)
	return usd, ok, nil
}
