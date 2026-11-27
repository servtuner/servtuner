package prometheus

import (
	"context"
	"fmt"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// ProbeOptionalCatalogs checks, before the load starts, which of the named
// catalogs are available on this cluster ("Detection"). It evaluates
// each catalog's marker series at one instant:
//
//   - a series is there: collected;
//   - the marker matches nothing: absent, with the marker in the detail;
//   - the query fails: error, with the cause.
//
// A missing optional catalog never fails a run, so a failed probe is reported in
// the result and not as a Go error. The Go error is for a name that is not a
// catalog at all.
func ProbeOptionalCatalogs(ctx context.Context, client *Client, names []spec.CatalogName, input RenderInput, at time.Time) ([]spec.OptionalCatalogResult, error) {
	results := make([]spec.OptionalCatalogResult, 0, len(names))
	for _, name := range names {
		catalog, err := CatalogFor(name)
		if err != nil {
			return nil, fmt.Errorf("probe optional catalog: %w", err)
		}
		results = append(results, probeCatalog(ctx, client, catalog, input, at))
	}
	return results, nil
}

func probeCatalog(ctx context.Context, client *Client, catalog Catalog, input RenderInput, at time.Time) spec.OptionalCatalogResult {
	marker, err := catalog.RenderMarker(input)
	if err != nil {
		return optionalResult(catalog.Name, spec.CatalogResultError, err.Error())
	}
	vector, err := client.QueryInstant(ctx, marker, at)
	if err != nil {
		return optionalResult(catalog.Name, spec.CatalogResultError, fmt.Sprintf("probe marker: %v", err))
	}
	if len(vector) == 0 {
		return optionalResult(catalog.Name, spec.CatalogResultAbsent, fmt.Sprintf("marker query returned no series: %s", marker))
	}
	return optionalResult(catalog.Name, spec.CatalogResultCollected, "")
}

func optionalResult(name spec.CatalogName, result spec.CatalogResult, detail string) spec.OptionalCatalogResult {
	probed := spec.OptionalCatalogResult{Name: name, Result: result}
	if detail != "" {
		probed.Detail = &detail
	}
	return probed
}
