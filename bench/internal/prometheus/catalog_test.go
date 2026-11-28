package prometheus

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

var catalogNames = []spec.CatalogName{
	spec.CatalogNameKubernetes, spec.CatalogNameBifrost, spec.CatalogNameVllm, spec.CatalogNameLitellm, spec.CatalogNameSglang, spec.CatalogNameDcgm,
}

// targetCatalogs are the catalogs whose series come from the servtuner-targets job,
// where every selector must say so.
var targetCatalogs = []spec.CatalogName{spec.CatalogNameBifrost, spec.CatalogNameVllm, spec.CatalogNameLitellm, spec.CatalogNameSglang}

func renderInputFor(catalog Catalog, interval time.Duration) RenderInput {
	return RenderInput{Selectors: testSelectors, StageWindow: 2 * time.Minute, Interval: interval}
}

func TestEveryCatalogLoadsAndNamesComeFromTheSchemaEnum(t *testing.T) {
	catalogs, err := Catalogs()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogs) != len(catalogNames) {
		t.Errorf("loaded %d catalogs, want %d", len(catalogs), len(catalogNames))
	}
	for _, name := range catalogNames {
		catalog, found := catalogs[name]
		if !found {
			t.Errorf("catalog %s is missing", name)
			continue
		}
		if len(catalog.Queries) < 10 {
			t.Errorf("catalog %s has only %d queries", name, len(catalog.Queries))
		}
	}
}

func TestCatalogRolesAndOptionality(t *testing.T) {
	wantRole := map[spec.CatalogName]spec.JobRole{
		spec.CatalogNameKubernetes: spec.JobRoleCadvisor,
		spec.CatalogNameBifrost:    spec.JobRoleTarget,
		spec.CatalogNameVllm:       spec.JobRoleTarget,
		spec.CatalogNameLitellm:    spec.JobRoleTarget,
		spec.CatalogNameSglang:     spec.JobRoleTarget,
		spec.CatalogNameDcgm:       spec.JobRoleDcgm,
	}
	for name, role := range wantRole {
		catalog, err := CatalogFor(name)
		if err != nil {
			t.Fatal(err)
		}
		if catalog.JobRole != role {
			t.Errorf("%s job role = %s, want %s", name, catalog.JobRole, role)
		}
		if strings.TrimSpace(catalog.Marker.Query) == "" {
			t.Errorf("%s has no marker, so it cannot be declared optional", name)
		}
	}
}

func TestEveryRenderedQueryIsWellFormed(t *testing.T) {
	for _, name := range catalogNames {
		catalog, err := CatalogFor(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, interval := range []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, time.Minute} {
			rendered, err := catalog.Render(renderInputFor(catalog, interval))
			if err != nil {
				t.Fatalf("%s at %s: %v", name, interval, err)
			}
			for _, query := range rendered {
				for _, text := range append([]string{query.Query}, query.Alternates...) {
					if problem := checkPromQLShape(text); problem != "" {
						t.Errorf("%s/%s at %s: %s: %s", name, query.Entry.Name, interval, problem, text)
					}
				}
			}
		}
		marker, err := catalog.RenderMarker(testSelectors.asInput())
		if err != nil {
			t.Fatal(err)
		}
		if problem := checkPromQLShape(marker); problem != "" {
			t.Errorf("%s marker: %s: %s", name, problem, marker)
		}
	}
}

func (s Selectors) asInput() RenderInput { return RenderInput{Selectors: s} }

