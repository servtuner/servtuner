package prometheus

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/prometheus/prometheustest"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var probeTime = testWindow.Start

func probeInput() RenderInput { return RenderInput{Selectors: testSelectors} }

func TestProbeReportsCollectedWhenTheMarkerExists(t *testing.T) {
	fake := &prometheustest.Server{Answer: func(query string) []prometheustest.Series {
		if strings.Contains(query, "DCGM_FI_DEV_FB_USED") {
			return prometheustest.OneSeries(nil, 1793188800, "2")
		}
		return nil
	}}
	for name, client := range map[string]*Client{"platform": newPlatformClient(t, fake, nil), "mtls": newMTLSClient(t, fake)} {
		results, err := ProbeOptionalCatalogs(context.Background(), client, []spec.CatalogName{spec.CatalogNameDcgm}, probeInput(), probeTime)
		if err != nil || len(results) != 1 {
			t.Fatalf("%s: results = %v, err = %v", name, results, err)
		}
		if results[0].Name != spec.CatalogNameDcgm || results[0].Result != spec.CatalogResultCollected || results[0].Detail != nil {
			t.Errorf("%s: result = %+v", name, results[0])
		}
	}
	marker := fake.Requests()[0].Query
	if !strings.Contains(marker, `DCGM_FI_DEV_FB_USED_BYTES{hostname=~"gpu-node-1"}`) {
		t.Errorf("the marker is not scoped to the target nodes: %s", marker)
	}
}

func TestProbeReportsAbsentWithTheMarkerInTheDetail(t *testing.T) {
	client := newPlatformClient(t, &prometheustest.Server{}, nil)
	results, err := ProbeOptionalCatalogs(context.Background(), client, []spec.CatalogName{spec.CatalogNameDcgm}, probeInput(), probeTime)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Result != spec.CatalogResultAbsent || results[0].Detail == nil || !strings.Contains(*results[0].Detail, "DCGM_FI_DEV_FB_USED") {
		t.Errorf("result = %+v", results[0])
	}
}

func TestProbeReportsErrorWhenPrometheusFails(t *testing.T) {
	fake := &prometheustest.Server{}
	fake.FailNext(http.StatusInternalServerError, http.StatusInternalServerError, http.StatusInternalServerError, http.StatusInternalServerError)
	client := newPlatformClient(t, fake, nil)
	results, err := ProbeOptionalCatalogs(context.Background(), client, []spec.CatalogName{spec.CatalogNameDcgm}, probeInput(), probeTime)
	if err != nil {
		t.Fatalf("a failed probe is a result, not a Go error: %v", err)
	}
	if results[0].Result != spec.CatalogResultError || results[0].Detail == nil || *results[0].Detail == "" {
		t.Errorf("result = %+v", results[0])
	}
}

func TestProbeWithoutKnownNodesAsksAboutAnyNode(t *testing.T) {
	fake := &prometheustest.Server{}
	client := newPlatformClient(t, fake, nil)
	unscoped := RenderInput{Selectors: Selectors{NamespaceRegex: "llm", PodRegex: "vllm-.*"}}
	if _, err := ProbeOptionalCatalogs(context.Background(), client, []spec.CatalogName{spec.CatalogNameDcgm}, unscoped, probeTime); err != nil {
		t.Fatal(err)
	}
	if marker := fake.Requests()[0].Query; !strings.Contains(marker, `hostname=~".+"`) {
		t.Errorf("marker = %s", marker)
	}
}

func TestAnyCatalogCanBeProbedAndAMarkerNeedsNoInterval(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	names := []spec.CatalogName{spec.CatalogNameVllm, spec.CatalogNameBifrost, spec.CatalogNameLitellm, spec.CatalogNameKubernetes}
	results, err := ProbeOptionalCatalogs(context.Background(), client, names, probeInput(), probeTime)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Result != spec.CatalogResultCollected {
			t.Errorf("%s = %+v", result.Name, result)
		}
	}
	if _, err := ProbeOptionalCatalogs(context.Background(), client, []spec.CatalogName{spec.CatalogNameExtra}, probeInput(), probeTime); err == nil {
		t.Error("extra is not a catalog")
	}
}

func TestProbeResultsValidateAgainstTheSchema(t *testing.T) {
	client := newPlatformClient(t, &prometheustest.Server{}, nil)
	results, err := ProbeOptionalCatalogs(context.Background(), client, []spec.CatalogName{spec.CatalogNameDcgm}, probeInput(), probeTime)
	if err != nil {
		t.Fatal(err)
	}
	document := spec.PrometheusQueries{Queries: []spec.Query{}, OptionalCatalogs: results}
	if err := spec.Validate(spec.SchemaPrometheusQueries, mustMarshal(t, document)); err != nil {
		t.Errorf("queries.json is invalid: %v", err)
	}
}
