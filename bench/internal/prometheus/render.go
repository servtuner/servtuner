package prometheus

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/prometheus/common/model"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Selectors say which pods and nodes a run's queries cover. Namespace and pod
// are regular expressions (RE2, as PromQL's =~ uses); AnyOf builds one from
// names.
type Selectors struct {
	// Job is the scrape job of the targets under test. It defaults to TargetJob.
	Job string
	// NamespaceRegex and PodRegex cover the target pods and the bench pod
	// (client_bound needs the bench pod's CPU).
	NamespaceRegex string
	PodRegex       string
	// NodeRegex covers the nodes the target pods run on, for GPU series. It is
	// required only by catalogs that use it.
	NodeRegex string
}

// RenderInput is everything a catalog's templates are filled from.
type RenderInput struct {
	Selectors
	// StageWindow is the duration of the stages the series will be cut into: the
	// shortest stage of the run.
	StageWindow time.Duration
	// Interval is the scrape interval of the catalog's job role.
	Interval time.Duration
	// KubeStateInterval is the scrape interval of kube-state-metrics, which is
	// usually slower than cAdvisor's. Zero means the same as Interval.
	KubeStateInterval time.Duration
	// CadvisorJob is the cAdvisor job the interval was discovered from. When a
	// cluster has a dedicated job beside the cluster-wide one, both scrape the
	// same containers, so cAdvisor series must name one job or sums count every
	// container twice. Empty selects every job (no Prometheus to ask).
	CadvisorJob string
}

// Subquery step floor: cAdvisor refreshes its stats every 15s at best, so a
// finer step only repeats values.
const (
	minimumSubqueryStep   = 15 * time.Second
	minimumCounterRate    = time.Minute
	rateWindowIntervals   = 4
	templateLeftDelimiter = "<<"
	templateRightDelim    = ">>"
)

// anyNode matches every node name.
const anyNode = ".+"

// AnyOf joins names into a regular expression that matches exactly one of them.
func AnyOf(names ...string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return strings.Join(quoted, "|")
}

// templateData builds the variables of every template. Selector strings are
// matcher lists to place inside braces.
//
//	Job, Namespace, Pod            the raw, escaped values
//	Selector                       target series: job, namespace and pod
//	WorkloadSelector               namespace and pod (kube-state-metrics)
//	CadvisorPodSelector            cAdvisor series of the workload, from one job when known
//	CadvisorSelector               container series, without the pause container
//	NodeRegex                      present only when set
//	Window                         the stage window, never below 4 x interval
//	CounterRateWindow              inner window of a counter peak: >= 4 x interval and 1m
//	SubqueryStep, Step, Interval   the subquery step, the query step, the interval
//	NamespaceRegex                 the namespace regular expression
//	KubeStateWindow                Window from the kube-state-metrics interval
//
// The window rules follow the effective scrape interval.
func (in RenderInput) templateData() (map[string]any, error) {
	if in.NamespaceRegex == "" || in.PodRegex == "" {
		return nil, fmt.Errorf("selectors need a namespace and a pod regular expression, got %q and %q", in.NamespaceRegex, in.PodRegex)
	}
	job := in.Job
	if job == "" {
		job = TargetJob
	}
	namespace, pod := escapeLabelValue(in.NamespaceRegex), escapeLabelValue(in.PodRegex)
	workload := fmt.Sprintf(`namespace=~"%s",pod=~"%s"`, namespace, pod)
	cadvisorPod := fmt.Sprintf(`metrics_path="%s",%s`, cadvisorMetricsPath, workload)
	if in.CadvisorJob != "" {
		cadvisorPod = fmt.Sprintf(`job="%s",%s`, escapeLabelValue(in.CadvisorJob), cadvisorPod)
	}
	data := map[string]any{
		"Job":                 escapeLabelValue(job),
		"Selector":            fmt.Sprintf(`job="%s",%s`, escapeLabelValue(job), workload),
		"WorkloadSelector":    workload,
		"CadvisorPodSelector": cadvisorPod,
		"CadvisorSelector":    cadvisorPod + `,container!="",container!="POD"`,
		"PodRegex":            pod,
		"Window":              formatDuration(max(in.StageWindow, rateWindowIntervals*in.Interval)),
		"CounterRateWindow":   formatDuration(max(rateWindowIntervals*in.Interval, minimumCounterRate)),
		"SubqueryStep":        formatDuration(max(in.Interval, minimumSubqueryStep)),
		"Step":                formatDuration(in.Interval),
		"Interval":            formatDuration(in.Interval),
	}
	ksmInterval := in.KubeStateInterval
	if ksmInterval <= 0 {
		ksmInterval = in.Interval
	}
	data["NamespaceRegex"] = namespace
	data["KubeStateWindow"] = formatDuration(max(in.StageWindow, rateWindowIntervals*ksmInterval))
	if in.NodeRegex != "" {
		data["NodeRegex"] = escapeLabelValue(in.NodeRegex)
	}
	return data, nil
}

