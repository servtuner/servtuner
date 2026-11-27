package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/common/model"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// CollectRequest describes one run's server-side collection ("Collect").
type CollectRequest struct {
	// Window is the run window, in UTC, from the recorded phases; never a
	// wall-clock guess (CLAUDE.md, rule 5).
	Window Window
	// StageWindow is the duration of the shortest stage.
	StageWindow time.Duration
	// Intervals is the effective scrape interval, from targets.json.
	Intervals Resolution
	// StepOverride replaces the discovered interval as the query step when
	// positive (the RunSpec's prometheus.step).
	StepOverride time.Duration
	Selectors    Selectors
	// Required lists the catalogs that are always collected.
	Required []spec.CatalogName
	// Optional holds the probe results. Only those with result collected are
	// queried; every one of them is recorded in queries.json.
	Optional []spec.OptionalCatalogResult
	// ExtraQueries are the RunSpec's own queries, collected as catalog extra.
	ExtraQueries []spec.ExtraQuery
}

// SeriesResult is the raw answer to one query. Raw is the Prometheus API's data
// object {"resultType":"matrix","result":[...]} and is stored as it is, so
// analysis can be redone from it. Histograms are the _bucket, _sum and _count
// series of a family, not computed quantiles. Raw is nil when the query failed.
type SeriesResult struct {
	Catalog spec.CatalogName
	Name    spec.MetricName
	Raw     json.RawMessage
}

// Collection is what a run collected. Document is prometheus/queries.json, and
// Series the contents of prometheus/<catalog>/<name>.json. Nothing is written
// here; the pipeline stores both.
type Collection struct {
	Document spec.PrometheusQueries
	Series   []SeriesResult
}

// Collect runs every query of the required catalogs and of the detected
// optional ones over the run window, at the step of each catalog's job role.
//
// The _max, _p95, _min and _avg entries are rolling windows of the shortest
// stage (at least 4 x the scrape interval), evaluated at every step across the
// whole run. Analysis reads a stage's value from the step nearest the stage's
// end, where the window covers exactly that stage.
//
// Required catalogs are rendered before any query runs and fail the request. A
// detected optional catalog that cannot be rendered is recorded as an error
// result in the document and skipped. A query that fails or returns nothing is
// recorded in the document and never stops the collection, because a failed step
// still finalizes the bundle with what it has. The error is for a bad request or
// a cancelled context, and the Collection returned with it holds what was
// gathered so far.
func Collect(ctx context.Context, client *Client, request CollectRequest) (Collection, error) {
	if !request.Window.End.After(request.Window.Start) {
		return Collection{}, fmt.Errorf("collect: run window end %s is not after start %s", request.Window.End, request.Window.Start)
	}
	if request.Intervals.Interval <= 0 {
		return Collection{}, fmt.Errorf("collect: the scrape interval is unknown; resolve it from targets.json or assume it first")
	}
	required, optional, err := catalogsToCollect(request)
	if err != nil {
		return Collection{}, err
	}
	plans, err := planCatalogs(required, request)
	if err != nil {
		return Collection{}, err
	}
	collection := Collection{Document: spec.PrometheusQueries{
		Queries:          []spec.Query{},
		OptionalCatalogs: append([]spec.OptionalCatalogResult{}, request.Optional...),
	}}
	for _, name := range optional {
		optionalPlans, err := planCatalogs([]spec.CatalogName{name}, request)
		if err != nil {
			collection.failOptional(name, err)
			continue
		}
		plans = append(plans, optionalPlans...)
	}
	for _, plan := range plans {
		for _, query := range plan.queries {
			if err := ctx.Err(); err != nil {
				return collection, fmt.Errorf("collect: %w", err)
			}
			collection.add(runEntry(ctx, client, plan.catalog, query, request.Window, plan.step))
		}
	}
	extraStep := queryStep(request.StepOverride, request.Intervals.Interval)
	for _, extra := range request.ExtraQueries {
		if err := ctx.Err(); err != nil {
			return collection, fmt.Errorf("collect: %w", err)
		}
		collection.add(runQuery(ctx, client, spec.CatalogNameExtra, extra.Name, extra.Query, nil, request.Window, extraStep))
	}
	return collection, nil
}

// catalogPlan is one catalog's rendered queries and the step to run them with.
type catalogPlan struct {
	catalog spec.CatalogName
	queries []RenderedQuery
	step    time.Duration
}

// planCatalogs renders every catalog before any query runs, so a bad request
// (a catalog that needs nodes nobody named) fails the collection at once and not
// halfway through.
func planCatalogs(names []spec.CatalogName, request CollectRequest) ([]catalogPlan, error) {
	plans := make([]catalogPlan, 0, len(names))
	for _, name := range names {
		catalog, err := CatalogFor(name)
		if err != nil {
			return nil, fmt.Errorf("collect: %w", err)
		}
		interval := request.Intervals.For(catalog.JobRole).Interval
		rendered, err := catalog.Render(RenderInput{Selectors: request.Selectors, StageWindow: request.StageWindow, Interval: interval,
			KubeStateInterval: request.Intervals.For(spec.JobRoleKubeStateMetrics).Interval,
			CadvisorJob:       request.Intervals.JobOf(spec.JobRoleCadvisor)})
		if err != nil {
			return nil, fmt.Errorf("collect: %w", err)
		}
		plans = append(plans, catalogPlan{catalog: name, queries: rendered, step: queryStep(request.StepOverride, interval)})
	}
	return plans, nil
}

