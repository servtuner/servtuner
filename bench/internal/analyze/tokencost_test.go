package analyze

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

const tokenSheet = `{"gpt-4o-mini": {"provider": "openai", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06}}`

var tokenStageStart = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

func tokenRow(stage int64, status int64, input, output *int64) engine.RequestRow {
	return engine.RequestRow{Phase: engine.RowPhaseStage, StageIndex: &stage, Status: status, InputTokens: input, OutputTokens: output}
}

func tokens(n int64) *int64 { return &n }

func tokenRun(targetType spec.TargetType, model string, rows []engine.RequestRow, matrix input.MatrixSource) input.Run {
	return input.Run{
		Spec: spec.RunSpec{
			Target:   spec.Target{Type: targetType, Model: model},
			Analysis: &spec.Analysis{TokenPricing: &spec.TokenPricingPolicy{Source: spec.TokenPricingPolicySourceDatasheet}},
		},
		Records: input.SliceRecords(rows), Datasheet: []byte(tokenSheet), Matrix: matrix,
	}
}

func tokenAnalysis() spec.AnalysisResult {
	return spec.AnalysisResult{Capacity: spec.CapacityModel{SloMax: &spec.SloMaxRps{Rps: 10, ValidStageIndex: 1}}}
}

var tokenStages = []input.Stage{{Index: 1, Start: tokenStageStart, End: tokenStageStart.Add(time.Minute)}}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestTokenCostFromTheDatasheetAtTheSLOMaxStage(t *testing.T) {
	rows := []engine.RequestRow{
		tokenRow(1, 200, tokens(100), tokens(50)),  // 1e-4 + 1e-4 = 2e-4 USD
		tokenRow(1, 200, tokens(300), tokens(150)), // 6e-4 USD
		tokenRow(1, 500, tokens(100), tokens(0)),   // a failure is not billed
		tokenRow(0, 200, tokens(999), tokens(999)), // another stage
	}
	cost, err := buildTokenCost(tokenRun(spec.TargetTypeLitellm, "openai/gpt-4o-mini", rows, nil), tokenAnalysis(), tokenStages)
	if err != nil {
		t.Fatal(err)
	}
	if !cost.IsPriced || *cost.DatasheetKey != "gpt-4o-mini" || *cost.Method != spec.TokenCostMethodDatasheet {
		t.Fatalf("cost %+v", cost)
	}
	// 8e-4 USD over 2 successes is 0.4 USD per 1k; over 200 output tokens, 4 USD per 1M.
	if !near(*cost.UsdPer1KRequests, 0.4) || !near(*cost.UsdPer1MOutputTokens, 4) {
		t.Errorf("per 1k %v, per 1M %v", *cost.UsdPer1KRequests, *cost.UsdPer1MOutputTokens)
	}
}

func TestBifrostsOwnCostIsAuthoritativeAndCrossChecked(t *testing.T) {
	rows := []engine.RequestRow{tokenRow(1, 200, tokens(100), tokens(50)), tokenRow(1, 200, tokens(300), tokens(150))}
	series := input.Series{Labels: map[string]string{"__name__": "bifrost_cost_total", "pod": "bifrost-0"},
		Samples: []input.Sample{{At: tokenStageStart, Value: 1}, {At: tokenStageStart.Add(30 * time.Second), Value: 1.001}}}
	matrix := input.MapMatrix{routerCostSeries[spec.TargetTypeBifrost]: {series}}
	cost, err := buildTokenCost(tokenRun(spec.TargetTypeBifrost, "openai/gpt-4o-mini", rows, matrix), tokenAnalysis(), tokenStages)
	if err != nil {
		t.Fatal(err)
	}
	if *cost.Method != spec.TokenCostMethodRouterMetric || !near(*cost.UsdPer1KRequests, 0.5) || !near(*cost.DatasheetUsdPer1KRequests, 0.4) {
		t.Fatalf("cost %+v", cost)
	}
	// Router 1e-3 USD against servtuner's 8e-4: 20% apart, 0.1 USD per 1k added.
	if !near(*cost.Divergence, 0.2) || !near(*cost.RetryAddedUsdPer1KRequests, 0.1) {
		t.Errorf("divergence %v, retry added %v", *cost.Divergence, *cost.RetryAddedUsdPer1KRequests)
	}
	if !strings.Contains(strings.Join(cost.Approximations, " "), "differ by 20.0%") {
		t.Errorf("approximations %v", cost.Approximations)
	}
}

