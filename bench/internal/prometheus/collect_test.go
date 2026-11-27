package prometheus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/prometheus/prometheustest"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func collectRequest(required []spec.CatalogName, optional []spec.OptionalCatalogResult) CollectRequest {
	return CollectRequest{
		Window:      testWindow,
		StageWindow: 2 * time.Minute,
		Intervals: Resolution{
			ScrapeInterval: ScrapeInterval{Interval: 10 * time.Second, Source: spec.ScrapeIntervalSourceDiscovered, Job: "servtuner-cadvisor"},
			PerRole: map[spec.JobRole]ScrapeInterval{
				spec.JobRoleTarget:   {Interval: 5 * time.Second, Source: spec.ScrapeIntervalSourceDiscovered, Job: "servtuner-targets"},
				spec.JobRoleCadvisor: {Interval: 10 * time.Second, Source: spec.ScrapeIntervalSourceDiscovered, Job: "servtuner-cadvisor"},
			},
		},
		Selectors: testSelectors,
		Required:  required,
		Optional:  optional,
	}
}

func findQuery(t *testing.T, collection Collection, catalog spec.CatalogName, name string) spec.Query {
	t.Helper()
	for _, query := range collection.Document.Queries {
		if query.Catalog == catalog && string(query.Name) == name {
			return query
		}
	}
	t.Fatalf("no query %s/%s in the document", catalog, name)
	return spec.Query{}
}

func TestCollectRunsEveryQueryWithTheStepOfItsJobRole(t *testing.T) {
	fake := answerEverything()
	client := newMTLSClient(t, fake)
	request := collectRequest([]spec.CatalogName{spec.CatalogNameVllm, spec.CatalogNameKubernetes}, nil)
	collection, err := Collect(context.Background(), client, request)
	if err != nil {
		t.Fatal(err)
	}
	vllm, _ := CatalogFor(spec.CatalogNameVllm)
	kubernetes, _ := CatalogFor(spec.CatalogNameKubernetes)
	if want := len(vllm.Queries) + len(kubernetes.Queries); len(collection.Document.Queries) != want || len(collection.Series) != want {
		t.Fatalf("queries %d, series %d, want %d", len(collection.Document.Queries), len(collection.Series), want)
	}
	if step := findQuery(t, collection, spec.CatalogNameVllm, "ttft_seconds").StepSeconds; step != 5 {
		t.Errorf("target step = %v, want the 5s interval", step)
	}
	if step := findQuery(t, collection, spec.CatalogNameKubernetes, "cpu_cores").StepSeconds; step != 10 {
		t.Errorf("cadvisor step = %v, want the 10s interval", step)
	}
	for _, request := range fake.Requests() {
		if request.Start != "1793188800" || request.End != "1793189400" {
			t.Errorf("request window = %s..%s, want the run window", request.Start, request.End)
		}
	}
	document := mustMarshal(t, collection.Document)
	if err := spec.Validate(spec.SchemaPrometheusQueries, document); err != nil {
		t.Errorf("queries.json is invalid: %v", err)
	}
}

func TestCollectStoresRawMatrices(t *testing.T) {
	fake := &prometheustest.Server{Answer: func(query string) []prometheustest.Series {
		if strings.Contains(query, "vllm:time_to_first_token_seconds") {
			return []prometheustest.Series{
				{Metric: map[string]string{"__name__": "vllm:time_to_first_token_seconds_bucket", "le": "0.5", "pod": "vllm-0"}, Samples: []prometheustest.Sample{{UnixSeconds: 1793188800, Value: "4"}, {UnixSeconds: 1793188805, Value: "9"}}},
				{Metric: map[string]string{"__name__": "vllm:time_to_first_token_seconds_count", "pod": "vllm-0"}, Samples: []prometheustest.Sample{{UnixSeconds: 1793188800, Value: "10"}}},
			}
		}
		return nil
	}}
	client := newPlatformClient(t, fake, nil)
	collection, err := Collect(context.Background(), client, collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	for _, series := range collection.Series {
		if series.Catalog == spec.CatalogNameVllm && series.Name == "ttft_seconds" {
			raw = series.Raw
		}
	}
	var stored struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("raw = %s: %v", raw, err)
	}
	if stored.ResultType != "matrix" || len(stored.Result) != 2 || stored.Result[0].Metric["le"] != "0.5" || len(stored.Result[0].Values) != 2 || stored.Result[0].Values[1][1] != "9" {
		t.Errorf("stored = %s", raw)
	}
	if query := findQuery(t, collection, spec.CatalogNameVllm, "ttft_seconds"); query.Result != spec.QueryResultCollected {
		t.Errorf("result = %s", query.Result)
	}
	if query := findQuery(t, collection, spec.CatalogNameVllm, "e2e_seconds"); query.Result != spec.QueryResultEmpty {
		t.Errorf("an unanswered query is empty, got %s", query.Result)
	}
}

