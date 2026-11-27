package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// PriceListEndpointRegion is the Price List Query API endpoint used. The API
// is served from us-east-1, eu-central-1 and ap-south-1 only, and any of them
// prices any region: the nodes' region is the regionCode filter (AWS General
// Reference, Billing endpoints; verified 2026-11-22).
const PriceListEndpointRegion = "us-east-1"

// ProductsAPI is the one Price List call bench makes (pricing:GetProducts),
// so tests can answer it.
type ProductsAPI interface {
	GetProducts(ctx context.Context, input *awspricing.GetProductsInput, options ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error)
}

// FromAWS resolves the on-demand Linux price of each instance type in the
// region. A type the API cannot price is listed in problems, not fatal: the
// analysis then reports it unpriced.
func FromAWS(ctx context.Context, api ProductsAPI, policy spec.InfraPricingPolicy, instanceTypes []string, now time.Time) (spec.InfraPricing, error) {
	if policy.Aws == nil {
		return spec.InfraPricing{}, fmt.Errorf("infra_pricing.source is aws but infra_pricing.aws is missing")
	}
	prices := spec.InfraPricing{
		Source: spec.InfraPricingSourceAws, ResolvedAt: spec.NewUTCTimestamp(now), Discount: discountOf(policy),
		Nodes: spec.InfraPricingNodes{}, Gpus: spec.InfraPricingGpus{}, Query: []string{},
	}
	for _, instanceType := range instanceTypes {
		if instanceType == "" || instanceType == "local" {
			prices.Problems = append(prices.Problems, fmt.Sprintf("%q is not an EC2 instance type", instanceType))
			continue
		}
		filters := ec2Filters(instanceType, policy.Aws.Region)
		prices.Query = append(prices.Query, describeFilters(filters))
		output, err := api.GetProducts(ctx, &awspricing.GetProductsInput{ServiceCode: aws.String("AmazonEC2"), Filters: filters, MaxResults: aws.Int32(maxProducts)})
		if err != nil {
			prices.Problems = append(prices.Problems, fmt.Sprintf("%s: GetProducts: %v", instanceType, err))
			continue
		}
		node, err := nodeFromProducts(output.PriceList)
		if err != nil {
			prices.Problems = append(prices.Problems, fmt.Sprintf("%s: %v", instanceType, err))
			continue
		}
		prices.Nodes[instanceType] = node
	}
	return prices, nil
}

// maxProducts bounds one answer: with every filter set an instance type has
// one product, so more than a few means the filters are not specific enough.
const maxProducts = 10

func ec2Filters(instanceType, region string) []types.Filter {
	pairs := [][2]string{
		{"instanceType", instanceType}, {"regionCode", region}, {"operatingSystem", "Linux"},
		{"tenancy", "Shared"}, {"capacitystatus", "Used"}, {"preInstalledSw", "NA"}, {"licenseModel", "No License required"},
	}
	filters := make([]types.Filter, 0, len(pairs))
	for _, pair := range pairs {
		filters = append(filters, types.Filter{Type: types.FilterTypeTermMatch, Field: aws.String(pair[0]), Value: aws.String(pair[1])})
	}
	return filters
}

func describeFilters(filters []types.Filter) string {
	parts := make([]string, 0, len(filters))
	for _, filter := range filters {
		parts = append(parts, aws.ToString(filter.Field)+"="+aws.ToString(filter.Value))
	}
	return "AmazonEC2 " + strings.Join(parts, " ")
}

// product is the part of a Price List product document bench reads.
type product struct {
	Product struct {
		Attributes struct {
			VCPU   string `json:"vcpu"`
			Memory string `json:"memory"`
			GPU    string `json:"gpu"`
		} `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]struct {
				Unit         string            `json:"unit"`
				PricePerUnit map[string]string `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

// nodeFromProducts reads the one matching product's hourly USD price and its
// vCPU, memory and GPU count. Several products that disagree on the price are
// an error rather than a guess.
func nodeFromProducts(documents []string) (spec.NodePrice, error) {
	if len(documents) == 0 {
		return spec.NodePrice{}, fmt.Errorf("no product matches")
	}
	var node spec.NodePrice
	for i, document := range documents {
		var item product
		if err := json.Unmarshal([]byte(document), &item); err != nil {
			return spec.NodePrice{}, fmt.Errorf("parse product: %w", err)
		}
		current, err := nodeOf(item)
		if err != nil {
			return spec.NodePrice{}, err
		}
		if i > 0 && current != node {
			return spec.NodePrice{}, fmt.Errorf("%d products match with different prices or sizes", len(documents))
		}
		node = current
	}
	return node, nil
}

func nodeOf(item product) (spec.NodePrice, error) {
	attributes := item.Product.Attributes
	cores, err := strconv.ParseFloat(attributes.VCPU, 64)
	if err != nil {
		return spec.NodePrice{}, fmt.Errorf("vcpu %q: %w", attributes.VCPU, err)
	}
	memory, err := parseGiB(attributes.Memory)
	if err != nil {
		return spec.NodePrice{}, err
	}
	gpus := 0
	if attributes.GPU != "" {
		if gpus, err = strconv.Atoi(attributes.GPU); err != nil {
			return spec.NodePrice{}, fmt.Errorf("gpu %q: %w", attributes.GPU, err)
		}
	}
	perHour, err := onDemandHourly(item)
	if err != nil {
		return spec.NodePrice{}, err
	}
	return spec.NodePrice{UsdPerHour: perHour, Cores: cores, MemoryGib: memory, Gpus: gpus}, nil
}

// parseGiB reads the memory attribute, such as "16 GiB" or "1,152 GiB".
func parseGiB(text string) (float64, error) {
	number, unit, found := strings.Cut(strings.TrimSpace(text), " ")
	if !found || unit != "GiB" {
		return 0, fmt.Errorf("memory %q is not in GiB", text)
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(number, ",", ""), 64)
	if err != nil {
		return 0, fmt.Errorf("memory %q: %w", text, err)
	}
	return value, nil
}

func onDemandHourly(item product) (float64, error) {
	for _, term := range item.Terms.OnDemand {
		for _, dimension := range term.PriceDimensions {
			if dimension.Unit != "Hrs" {
				continue
			}
			text, ok := dimension.PricePerUnit["USD"]
			if !ok {
				continue
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return 0, fmt.Errorf("price %q: %w", text, err)
			}
			return value, nil
		}
	}
	return 0, fmt.Errorf("no on-demand hourly USD price")
}

// NewPriceListClient builds the Price List client with the SDK's default
// credential chain (EKS Pod Identity, IRSA, environment), as the S3 store
// does. bench's role needs pricing:GetProducts (deploy/aws/).
func NewPriceListClient(ctx context.Context) (ProductsAPI, error) {
	loaded, err := config.LoadDefaultConfig(ctx, config.WithRegion(PriceListEndpointRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return awspricing.NewFromConfig(loaded), nil
}
