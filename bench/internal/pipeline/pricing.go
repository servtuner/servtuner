package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/pricing"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// writeInfraPricing resolves the RunSpec's infrastructure prices once, at the
// start of the run, and writes pricing/infra.json, which the analysis prices
// from. Only a failed bundle write fails the step:
// prices that cannot be resolved leave the run unpriced, with the reason in
// the log and, for aws, in the document's problems.
func (r *run) writeInfraPricing(ctx context.Context) error {
	var policy *spec.InfraPricingPolicy
	if r.runSpec.Analysis != nil {
		policy = r.runSpec.Analysis.InfraPricing
	}
	var prices spec.InfraPricing
	var err error
	switch pricing.Source(policy) {
	case spec.InfraPricingPolicySourceStatic:
		prices, err = pricing.FromStatic(*policy, r.opts.Now())
	case spec.InfraPricingPolicySourceAws:
		if r.opts.Offline {
			err = errOfflineAWS
			break
		}
		prices, err = r.awsPrices(ctx, *policy)
	default:
		return nil
	}
	if err != nil {
		r.logger.Warn("infrastructure prices not resolved", "error", err)
		return nil
	}
	for _, problem := range prices.Problems {
		r.logger.Warn("infrastructure price missing", "problem", problem)
	}
	return r.writer.WriteDocument(ctx, bundle.InfraPricingJSON, spec.SchemaInfraPricing, prices)
}

// errOfflineAWS is why an offline run is never priced from AWS.
var errOfflineAWS = fmt.Errorf("servtuner run --offline: the AWS Price List is not reachable; give static prices (the price book's static source) instead")

func (r *run) awsPrices(ctx context.Context, policy spec.InfraPricingPolicy) (spec.InfraPricing, error) {
	if r.opts.PriceList == nil {
		return spec.InfraPricing{}, fmt.Errorf("no AWS Price List client configured")
	}
	api, err := r.opts.PriceList(ctx)
	if err != nil {
		return spec.InfraPricing{}, fmt.Errorf("AWS Price List client: %w", err)
	}
	return pricing.FromAWS(ctx, api, policy, pricing.InstanceTypes(r.preSnapshot), r.opts.Now())
}

// datasheetTimeout bounds the one datasheet download of a run.
const datasheetTimeout = 60 * time.Second

// writeTokenPricing fetches the token pricing datasheet once, at the start of
// the run, and stores it byte for byte as pricing/datasheet.json with
// pricing/token.json saying where it came from. A
// failed fetch is logged and leaves the run's token cost unpriced; only a
// failed bundle write fails the step.
func (r *run) writeTokenPricing(ctx context.Context, bifrostConfig []byte) error {
	if r.runSpec.Analysis == nil || r.runSpec.Analysis.TokenPricing == nil ||
		r.runSpec.Analysis.TokenPricing.Source != spec.TokenPricingPolicySourceDatasheet {
		return nil
	}
	policy := *r.runSpec.Analysis.TokenPricing
	url, basis := pricing.EffectiveDatasheetURL(policy, bifrostConfig)
	client := r.opts.DatasheetClient
	if client == nil {
		client = &http.Client{Timeout: datasheetTimeout, Transport: http.DefaultTransport.(*http.Transport).Clone()}
	}
	switch {
	case policy.FromPriceBook && r.opts.PriceBook == nil:
		r.logger.Warn("token pricing datasheet not fetched: token_pricing.from_price_book needs servtuner run --api-url, which serves the price book")
		return nil
	case policy.FromPriceBook:
		url, basis, client = r.opts.PriceBook.URL, spec.TokenPricingUrlBasisPriceBook, r.opts.PriceBook.Client
	case basis == spec.TokenPricingUrlBasisDefault && r.opts.Offline:
		r.logger.Warn("token pricing datasheet not fetched: servtuner run --offline never fetches the public datasheet; supply one in the price book", "url", url)
		return nil
	}
	fetchedAt := r.opts.Now()
	raw, err := pricing.FetchDatasheet(ctx, client, url)
	if err != nil {
		r.logger.Warn("token pricing datasheet not fetched", "url", url, "error", err)
		return nil
	}
	sheet, err := pricing.ParseDatasheet(raw)
	if err != nil {
		r.logger.Warn("token pricing datasheet does not parse", "url", url, "error", err)
		return nil
	}
	if err := r.writer.WriteFile(ctx, bundle.DatasheetJSON, raw); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	meta := spec.TokenPricing{
		Url: url, UrlBasis: basis, FetchedAt: spec.NewUTCTimestamp(fetchedAt),
		Sha256: hex.EncodeToString(digest[:]), Bytes: len(raw), Entries: len(sheet),
	}
	return r.writer.WriteDocument(ctx, bundle.TokenPricingJSON, spec.SchemaTokenPricing, meta)
}
