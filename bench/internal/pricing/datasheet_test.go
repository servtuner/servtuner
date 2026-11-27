package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// A small hand-written sheet in the datasheet's format (the real one is not
// committed: its terms are not reviewed).
const sheetJSON = `{
  "gpt-4o-mini": {"provider": "openai", "input_cost_per_token": 1.5e-07, "output_cost_per_token": 6e-07, "cache_read_input_token_cost": 7.5e-08, "mode": "chat"},
  "azure/gpt-4o-mini": {"provider": "azure", "input_cost_per_token": 1.65e-07, "output_cost_per_token": 6.6e-07},
  "claude-sonnet-4-5": {"provider": "anthropic", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05},
  "openai/container": {"provider": "openai", "code_interpreter_cost_per_session": 0.03}
}`

func TestLookupMatchesExactThenBareKeysOfTheSameProvider(t *testing.T) {
	sheet, err := ParseDatasheet([]byte(sheetJSON))
	if err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]string{
		"azure/gpt-4o-mini":           "azure/gpt-4o-mini",
		"openai/gpt-4o-mini":          "gpt-4o-mini",
		"gpt-4o-mini":                 "gpt-4o-mini",
		"anthropic/claude-sonnet-4-5": "claude-sonnet-4-5",
	} {
		price, ok := sheet.Lookup(model)
		if !ok || price.Key != want {
			t.Errorf("%s: %+v %v, want key %s", model, price, ok, want)
		}
	}
	for _, model := range []string{"anthropic/gpt-4o-mini", "servtuner-mock/mock-model", "mock-model", "openai/container"} {
		if price, ok := sheet.Lookup(model); ok {
			t.Errorf("%s priced as %+v: no guessing across providers or without token prices", model, price)
		}
	}
}

func TestTheDatasheetURLFollowsTheRunSpecThenBifrost(t *testing.T) {
	custom := "https://prices.example/sheet.json"
	if url, basis := EffectiveDatasheetURL(spec.TokenPricingPolicy{Url: &custom}, nil); url != custom || basis != spec.TokenPricingUrlBasisRunspec {
		t.Errorf("%s %s", url, basis)
	}
	config := []byte(`{"framework_config": {"pricing_url": "https://mirror.example/datasheet", "pricing_sync_interval": 86400}}`)
	if url, basis := EffectiveDatasheetURL(spec.TokenPricingPolicy{}, config); url != "https://mirror.example/datasheet" || basis != spec.TokenPricingUrlBasisBifrostConfig {
		t.Errorf("%s %s", url, basis)
	}
	if url, basis := EffectiveDatasheetURL(spec.TokenPricingPolicy{}, nil); url != DefaultDatasheetURL || basis != spec.TokenPricingUrlBasisDefault {
		t.Errorf("%s %s", url, basis)
	}
}

func TestFetchStoresTheSheetAndRefusesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sheetJSON))
	}))
	defer server.Close()
	body, err := FetchDatasheet(context.Background(), server.Client(), server.URL+"/datasheet")
	if err != nil || !strings.Contains(string(body), "claude-sonnet-4-5") {
		t.Fatalf("%v %s", err, body)
	}
	if _, err := FetchDatasheet(context.Background(), server.Client(), server.URL+"/missing"); err == nil {
		t.Error("a 404 is not a datasheet")
	}
}