// catalogsToCollect lists the required catalogs, and separately the optional
// ones that were detected and are not already required, each once.
func catalogsToCollect(request CollectRequest) (required, optional []spec.CatalogName, err error) {
	seen := map[spec.CatalogName]bool{}
	for _, name := range request.Required {
		if name == spec.CatalogNameExtra {
			return nil, nil, fmt.Errorf("collect: %q is the RunSpec's extra_queries, not a catalog", name)
		}
		if !seen[name] {
			seen[name] = true
			required = append(required, name)
		}
	}
	for _, detected := range request.Optional {
		if detected.Result != spec.CatalogResultCollected || seen[detected.Name] {
			continue
		}
		if detected.Name == spec.CatalogNameExtra {
			return nil, nil, fmt.Errorf("collect: %q is the RunSpec's extra_queries, not a catalog", detected.Name)
		}
		seen[detected.Name] = true
		optional = append(optional, detected.Name)
	}
	return required, optional, nil
}

// failOptional turns a detected optional catalog into an error result, because a
// missing optional catalog never fails a run.
func (c *Collection) failOptional(name spec.CatalogName, cause error) {
	detail := fmt.Sprintf("the catalog was detected but its queries could not be built: %v", cause)
	for i, result := range c.Document.OptionalCatalogs {
		if result.Name == name {
			c.Document.OptionalCatalogs[i] = spec.OptionalCatalogResult{Name: name, Result: spec.CatalogResultError, Detail: &detail}
		}
	}
}

// queryStep is the discovered interval: a smaller step only interpolates and a
// larger one throws samples away.
func queryStep(override, interval time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return interval
}

func (c *Collection) add(query spec.Query, series SeriesResult) {
	c.Document.Queries = append(c.Document.Queries, query)
	if series.Raw != nil {
		c.Series = append(c.Series, series)
	}
}

func runEntry(ctx context.Context, client *Client, catalog spec.CatalogName, query RenderedQuery, window Window, step time.Duration) (spec.Query, SeriesResult) {
	recorded, series := runQuery(ctx, client, catalog, query.Entry.Name, query.Query, query.Alternates, window, step)
	if query.Entry.MayBeEmpty {
		mayBeEmpty := true
		recorded.MayBeEmpty = &mayBeEmpty
	}
	return recorded, series
}

// runQuery runs the primary query and, only when it returns no series, each
// alternate in turn. The recorded query is the one that matched, and the detail
// says which. When nothing matched, the primary query is recorded as empty.
func runQuery(ctx context.Context, client *Client, catalog spec.CatalogName, name spec.MetricName, query string, alternates []string, window Window, step time.Duration) (spec.Query, SeriesResult) {
	candidates := append([]string{query}, alternates...)
	var firstOutcome outcome
	for index, candidate := range candidates {
		current := attempt(ctx, client, candidate, window, step)
		if index == 0 {
			firstOutcome = current
		}
		if current.err != nil || len(current.matrix) > 0 {
			return record(catalog, name, candidate, window, step, current, index)
		}
	}
	return record(catalog, name, query, window, step, firstOutcome, 0)
}

// outcome is one query's raw answer.
type outcome struct {
	matrix   model.Matrix
	warnings []string
	err      error
}

func attempt(ctx context.Context, client *Client, query string, window Window, step time.Duration) outcome {
	matrix, warnings, err := client.QueryRange(ctx, query, window, step)
	return outcome{matrix: matrix, warnings: warnings, err: err}
}

func record(catalog spec.CatalogName, name spec.MetricName, query string, window Window, step time.Duration, result outcome, alternateIndex int) (spec.Query, SeriesResult) {
	recorded := spec.Query{
		Catalog:     catalog,
		Name:        name,
		Query:       query,
		StepSeconds: step.Seconds(),
		Window:      window.Spec(),
	}
	var notes []string
	if alternateIndex > 0 {
		notes = append(notes, fmt.Sprintf("the primary query returned no series; alternate %d matched", alternateIndex))
	}
	if len(result.warnings) > 0 {
		notes = append(notes, "prometheus warnings: "+strings.Join(result.warnings, "; "))
	}
	if result.err != nil {
		recorded.Result = spec.QueryResultError
		notes = append(notes, result.err.Error())
		recorded.Detail = joinNotes(notes)
		return recorded, SeriesResult{}
	}
	recorded.Result = spec.QueryResultCollected
	if len(result.matrix) == 0 {
		recorded.Result = spec.QueryResultEmpty
		result.matrix = model.Matrix{}
	}
	recorded.Detail = joinNotes(notes)
	raw, err := json.Marshal(struct {
		ResultType string       `json:"resultType"`
		Result     model.Matrix `json:"result"`
	}{ResultType: "matrix", Result: result.matrix})
	if err != nil {
		recorded.Result = spec.QueryResultError
		detail := fmt.Sprintf("encode result of %s: %v", name, err)
		recorded.Detail = &detail
		return recorded, SeriesResult{}
	}
	return recorded, SeriesResult{Catalog: catalog, Name: name, Raw: raw}
}

func joinNotes(notes []string) *string {
	if len(notes) == 0 {
		return nil
	}
	joined := strings.Join(notes, "; ")
	return &joined
}