// formatDuration writes a duration in PromQL syntax ("1m30s").
func formatDuration(d time.Duration) string {
	return model.Duration(d).String()
}

// escapeLabelValue makes a value safe inside a double-quoted PromQL string.
func escapeLabelValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}

// RenderedQuery is one catalog entry with its templates filled in.
type RenderedQuery struct {
	Entry Entry
	// Query is the primary PromQL; Alternates are tried when it returns nothing.
	Query      string
	Alternates []string
}

// Render fills the templates of every entry. A template that uses a variable
// that is not set (NodeRegex without nodes) is an error, never an empty match.
func (c Catalog) Render(input RenderInput) ([]RenderedQuery, error) {
	if input.Interval <= 0 {
		return nil, fmt.Errorf("render catalog %s: scrape interval must be positive, got %s", c.Name, input.Interval)
	}
	data, err := input.templateData()
	if err != nil {
		return nil, fmt.Errorf("render catalog %s: %w", c.Name, err)
	}
	rendered := make([]RenderedQuery, 0, len(c.Queries))
	for _, entry := range c.Queries {
		query, err := renderTemplate(entry.Template, data)
		if err != nil {
			return nil, fmt.Errorf("render catalog %s query %s: %w", c.Name, entry.Name, err)
		}
		alternates := make([]string, 0, len(entry.Alternates))
		for _, alternate := range entry.Alternates {
			text, err := renderTemplate(Template{Query: alternate, LabelVariants: entry.LabelVariants}, data)
			if err != nil {
				return nil, fmt.Errorf("render catalog %s query %s alternate: %w", c.Name, entry.Name, err)
			}
			alternates = append(alternates, text)
		}
		rendered = append(rendered, RenderedQuery{Entry: entry, Query: query, Alternates: alternates})
	}
	return rendered, nil
}

// RenderMarker fills the marker query of the catalog. Markers use only the
// selectors, so no interval is needed. Without known nodes the probe asks
// whether the series exist anywhere, which is all detection needs.
func (c Catalog) RenderMarker(input RenderInput) (string, error) {
	if input.NodeRegex == "" {
		input.NodeRegex = anyNode
	}
	data, err := input.templateData()
	if err != nil {
		return "", fmt.Errorf("render marker of catalog %s: %w", c.Name, err)
	}
	marker, err := renderTemplate(c.Marker, data)
	if err != nil {
		return "", fmt.Errorf("render marker of catalog %s: %w", c.Name, err)
	}
	return marker, nil
}

func renderTemplate(source Template, data map[string]any) (string, error) {
	parsed, err := template.New("query").
		Delims(templateLeftDelimiter, templateRightDelim).
		Option("missingkey=error").
		Parse(strings.TrimSpace(source.Query))
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	variants := source.LabelVariants
	if len(variants) == 0 {
		variants = []map[string]string{nil}
	}
	texts := make([]string, 0, len(variants))
	for _, variant := range variants {
		variables := make(map[string]any, len(data)+len(variant))
		for name, value := range data {
			variables[name] = value
		}
		for name, value := range variant {
			variables[name] = value
		}
		var text strings.Builder
		if err := parsed.Execute(&text, variables); err != nil {
			return "", err
		}
		texts = append(texts, text.String())
	}
	return strings.Join(texts, " or "), nil
}

// RenderedCatalog is one catalog with every template filled in.
type RenderedCatalog struct {
	Name    spec.CatalogName
	Marker  string
	Queries []RenderedQuery
}

// PromQL lists every query text of the catalog: the marker, each primary query
// and each alternate. The e2e feeds them to Prometheus's /api/v1/format_query to
// check the syntax with the real parser.
func (c RenderedCatalog) PromQL() []string {
	texts := []string{c.Marker}
	for _, query := range c.Queries {
		texts = append(texts, query.Query)
		texts = append(texts, query.Alternates...)
	}
	return texts
}

// RenderAll renders every catalog for one stage window, scrape interval and set
// of selectors, without any I/O. The selectors must include nodes, because the
// DCGM catalog needs them.
func RenderAll(input RenderInput) ([]RenderedCatalog, error) {
	catalogs, err := Catalogs()
	if err != nil {
		return nil, err
	}
	names := make([]spec.CatalogName, 0, len(catalogs))
	for name := range catalogs {
		names = append(names, name)
	}
	slices.Sort(names)
	rendered := make([]RenderedCatalog, 0, len(names))
	for _, name := range names {
		catalog := catalogs[name]
		queries, err := catalog.Render(input)
		if err != nil {
			return nil, err
		}
		marker, err := catalog.RenderMarker(input)
		if err != nil {
			return nil, err
		}
		rendered = append(rendered, RenderedCatalog{Name: name, Marker: marker, Queries: queries})
	}
	return rendered, nil
}
