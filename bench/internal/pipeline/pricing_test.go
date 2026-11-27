package pipeline

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"testing"

	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/pricing"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
)

func readPrices(t *testing.T, store storage.BundleStore) (spec.InfraPricing, bool) {
	t.Helper()
	reader, err := store.Get(context.Background(), bundle.InfraPricingJSON)
	if errors.Is(err, storage.ErrNotFound) {
		return spec.InfraPricing{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, _ := io.ReadAll(reader)
	prices, err := spec.Parse[spec.InfraPricing](spec.SchemaInfraPricing, content)
	if err != nil {
		t.Fatal(err)
	}
	return prices, true
}

func TestAStaticPolicyIsSnapshottedAtRunStart(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "infra-pricing-static.json"))
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	prices, ok := readPrices(t, store)
	if !ok {
		t.Fatal("no pricing/infra.json")
	}
	gpu := prices.Gpus["NVIDIA GeForce RTX 3070"]
	if want := 500.0/8760 + 220.0/1000*0.087; math.Abs(gpu.UsdPerHour-want) > 1e-12 || prices.ResolvedAt != spec.NewUTCTimestamp(fixedTime) {
		t.Errorf("prices %+v", prices)
	}
}

type countingProducts struct{ calls int }

func (c *countingProducts) GetProducts(context.Context, *awspricing.GetProductsInput, ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error) {
	c.calls++
	return &awspricing.GetProductsOutput{}, nil
}

func TestAWSPricesNeedAClientAndNeverFailTheRun(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "infra-pricing-aws.json"))
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("no client is a logged problem, not a failure: %+v", result)
	}
	if _, ok := readPrices(t, store); ok {
		t.Error("prices written without a client")
	}
	options, store, _ = newOptions(t, readExample(t, "valid", "infra-pricing-aws.json"))
	api := &countingProducts{}
	options.PriceList = func(context.Context) (pricing.ProductsAPI, error) { return api, nil }
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	prices, ok := readPrices(t, store)
	// The example is off-cluster: no snapshot, so no instance type to ask about.
	if !ok || prices.Source != spec.InfraPricingSourceAws || len(prices.Nodes) != 0 || api.calls != 0 {
		t.Errorf("prices %+v, calls %d", prices, api.calls)
	}
}

func TestTheDatasheetIsStoredByteForByteWithWhereItCameFrom(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "token-pricing-datasheet.json"))
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if got := string(mustRead(t, store, bundle.DatasheetJSON)); got != testDatasheet {
		t.Errorf("datasheet %q", got)
	}
	meta, err := spec.Parse[spec.TokenPricing](spec.SchemaTokenPricing, mustRead(t, store, bundle.TokenPricingJSON))
	if err != nil {
		t.Fatal(err)
	}
	// The example is off-cluster, so there is no Bifrost config to read a pricing_url from.
	if meta.Url != pricing.DefaultDatasheetURL || meta.UrlBasis != spec.TokenPricingUrlBasisDefault || meta.Entries != 1 || meta.Bytes != len(testDatasheet) {
		t.Errorf("meta %+v", meta)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no route to host")
}

func TestAFailedDatasheetFetchNeverFailsTheRun(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "token-pricing-datasheet.json"))
	options.DatasheetClient = &http.Client{Transport: failingTransport{}}
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if _, err := store.Get(context.Background(), bundle.DatasheetJSON); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a datasheet was stored after a failed fetch: %v", err)
	}
}

// recordingDatasheet answers like cannedDatasheet and records each URL asked for.
type recordingDatasheet struct{ urls *[]string }

func (r recordingDatasheet) RoundTrip(request *http.Request) (*http.Response, error) {
	*r.urls = append(*r.urls, request.URL.String())
	return cannedDatasheet{}.RoundTrip(request)
}

func TestTheDatasheetComesFromThePriceBookWhenTheRunSaysSo(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "token-pricing-price-book.json"))
	var public, api []string
	options.DatasheetClient = &http.Client{Transport: recordingDatasheet{&public}}
	options.PriceBook = &PriceBookSource{URL: "http://api.test/price-book/datasheet", Client: &http.Client{Transport: recordingDatasheet{&api}}}
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	meta, err := spec.Parse[spec.TokenPricing](spec.SchemaTokenPricing, mustRead(t, store, bundle.TokenPricingJSON))
	if err != nil {
		t.Fatal(err)
	}
	if meta.UrlBasis != spec.TokenPricingUrlBasisPriceBook || meta.Url != "http://api.test/price-book/datasheet" || len(public) != 0 || len(api) != 1 {
		t.Errorf("meta %+v, public %v, api %v", meta, public, api)
	}
}

func TestThePriceBookNeedsTheAPI(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "token-pricing-price-book.json"))
	var public []string
	options.DatasheetClient = &http.Client{Transport: recordingDatasheet{&public}}
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if _, err := store.Get(context.Background(), bundle.DatasheetJSON); !errors.Is(err, storage.ErrNotFound) || len(public) != 0 {
		t.Errorf("without --api-url nothing is fetched: %v, %v", err, public)
	}
}

func TestAnOfflineRunNeverFetchesThePublicDatasheetOrAWS(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "token-pricing-datasheet.json"))
	var public []string
	options.DatasheetClient = &http.Client{Transport: recordingDatasheet{&public}}
	options.Offline = true
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if _, err := store.Get(context.Background(), bundle.DatasheetJSON); !errors.Is(err, storage.ErrNotFound) || len(public) != 0 {
		t.Errorf("offline fetched %v (%v)", public, err)
	}
	environment, err := spec.Parse[spec.Environment](spec.SchemaEnvironment, mustRead(t, store, bundle.EnvironmentJSON))
	if err != nil || environment.Offline == nil || !*environment.Offline {
		t.Errorf("environment.offline = %v (%v)", environment.Offline, err)
	}

	options, store, _ = newOptions(t, readExample(t, "valid", "infra-pricing-aws.json"))
	api := &countingProducts{}
	options.PriceList = func(context.Context) (pricing.ProductsAPI, error) { return api, nil }
	options.Offline = true
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if _, ok := readPrices(t, store); ok {
		t.Error("an offline run was priced from AWS")
	}
}
