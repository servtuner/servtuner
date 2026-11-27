package pricing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// productJSON is a Price List product document in the shape GetProducts
// returns (one string per product), trimmed to the fields bench reads plus a
// few it ignores.
func productJSON(instanceType, vcpu, memory, gpu, usd string) string {
	gpuAttribute := ""
	if gpu != "" {
		gpuAttribute = fmt.Sprintf(`"gpu": %q, "gpuMemory": "24 GiB",`, gpu)
	}
	return fmt.Sprintf(`{"product": {"productFamily": "Compute Instance", "attributes": {"instanceType": %q, "vcpu": %q, "memory": %q, %s "operatingSystem": "Linux", "regionCode": "us-east-1"}, "sku": "SKU1"},
	 "serviceCode": "AmazonEC2",
	 "terms": {"OnDemand": {"SKU1.JRTCKXETXF": {"priceDimensions": {"SKU1.JRTCKXETXF.6YS6EN2CT7": {"unit": "Hrs", "pricePerUnit": {"USD": %q}, "description": "per On Demand Linux instance hour"}}}}}}`,
		instanceType, vcpu, memory, gpuAttribute, usd)
}

type fakeProducts struct {
	answers map[string][]string
	inputs  []*awspricing.GetProductsInput
}

func (f *fakeProducts) GetProducts(_ context.Context, input *awspricing.GetProductsInput, _ ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error) {
	f.inputs = append(f.inputs, input)
	instanceType := ""
	for _, filter := range input.Filters {
		if aws.ToString(filter.Field) == "instanceType" {
			instanceType = aws.ToString(filter.Value)
		}
	}
	if instanceType == "throttled.large" {
		return nil, errors.New("ThrottlingException")
	}
	return &awspricing.GetProductsOutput{PriceList: f.answers[instanceType]}, nil
}

func TestAWSPricesEachInstanceTypeOnDemandLinux(t *testing.T) {
	api := &fakeProducts{answers: map[string][]string{
		"m7i.xlarge":   {productJSON("m7i.xlarge", "4", "16 GiB", "", "0.2016")},
		"g5.12xlarge":  {productJSON("g5.12xlarge", "48", "192 GiB", "4", "5.672")},
		"p4d.24xlarge": {productJSON("p4d.24xlarge", "96", "1,152 GiB", "8", "32.7726")},
		"ambiguous.xl": {productJSON("ambiguous.xl", "4", "16 GiB", "", "0.2"), productJSON("ambiguous.xl", "4", "16 GiB", "", "0.3")},
	}}
	policy := spec.InfraPricingPolicy{Source: spec.InfraPricingPolicySourceAws, Discount: 0.7, Aws: &spec.AwsPricing{Region: "eu-west-1"}}
	types := []string{"m7i.xlarge", "g5.12xlarge", "p4d.24xlarge", "ambiguous.xl", "throttled.large", "missing.large", "local"}
	prices, err := FromAWS(context.Background(), api, policy, types, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := prices.Nodes["m7i.xlarge"]; got != (spec.NodePrice{UsdPerHour: 0.2016, Cores: 4, MemoryGib: 16}) {
		t.Errorf("m7i.xlarge %+v", got)
	}
	if got := prices.Nodes["p4d.24xlarge"]; got.MemoryGib != 1152 || got.Gpus != 8 {
		t.Errorf("p4d.24xlarge %+v", got)
	}
	if len(prices.Nodes) != 3 || len(prices.Problems) != 4 {
		t.Errorf("nodes %v, problems %v", prices.Nodes, prices.Problems)
	}
	for _, want := range []string{"different prices", "ThrottlingException", "no product matches", `"local" is not an EC2`} {
		if !strings.Contains(strings.Join(prices.Problems, "\n"), want) {
			t.Errorf("no problem says %q: %v", want, prices.Problems)
		}
	}
	if prices.Discount != 0.7 || prices.Source != spec.InfraPricingSourceAws {
		t.Errorf("prices %+v", prices)
	}
	query := strings.Join(prices.Query, "\n")
	for _, want := range []string{"regionCode=eu-west-1", "operatingSystem=Linux", "tenancy=Shared", "capacitystatus=Used"} {
		if !strings.Contains(query, want) {
			t.Errorf("query lacks %s: %s", want, query)
		}
	}
	// A GPU node priced by AWS pays per GPU through the node price.
	cost := ReplicaCost(prices, Hardware{InstanceType: "g5.12xlarge", Accelerator: "A10G"}, Replica{GPUs: 1})
	if !cost.IsPriced || !near(*cost.UsdPerReplicaHour, 5.672/4*0.7) {
		t.Errorf("one of four A10G: %+v", cost)
	}
}