func TestTargetCatalogsSelectTheServtunerTargetsJobEverywhere(t *testing.T) {
	for _, name := range targetCatalogs {
		catalog, err := CatalogFor(name)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := catalog.Render(renderInputFor(catalog, 5*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range rendered {
			for _, text := range append([]string{query.Query}, query.Alternates...) {
				blocks := selectorBlocks(text)
				if len(blocks) == 0 {
					t.Errorf("%s/%s has no selector: %s", name, query.Entry.Name, text)
				}
				for _, block := range blocks {
					if !strings.Contains(block[1], `job="servtuner-targets"`) {
						t.Errorf("%s/%s: selector {%s} lacks job=\"servtuner-targets\"", name, query.Entry.Name, block[1])
					}
				}
			}
		}
	}
}

func TestKubernetesCatalogDoesNotDoubleCountContainers(t *testing.T) {
	catalog, err := CatalogFor(spec.CatalogNameKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := catalog.Render(renderInputFor(catalog, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	sawContainerSeries := false
	for _, query := range rendered {
		if strings.Contains(query.Query, "/metrics/resource") {
			t.Errorf("%s mixes in /metrics/resource", query.Entry.Name)
		}
		for _, block := range selectorBlocks(query.Query) {
			if !strings.HasPrefix(block[0], "container_") {
				continue
			}
			sawContainerSeries = true
			if strings.Contains(block[0], "network") {
				continue // pod-level series carry no container label
			}
			for _, required := range []string{`metrics_path="/metrics/cadvisor"`, `container!=""`, `container!="POD"`} {
				if !strings.Contains(block[1], required) {
					t.Errorf("%s: %s{%s} lacks %s", query.Entry.Name, block[0], block[1], required)
				}
			}
		}
	}
	if !sawContainerSeries {
		t.Error("no cAdvisor series found")
	}
}

func TestCadvisorSeriesNameTheDiscoveredJob(t *testing.T) {
	catalog, err := CatalogFor(spec.CatalogNameKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	input := renderInputFor(catalog, 15*time.Second)
	input.CadvisorJob = "servtuner-cadvisor"
	rendered, err := catalog.Render(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range rendered {
		for _, block := range selectorBlocks(query.Query) {
			if strings.Contains(block[1], "/metrics/cadvisor") && !strings.Contains(block[1], `job="servtuner-cadvisor"`) {
				t.Errorf("%s: %s{%s} reads every cAdvisor job, so a container scraped by two jobs counts twice", query.Entry.Name, block[0], block[1])
			}
		}
	}
}

func TestSeriesNamesAreUniqueAndUnusedMetricsAreAbsent(t *testing.T) {
	for _, name := range catalogNames {
		catalog, _ := CatalogFor(name)
		seen := map[spec.MetricName]bool{}
		for _, entry := range catalog.Queries {
			if seen[entry.Name] {
				t.Errorf("%s: %s appears twice", name, entry.Name)
			}
			seen[entry.Name] = true
			for _, forbidden := range []string{"container_memory_max_usage_bytes", "container_memory_failcnt", "container_spec_", "kube_node_labels"} {
				if strings.Contains(entry.Query, forbidden) {
					t.Errorf("%s/%s uses %s, which the research rules out", name, entry.Name, forbidden)
				}
			}
		}
	}
}

func TestWindowRulesFollowTheInterval(t *testing.T) {
	data, err := RenderInput{Selectors: testSelectors, StageWindow: 90 * time.Second, Interval: 5 * time.Second}.templateData()
	if err != nil {
		t.Fatal(err)
	}
	if data["Window"] != "1m30s" || data["CounterRateWindow"] != "1m" || data["SubqueryStep"] != "15s" || data["Step"] != "5s" {
		t.Errorf("5s interval: %v", data)
	}
	// A coarse interval widens every window: never less than 4 x I.
	data, err = RenderInput{Selectors: testSelectors, StageWindow: 90 * time.Second, Interval: time.Minute}.templateData()
	if err != nil {
		t.Fatal(err)
	}
	if data["Window"] != "4m" || data["CounterRateWindow"] != "4m" || data["SubqueryStep"] != "1m" {
		t.Errorf("1m interval: %v", data)
	}
}

func TestGaugePeaksUseOverTimeAndCounterPeaksUseSubqueries(t *testing.T) {
	kubernetes, _ := CatalogFor(spec.CatalogNameKubernetes)
	vllm, _ := CatalogFor(spec.CatalogNameVllm)
	input := RenderInput{Selectors: testSelectors, StageWindow: 2 * time.Minute, Interval: 5 * time.Second}
	assertQuery := func(catalog Catalog, name, want string) {
		t.Helper()
		rendered, err := catalog.Render(input)
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range rendered {
			if string(query.Entry.Name) == name {
				if !strings.Contains(query.Query, want) {
					t.Errorf("%s/%s = %s, want it to contain %s", catalog.Name, name, query.Query, want)
				}
				return
			}
		}
		t.Errorf("%s/%s not found", catalog.Name, name)
	}
	assertQuery(vllm, "kv_cache_usage_max", `max_over_time(vllm:kv_cache_usage_perc{job="servtuner-targets",`)
	assertQuery(vllm, "kv_cache_usage_max", `[2m])`)
	assertQuery(vllm, "kv_cache_usage_p95", `quantile_over_time(0.95, vllm:kv_cache_usage_perc{`)
	assertQuery(kubernetes, "cpu_cores_max", `rate(container_cpu_usage_seconds_total{`)
	assertQuery(kubernetes, "cpu_cores_max", `[1m])))[2m:15s])`)
	assertQuery(kubernetes, "memory_working_set_bytes_max", `max_over_time(container_memory_working_set_bytes{`)
}

func TestHistogramsAreStoredAsRawBucketSeries(t *testing.T) {
	for _, name := range targetCatalogs {
		catalog, _ := CatalogFor(name)
		histograms := 0
		for _, entry := range catalog.Queries {
			if !strings.Contains(entry.Query, "_bucket|sum|count") && !strings.Contains(entry.Query, "_(bucket|sum|count)") {
				continue
			}
			histograms++
			for _, computed := range []string{"histogram_quantile", "rate(", "increase("} {
				if strings.Contains(entry.Query, computed) {
					t.Errorf("%s/%s computes %s instead of storing raw buckets", name, entry.Name, computed)
				}
			}
		}
		if histograms < 5 {
			t.Errorf("%s stores only %d histogram families", name, histograms)
		}
	}
}

func TestDCGMQueriesAreScopedToTheNodeOrThePod(t *testing.T) {
	catalog, _ := CatalogFor(spec.CatalogNameDcgm)
	rendered, err := catalog.Render(renderInputFor(catalog, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range rendered {
		hostScoped := strings.Contains(query.Entry.Query, `hostname=~"<<.NodeRegex>>"`)
		podScoped := strings.Contains(query.Entry.Query, "<<.PodLabel>>")
		switch {
		case hostScoped && !strings.Contains(query.Query, `hostname=~"gpu-node-1"`):
			t.Errorf("%s is not scoped to the target node: %s", query.Entry.Name, query.Query)
		case podScoped && !(strings.Contains(query.Query, `by (namespace, pod)`) && strings.Contains(query.Query, `by (exported_namespace, exported_pod)`) &&
			strings.Contains(query.Query, `namespace=~"llm|servtuner"`) && strings.Contains(query.Query, `exported_namespace=~"llm|servtuner"`)):
			t.Errorf("%s does not cover pod and exported_pod: %s", query.Entry.Name, query.Query)
		case !hostScoped && !podScoped:
			t.Errorf("%s is scoped by neither node nor pod", query.Entry.Name)
		}
		// dcgm-exporter 4.9.0 names: units in the names, none of the 4.8 ones.
		for _, old := range []string{"DCGM_FI_DEV_FB_USED{", "DCGM_FI_DEV_GPU_UTIL{", "DCGM_FI_DEV_SM_CLOCK{", "DCGM_FI_DEV_POWER_USAGE{", "Hostname"} {
			if strings.Contains(query.Query, old) {
				t.Errorf("%s uses the pre-4.9 %s: %s", query.Entry.Name, old, query.Query)
			}
		}
	}
}

func TestAlternatesCarryTheRenamedMetrics(t *testing.T) {
	catalog, _ := CatalogFor(spec.CatalogNameVllm)
	rendered, err := catalog.Render(renderInputFor(catalog, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]RenderedQuery{}
	for _, query := range rendered {
		byName[string(query.Entry.Name)] = query
	}
	want := map[string]string{
		"kv_cache_usage":             "vllm:gpu_cache_usage_perc",
		"prefix_cache_hits_total":    "vllm:gpu_prefix_cache_hits_total",
		"prefix_cache_queries_total": "vllm:gpu_prefix_cache_queries_total",
		"itl_seconds":                "vllm:time_per_output_token_seconds",
	}
	for name, metric := range want {
		query, found := byName[name]
		if !found || len(query.Alternates) != 1 || !strings.Contains(query.Alternates[0], metric) {
			t.Errorf("%s alternates = %v, want one using %s", name, query.Alternates, metric)
		}
	}
}

func TestRenderRefusesMissingSelectors(t *testing.T) {
	catalog, _ := CatalogFor(spec.CatalogNameVllm)
	if _, err := catalog.Render(RenderInput{Selectors: Selectors{PodRegex: "a"}, Interval: 5 * time.Second}); err == nil {
		t.Error("a missing namespace regular expression must be an error, not a match-all")
	}
	if _, err := catalog.Render(RenderInput{Selectors: Selectors{NamespaceRegex: "n", PodRegex: "a"}}); err == nil {
		t.Error("a missing interval must be an error")
	}
	dcgm, _ := CatalogFor(spec.CatalogNameDcgm)
	withoutNodes := Selectors{NamespaceRegex: "n", PodRegex: "a"}
	if _, err := dcgm.Render(RenderInput{Selectors: withoutNodes, Interval: 5 * time.Second}); err == nil {
		t.Error("a catalog that needs nodes must not render without them")
	}
}

func TestSelectorValuesAreEscaped(t *testing.T) {
	catalog, _ := CatalogFor(spec.CatalogNameVllm)
	input := RenderInput{Selectors: Selectors{Job: `odd"job`, NamespaceRegex: `a\.b`, PodRegex: "p"}, Interval: 5 * time.Second}
	rendered, err := catalog.Render(input)
	if err != nil {
		t.Fatal(err)
	}
	if text := rendered[0].Query; !strings.Contains(text, `job="odd\"job"`) || !strings.Contains(text, `namespace=~"a\\.b"`) {
		t.Errorf("query = %s", text)
	}
	if got := AnyOf("a.b", "c"); got != `a\.b|c` {
		t.Errorf("AnyOf = %s", got)
	}
}

func TestCatalogNameOf(t *testing.T) {
	if name, err := CatalogNameOf("dcgm"); err != nil || name != spec.CatalogNameDcgm {
		t.Errorf("dcgm: %v, %v", name, err)
	}
	if _, err := CatalogNameOf("nvidia"); err == nil {
		t.Error("a name outside the schema enum must be rejected")
	}
	if _, err := CatalogFor(spec.CatalogNameExtra); err == nil {
		t.Error("extra has no catalog file")
	}
}

func TestParseCatalogRejectsBadFiles(t *testing.T) {
	good := "name: vllm\nversion: 1\njob_role: target\nmarker:\n  query: up\nqueries:\n  - name: a\n    query: up\n"
	cases := map[string]string{
		"unknown field":   good + "surprise: 1\n",
		"unknown catalog": strings.Replace(good, "name: vllm", "name: nvidia", 1),
		"extra":           strings.Replace(good, "name: vllm", "name: extra", 1),
		"unknown role":    strings.Replace(good, "job_role: target", "job_role: gateway", 1),
		"duplicate name":  good + "  - name: a\n    query: up\n",
		"bad series name": strings.Replace(good, "name: a", "name: Bad-Name", 1),
		"no marker":       strings.Replace(good, "  query: up\nqueries", "  query: ''\nqueries", 1),
		"version zero":    strings.Replace(good, "version: 1", "version: 0", 1),
		"empty query":     strings.Replace(good, "    query: up\n", "    query: ''\n", 1),
	}
	if _, err := parseCatalog([]byte(good)); err != nil {
		t.Fatalf("the good catalog failed: %v", err)
	}
	for name, document := range cases {
		if _, err := parseCatalog([]byte(document)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestLoadCatalogsRejectsAFileNamedAfterAnotherCatalog(t *testing.T) {
	files := fstest.MapFS{"catalogs/dcgm.yaml": {Data: []byte("name: vllm\nversion: 1\njob_role: target\nmarker:\n  query: up\nqueries:\n  - name: a\n    query: up\n")}}
	if _, err := loadCatalogs(files); err == nil {
		t.Error("the file name and the catalog name must agree")
	}
}

func TestRenderedNamesAreStable(t *testing.T) {
	catalog, _ := CatalogFor(spec.CatalogNameVllm)
	rendered, _ := catalog.Render(renderInputFor(catalog, 5*time.Second))
	names := make([]string, len(rendered))
	for i, query := range rendered {
		names[i] = string(query.Entry.Name)
	}
	if !slices.Contains(names, "ttft_seconds") || names[0] != "ttft_seconds" {
		t.Errorf("names = %v", names)
	}
}

func TestRenderAllCoversEveryCatalogWithoutIO(t *testing.T) {
	rendered, err := RenderAll(RenderInput{Selectors: testSelectors, StageWindow: time.Minute, Interval: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) != len(catalogNames) {
		t.Fatalf("rendered %d catalogs", len(rendered))
	}
	for _, catalog := range rendered {
		texts := catalog.PromQL()
		if len(texts) < 11 {
			t.Errorf("%s: only %d query texts", catalog.Name, len(texts))
		}
		for _, text := range texts {
			if problem := checkPromQLShape(text); problem != "" {
				t.Errorf("%s: %s: %s", catalog.Name, problem, text)
			}
		}
	}
	if _, err := RenderAll(RenderInput{Selectors: Selectors{NamespaceRegex: "n", PodRegex: "p"}, Interval: 5 * time.Second}); err == nil {
		t.Error("without nodes the dcgm catalog cannot render")
	}
}

func TestKubernetesCatalogHasAveragesAndReplicaSeries(t *testing.T) {
	catalog, _ := CatalogFor(spec.CatalogNameKubernetes)
	names := map[string]bool{}
	for _, entry := range catalog.Queries {
		names[string(entry.Name)] = true
	}
	for _, want := range []string{"cpu_cores_avg", "cpu_cores_max", "cpu_cores_p95", "memory_working_set_bytes_avg", "memory_working_set_bytes_max", "memory_working_set_bytes_p95", "deploy_available_min", "hpa_desired_max"} {
		if !names[want] {
			t.Errorf("kubernetes catalog lacks %s", want)
		}
	}
}

func TestKubeStateQueriesUseTheirOwnIntervalForTheirWindow(t *testing.T) {
	catalog, _ := CatalogFor(spec.CatalogNameKubernetes)
	input := RenderInput{Selectors: testSelectors, StageWindow: time.Minute, Interval: 10 * time.Second, KubeStateInterval: 30 * time.Second}
	rendered, err := catalog.Render(input)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"restarts":             "[2m])", // 4 x 30s
		"oom_killed":           "[2m])",
		"deploy_available_min": "[2m])",
		"hpa_desired_max":      "[2m])",
		"cpu_cores_max":        "[1m:15s])", // cAdvisor: stage window and its own step
		"cpu_throttle_ratio":   "[1m]",
	}
	for _, query := range rendered {
		if suffix, checked := want[string(query.Entry.Name)]; checked {
			if !strings.Contains(query.Query, suffix) {
				t.Errorf("%s = %s, want it to contain %s", query.Entry.Name, query.Query, suffix)
			}
			delete(want, string(query.Entry.Name))
		}
	}
	if len(want) != 0 {
		t.Errorf("entries not found: %v", want)
	}
	// Without a kube-state-metrics interval the cAdvisor interval applies.
	input.KubeStateInterval = 0
	if data, _ := input.templateData(); data["KubeStateWindow"] != "1m" {
		t.Errorf("fallback = %v", data)
	}
}