func TestRequestsWithoutUsageArePricedAtTheMeanAndSaid(t *testing.T) {
	rows := []engine.RequestRow{tokenRow(1, 200, tokens(100), tokens(50)), tokenRow(1, 200, nil, nil)}
	cost, _ := buildTokenCost(tokenRun(spec.TargetTypeLitellm, "openai/gpt-4o-mini", rows, nil), tokenAnalysis(), tokenStages)
	if !near(*cost.UsdPer1KRequests, 0.2) || !strings.Contains(strings.Join(cost.Approximations, " "), "1 of 2 successful requests") {
		t.Errorf("cost %+v %v", cost, cost.Approximations)
	}
}

func TestUnpricedModelsAndMissingSheetsAreNeverFree(t *testing.T) {
	rows := []engine.RequestRow{tokenRow(1, 200, tokens(100), tokens(50))}
	for name, run := range map[string]input.Run{
		"custom model": tokenRun(spec.TargetTypeBifrost, "servtuner-mock/mock-model", rows, nil),
		"no sheet": func() input.Run {
			run := tokenRun(spec.TargetTypeLitellm, "openai/gpt-4o-mini", rows, nil)
			run.Datasheet = nil
			return run
		}(),
	} {
		cost, err := buildTokenCost(run, tokenAnalysis(), tokenStages)
		if err != nil || cost.IsPriced || cost.UsdPer1KRequests != nil || cost.Reason == nil {
			t.Errorf("%s: %+v %v", name, cost, err)
		}
	}
	if cost, _ := buildTokenCost(input.Run{}, tokenAnalysis(), tokenStages); cost != nil {
		t.Error("token_pricing none gives no token cost")
	}
}

// A private API's prices as given: no datasheet, any model name.
func TestManualPricesPriceAModelNoDatasheetLists(t *testing.T) {
	rows := []engine.RequestRow{tokenRow(1, 200, tokens(100), tokens(50)), tokenRow(1, 200, tokens(300), tokens(150))}
	run := tokenRun(spec.TargetTypeOpenaiCompatible, "acme/private-model", rows, nil)
	run.Datasheet = nil
	run.Spec.Analysis.TokenPricing = &spec.TokenPricingPolicy{
		Source: spec.TokenPricingPolicySourceManual,
		Manual: &spec.ManualTokenPrices{UsdPer1MInputTokens: 1, UsdPer1MOutputTokens: 2},
	}
	cost, err := buildTokenCost(run, tokenAnalysis(), tokenStages)
	if err != nil {
		t.Fatal(err)
	}
	// The datasheet test's prices, so the same 0.4 USD per 1k and 4 USD per 1M.
	if !cost.IsPriced || *cost.Method != spec.TokenCostMethodManual || cost.DatasheetKey != nil {
		t.Fatalf("cost %+v", cost)
	}
	if !near(*cost.UsdPer1KRequests, 0.4) || !near(*cost.UsdPer1MOutputTokens, 4) {
		t.Errorf("per 1k %v, per 1M %v", *cost.UsdPer1KRequests, *cost.UsdPer1MOutputTokens)
	}
}

func TestTheDatasheetCostSaysWhereItsPricesCameFromAndWhen(t *testing.T) {
	rows := []engine.RequestRow{tokenRow(1, 200, tokens(100), tokens(50))}
	run := tokenRun(spec.TargetTypeLitellm, "openai/gpt-4o-mini", rows, nil)
	fetched := spec.NewUTCTimestamp(tokenStageStart)
	run.TokenPricing = &spec.TokenPricing{UrlBasis: spec.TokenPricingUrlBasisPriceBook, FetchedAt: fetched}
	cost, err := buildTokenCost(run, tokenAnalysis(), tokenStages)
	if err != nil {
		t.Fatal(err)
	}
	if cost.DatasheetFetchedAt == nil || *cost.DatasheetFetchedAt != fetched || cost.DatasheetSource == nil || *cost.DatasheetSource != spec.TokenCostDatasheetSourcePriceBook {
		t.Errorf("cost %+v", cost)
	}
}
