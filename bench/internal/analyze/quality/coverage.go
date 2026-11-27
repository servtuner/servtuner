package quality

import (
	"fmt"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Coverage thresholds: the share of series with gaps that matters.
const (
	// GapFactor: a series has a gap when two consecutive samples are more than
	// twice the query step apart.
	GapFactor = 2
	// SeriesGapsIssuePercent is the share of series with gaps at which
	// series_gaps is raised.
	SeriesGapsIssuePercent = 10.0
)

// CheckCoverage reports which expected catalogs were collected, and how many
// series have gaps or no data. A catalog is expected when a query names it or
// its detection result is recorded. It returns a nil coverage when the run has
// no queries.json.
func CheckCoverage(queries *spec.PrometheusQueries, matrix input.MatrixSource) (*spec.Coverage, []spec.QualityIssue, error) {
	if queries == nil {
		return nil, nil, nil
	}
	collected := map[spec.CatalogName]bool{}
	details := map[spec.CatalogName]string{}
	for _, optional := range queries.OptionalCatalogs {
		collected[optional.Name] = optional.Result == spec.CatalogResultCollected
		if optional.Detail != nil {
			details[optional.Name] = *optional.Detail
		}
	}
	optionalSeen := map[spec.CatalogName]bool{}
	for _, optional := range queries.OptionalCatalogs {
		optionalSeen[optional.Name] = true
	}
	for _, query := range queries.Queries {
		if optionalSeen[query.Catalog] {
			continue
		}
		collected[query.Catalog] = collected[query.Catalog] || query.Result == spec.QueryResultCollected
	}

	var gapped int
	for _, query := range queries.Queries {
		hasGaps, err := hasGaps(query, matrix)
		if err != nil {
			return nil, nil, err
		}
		if hasGaps {
			gapped++
		}
	}
	// Catalogs is required by the schema even when no catalog was expected, so
	// it starts empty rather than nil (nil marshals as null).
	coverage := &spec.Coverage{Catalogs: []spec.CatalogCoverage{}}
	if len(queries.Queries) > 0 {
		coverage.SeriesWithGapsPercent = float64(gapped) / float64(len(queries.Queries)) * 100
	}

	names := make([]string, 0, len(collected))
	for name := range collected {
		names = append(names, string(name))
	}
	sort.Strings(names)
	var issues []spec.QualityIssue
	for _, name := range names {
		catalog := spec.CatalogName(name)
		coverage.Catalogs = append(coverage.Catalogs, spec.CatalogCoverage{Catalog: catalog, IsCollected: collected[catalog]})
		if !collected[catalog] {
			message := fmt.Sprintf("catalog %s was not collected; recommendations that depend on it have lower confidence", catalog)
			if detail := details[catalog]; detail != "" {
				message += ": " + detail
			}
			issues = append(issues, spec.QualityIssue{Code: spec.QualityIssueCodeCatalogMissing, Message: message})
		}
	}
	if coverage.SeriesWithGapsPercent >= SeriesGapsIssuePercent {
		issues = append(issues, spec.QualityIssue{
			Code:    spec.QualityIssueCodeSeriesGaps,
			Message: fmt.Sprintf("%.1f%% of the series have gaps or no data (%d of %d)", coverage.SeriesWithGapsPercent, gapped, len(queries.Queries)),
		})
	}
	return coverage, issues, nil
}

// hasGaps says whether a query's series is missing or has a gap. A query that
// returned nothing or failed has no data, which counts as a gap, unless the
// catalog declared an empty result normal (may_be_empty: no CPU limit, no HPA).
func hasGaps(query spec.Query, matrix input.MatrixSource) (bool, error) {
	if query.Result == spec.QueryResultEmpty && query.MayBeEmpty != nil && *query.MayBeEmpty {
		return false, nil
	}
	if query.Result != spec.QueryResultCollected {
		return true, nil
	}
	series, found, err := matrix.Matrix(input.SeriesRef{Catalog: query.Catalog, Name: string(query.Name)})
	if err != nil {
		return false, fmt.Errorf("read series %s/%s: %w", query.Catalog, query.Name, err)
	}
	if !found {
		return true, nil
	}
	limit := time.Duration(GapFactor * query.StepSeconds * float64(time.Second))
	for _, one := range series {
		for i := 1; i < len(one.Samples); i++ {
			if one.Samples[i].At.Sub(one.Samples[i-1].At) > limit {
				return true, nil
			}
		}
	}
	return false, nil
}