func TestCollectRecordsWhichSeriesMayBeEmpty(t *testing.T) {
	fake := &prometheustest.Server{Answer: func(string) []prometheustest.Series { return nil }}
	collection, err := Collect(context.Background(), newPlatformClient(t, fake, nil), collectRequest([]spec.CatalogName{spec.CatalogNameKubernetes}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if query := findQuery(t, collection, spec.CatalogNameKubernetes, "cpu_limit_cores"); query.MayBeEmpty == nil || !*query.MayBeEmpty {
		t.Errorf("cpu_limit_cores: may_be_empty = %v, want true (no CPU limit is a state)", query.MayBeEmpty)
	}
	if query := findQuery(t, collection, spec.CatalogNameKubernetes, "cpu_cores"); query.MayBeEmpty != nil {
		t.Errorf("cpu_cores: may_be_empty = %v, want it left out", *query.MayBeEmpty)
	}
}

func TestCollectSkipsAbsentOptionalCatalogsAndRecordsThem(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	absent := "marker query returned no series"
	optional := []spec.OptionalCatalogResult{{Name: spec.CatalogNameDcgm, Result: spec.CatalogResultAbsent, Detail: &absent}}
	collection, err := Collect(context.Background(), client, collectRequest([]spec.CatalogName{spec.CatalogNameLitellm}, optional))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range collection.Document.Queries {
		if query.Catalog == spec.CatalogNameDcgm {
			t.Fatalf("an absent catalog was queried: %s", query.Name)
		}
	}
	for _, request := range fake.Requests() {
		if strings.Contains(request.Query, "DCGM_") {
			t.Errorf("a DCGM query was sent: %s", request.Query)
		}
	}
	if len(collection.Document.OptionalCatalogs) != 1 || collection.Document.OptionalCatalogs[0].Result != spec.CatalogResultAbsent {
		t.Errorf("optional catalogs = %+v", collection.Document.OptionalCatalogs)
	}

	collected := []spec.OptionalCatalogResult{{Name: spec.CatalogNameDcgm, Result: spec.CatalogResultCollected}}
	withDCGM, err := Collect(context.Background(), client, collectRequest([]spec.CatalogName{spec.CatalogNameLitellm}, collected))
	if err != nil {
		t.Fatal(err)
	}
	if query := findQuery(t, withDCGM, spec.CatalogNameDcgm, "fb_used_bytes_max"); query.StepSeconds != 10 {
		t.Errorf("dcgm step = %v: with no dcgm job the slowest relevant interval applies", query.StepSeconds)
	}
}

func TestCollectRecordsFailedQueriesAndKeepsGoing(t *testing.T) {
	fake := answerEverything()
	fake.ErrorFor = func(query string) (int, string) {
		if strings.Contains(query, "kv_cache_usage_perc") {
			return 422, "execution: out of memory"
		}
		return 0, ""
	}
	client := newPlatformClient(t, fake, nil)
	collection, err := Collect(context.Background(), client, collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, nil))
	if err != nil {
		t.Fatalf("a failed query must not fail the collection: %v", err)
	}
	failed := findQuery(t, collection, spec.CatalogNameVllm, "kv_cache_usage_max")
	if failed.Result != spec.QueryResultError || failed.Detail == nil || !strings.Contains(*failed.Detail, "out of memory") {
		t.Errorf("failed query = %+v", failed)
	}
	if ok := findQuery(t, collection, spec.CatalogNameVllm, "ttft_seconds"); ok.Result != spec.QueryResultCollected {
		t.Errorf("later queries must still run: %+v", ok)
	}
	for _, series := range collection.Series {
		if series.Name == "kv_cache_usage_max" {
			t.Error("a failed query has no raw result")
		}
	}
	if err := spec.Validate(spec.SchemaPrometheusQueries, mustMarshal(t, collection.Document)); err != nil {
		t.Errorf("queries.json is invalid: %v", err)
	}
}

func TestCollectTriesAlternatesWhenThePrimaryIsEmptyAndRecordsTheMatch(t *testing.T) {
	fake := &prometheustest.Server{Answer: func(query string) []prometheustest.Series {
		if strings.Contains(query, "gpu_cache_usage_perc") && strings.HasPrefix(query, "vllm:") {
			return prometheustest.OneSeries(map[string]string{"pod": "vllm-0"}, 1793188800, "0.5")
		}
		return nil
	}}
	client := newPlatformClient(t, fake, nil)
	collection, err := Collect(context.Background(), client, collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, nil))
	if err != nil {
		t.Fatal(err)
	}
	matched := findQuery(t, collection, spec.CatalogNameVllm, "kv_cache_usage")
	if matched.Result != spec.QueryResultCollected || !strings.HasPrefix(matched.Query, "vllm:gpu_cache_usage_perc") ||
		matched.Detail == nil || !strings.Contains(*matched.Detail, "alternate 1 matched") {
		t.Errorf("matched = %+v", matched)
	}
	unmatched := findQuery(t, collection, spec.CatalogNameVllm, "itl_seconds")
	if unmatched.Result != spec.QueryResultEmpty || !strings.Contains(unmatched.Query, "inter_token_latency_seconds") {
		t.Errorf("when nothing matches the primary query is recorded: %+v", unmatched)
	}
}

