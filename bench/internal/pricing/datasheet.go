package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// DefaultDatasheetURL is Bifrost's own default pricing source
// (framework/modelcatalog/datasheet/store.go), also what a live Bifrost
// reports as framework_config.pricing_url.
const DefaultDatasheetURL = "https://getbifrost.ai/datasheet"

// maxDatasheetBytes caps the download: the sheet was about 5 MB in November
// 2026, so a far larger answer is not a datasheet.
const maxDatasheetBytes = 67108864 // 64 MiB

// EffectiveDatasheetURL is the URL the run fetches, and why: the RunSpec's
// token_pricing.url, else the Bifrost target's configured pricing_url (from
// its captured config document), else Bifrost's default.
func EffectiveDatasheetURL(policy spec.TokenPricingPolicy, bifrostConfig []byte) (string, spec.TokenPricingUrlBasis) {
	if policy.Url != nil && *policy.Url != "" {
		return *policy.Url, spec.TokenPricingUrlBasisRunspec
	}
	var config struct {
		FrameworkConfig struct {
			PricingURL string `json:"pricing_url"`
		} `json:"framework_config"`
	}
	if len(bifrostConfig) > 0 && json.Unmarshal(bifrostConfig, &config) == nil && config.FrameworkConfig.PricingURL != "" {
		return config.FrameworkConfig.PricingURL, spec.TokenPricingUrlBasisBifrostConfig
	}
	return DefaultDatasheetURL, spec.TokenPricingUrlBasisDefault
}

// FetchDatasheet downloads the sheet. The caller stores the bytes as they came.
func FetchDatasheet(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDatasheetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if len(body) > maxDatasheetBytes {
		return nil, fmt.Errorf("%s is over %d bytes", url, maxDatasheetBytes)
	}
	return body, nil
}

// Datasheet is the part of a model-price sheet servtuner reads. The field names
// are LiteLLM's model-price format, which Bifrost's sheet uses too.
type Datasheet map[string]datasheetEntry

type datasheetEntry struct {
	Provider           string   `json:"provider"`
	InputCostPerToken  *float64 `json:"input_cost_per_token"`
	OutputCostPerToken *float64 `json:"output_cost_per_token"`
}

// ParseDatasheet reads a sheet; entries without token prices are kept and
// simply never match a lookup.
func ParseDatasheet(raw []byte) (Datasheet, error) {
	var sheet Datasheet
	if err := json.Unmarshal(raw, &sheet); err != nil {
		return nil, fmt.Errorf("parse datasheet: %w", err)
	}
	return sheet, nil
}

// ModelPrice is a model's base token prices.
type ModelPrice struct {
	Key                           string
	InputPerToken, OutputPerToken float64
}

// Lookup finds a model's prices: the exact key ("azure/gpt-4o-mini"), else,
// for "provider/model", the bare model key whose entry names that provider
// (OpenAI and Anthropic models are keyed bare, "gpt-4o-mini"). It does not
// guess across providers.
func (sheet Datasheet) Lookup(model string) (ModelPrice, bool) {
	if price, ok := sheet.priced(model); ok {
		return price, true
	}
	provider, bare, hasProvider := strings.Cut(model, "/")
	if !hasProvider {
		return ModelPrice{}, false
	}
	entry, ok := sheet[bare]
	if !ok || !strings.EqualFold(entry.Provider, provider) {
		return ModelPrice{}, false
	}
	return sheet.priced(bare)
}

func (sheet Datasheet) priced(key string) (ModelPrice, bool) {
	entry, ok := sheet[key]
	if !ok || entry.InputCostPerToken == nil || entry.OutputCostPerToken == nil {
		return ModelPrice{}, false
	}
	return ModelPrice{Key: key, InputPerToken: *entry.InputCostPerToken, OutputPerToken: *entry.OutputCostPerToken}, true
}