func TestCollectRunsExtraQueriesAsCatalogExtra(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	request := collectRequest(nil, nil)
	request.ExtraQueries = []spec.ExtraQuery{{Name: "my_series", Query: "sum(rate(my_total[1m]))"}}
	request.StepOverride = 15 * time.Second
	collection, err := Collect(context.Background(), client, request)
	if err != nil {
		t.Fatal(err)
	}
	extra := findQuery(t, collection, spec.CatalogNameExtra, "my_series")
	if extra.Query != "sum(rate(my_total[1m]))" || extra.StepSeconds != 15 || extra.Result != spec.QueryResultCollected {
		t.Errorf("extra = %+v", extra)
	}
}

func TestCollectRejectsBadRequestsBeforeQuerying(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	noNodes := collectRequest([]spec.CatalogName{spec.CatalogNameVllm, spec.CatalogNameDcgm}, nil)
	noNodes.Selectors.NodeRegex = ""
	if _, err := Collect(context.Background(), client, noNodes); err == nil {
		t.Error("a required dcgm without nodes must be rejected")
	}
	if got := len(fake.Requests()); got != 0 {
		t.Errorf("%d queries ran before the request was rejected", got)
	}
	noInterval := collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, nil)
	noInterval.Intervals = Resolution{}
	if _, err := Collect(context.Background(), client, noInterval); err == nil {
		t.Error("an unknown interval must be rejected")
	}
	badWindow := collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, nil)
	badWindow.Window = Window{Start: testWindow.End, End: testWindow.Start}
	if _, err := Collect(context.Background(), client, badWindow); err == nil {
		t.Error("an inverted window must be rejected")
	}
	if _, err := Collect(context.Background(), client, collectRequest([]spec.CatalogName{spec.CatalogNameExtra}, nil)); err == nil {
		t.Error("extra is not a catalog")
	}
}

func TestCollectStopsOnACancelledContextAndReturnsWhatItHas(t *testing.T) {
	client := newPlatformClient(t, answerEverything(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collection, err := Collect(ctx, client, collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, nil))
	if err == nil {
		t.Fatal("a cancelled context must be reported")
	}
	if collection.Document.Queries == nil || collection.Document.OptionalCatalogs == nil {
		t.Error("the partial document must still encode as arrays")
	}
}

func TestCatalogsToCollectDeduplicates(t *testing.T) {
	collected := []spec.OptionalCatalogResult{{Name: spec.CatalogNameDcgm, Result: spec.CatalogResultCollected}, {Name: spec.CatalogNameVllm, Result: spec.CatalogResultCollected}}
	required, optional, err := catalogsToCollect(collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, collected))
	if err != nil || len(required) != 1 || required[0] != spec.CatalogNameVllm || len(optional) != 1 || optional[0] != spec.CatalogNameDcgm {
		t.Errorf("required = %v, optional = %v, err = %v", required, optional, err)
	}
}

func TestAnOptionalCatalogThatCannotBeRenderedDoesNotAbortCollect(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	detected := []spec.OptionalCatalogResult{{Name: spec.CatalogNameDcgm, Result: spec.CatalogResultCollected}}
	request := collectRequest([]spec.CatalogName{spec.CatalogNameVllm}, detected)
	request.Selectors.NodeRegex = ""
	collection, err := Collect(context.Background(), client, request)
	if err != nil {
		t.Fatalf("collect aborted: %v", err)
	}
	vllm, _ := CatalogFor(spec.CatalogNameVllm)
	if len(collection.Document.Queries) != len(vllm.Queries) {
		t.Errorf("the required catalog must still be collected: %d queries", len(collection.Document.Queries))
	}
	result := collection.Document.OptionalCatalogs[0]
	if result.Name != spec.CatalogNameDcgm || result.Result != spec.CatalogResultError || result.Detail == nil || !strings.Contains(*result.Detail, "NodeRegex") {
		t.Errorf("optional result = %+v", result)
	}
	if detected[0].Result != spec.CatalogResultCollected {
		t.Error("the caller's slice must not be modified")
	}
	if err := spec.Validate(spec.SchemaPrometheusQueries, mustMarshal(t, collection.Document)); err != nil {
		t.Errorf("queries.json is invalid: %v", err)
	}
}

func TestCollectUsesTheKubeStateIntervalWhenDiscovered(t *testing.T) {
	fake := answerEverything()
	client := newPlatformClient(t, fake, nil)
	request := collectRequest([]spec.CatalogName{spec.CatalogNameKubernetes}, nil)
	request.Intervals.PerRole[spec.JobRoleKubeStateMetrics] = ScrapeInterval{Interval: 30 * time.Second, Source: spec.ScrapeIntervalSourceDiscovered, Job: "kube-state-metrics"}
	collection, err := Collect(context.Background(), client, request)
	if err != nil {
		t.Fatal(err)
	}
	if query := findQuery(t, collection, spec.CatalogNameKubernetes, "deploy_available_min"); !strings.Contains(query.Query, "[2m]))") {
		t.Errorf("deploy_available_min = %s", query.Query)
	}
}
