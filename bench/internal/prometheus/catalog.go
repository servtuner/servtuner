package prometheus

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// The catalogs are versioned PromQL, one file per catalog. They are
// embedded so a bench binary carries the queries its bundles record.
//
// Entries come in three kinds. Raw entries (histogram families, counters,
// gauges) are the series as scraped, stored for later analysis. Entries ending in
// _max, _p95, _min or _avg are rolling windows of the shortest stage, evaluated
// at every step across the run: analysis reads each stage's value at the step
// nearest that stage's end. The rest are derived series such as rates and ratios.
//
//go:embed catalogs/*.yaml
var catalogFiles embed.FS

const (
	catalogsDirectory = "catalogs"
	catalogSuffix     = ".yaml"
)

// Template is a PromQL text with <<.Variable>> placeholders (RenderInput lists
// them). The delimiters are << and >> because PromQL uses {{ in matchers.
type Template struct {
	Query string `json:"query"`
	// LabelVariants renders the query once for every map of extra variables and
	// joins the results with "or". It covers label names that differ between
	// exporter versions or scrape setups (DCGM's hostname/Hostname).
	LabelVariants []map[string]string `json:"label_variants,omitempty"`
}

// Entry is one named query of a catalog. The raw result is stored as
// prometheus/<catalog>/<name>.json.
type Entry struct {
	Name        spec.MetricName `json:"name"`
	Description string          `json:"description,omitempty"`
	Template
	// Alternates are tried in order when the query returns no series, for
	// metrics renamed between target versions (vLLM "Renames"). The
	// query that matched is the one recorded.
	Alternates []string `json:"alternates,omitempty"`
	// MayBeEmpty marks a series whose absence is a normal state of the cluster,
	// not missing data: no CPU limit means no throttling series, no HPA means no
	// desired replicas. It is recorded in queries.json for the quality gate.
	MayBeEmpty bool `json:"may_be_empty,omitempty"`
}

// Catalog is one parsed catalogs/<name>.yaml.
type Catalog struct {
	Name    spec.CatalogName `json:"name"`
	Version int              `json:"version"`
	// JobRole says which scrape job feeds the catalog, so queries use that job's
	// interval.
	JobRole     spec.JobRole `json:"job_role"`
	Description string       `json:"description,omitempty"`
	// Marker is the series whose presence means the catalog is available on
	// this cluster. Any catalog can be declared optional in a RunSpec; Marker is
	// what probing evaluates.
	Marker  Template `json:"marker"`
	Queries []Entry  `json:"queries"`
}

var (
	loadOnce       sync.Once
	loadedCatalogs map[spec.CatalogName]Catalog
	errLoad        error
)

// Catalogs returns every embedded catalog by name.
func Catalogs() (map[spec.CatalogName]Catalog, error) {
	loadOnce.Do(func() { loadedCatalogs, errLoad = loadCatalogs(catalogFiles) })
	return loadedCatalogs, errLoad
}

// CatalogFor returns one catalog by name.
func CatalogFor(name spec.CatalogName) (Catalog, error) {
	catalogs, err := Catalogs()
	if err != nil {
		return Catalog{}, err
	}
	catalog, found := catalogs[name]
	if !found {
		return Catalog{}, fmt.Errorf("no catalog named %q: it is not collected from a file (extra is the RunSpec's extra_queries)", name)
	}
	return catalog, nil
}

// CatalogNameOf turns the free-form string of a RunSpec's catalogs or
// optional_catalogs list into the generated enum, so the set of names stays
// defined by the schema.
func CatalogNameOf(text string) (spec.CatalogName, error) {
	var name spec.CatalogName
	if err := json.Unmarshal([]byte(strconv.Quote(text)), &name); err != nil {
		return "", fmt.Errorf("catalog name %q: %w", text, err)
	}
	return name, nil
}

func loadCatalogs(files fs.FS) (map[spec.CatalogName]Catalog, error) {
	entries, err := fs.ReadDir(files, catalogsDirectory)
	if err != nil {
		return nil, fmt.Errorf("read embedded catalogs: %w", err)
	}
	catalogs := map[spec.CatalogName]Catalog{}
	for _, entry := range entries {
		file := path.Join(catalogsDirectory, entry.Name())
		document, err := fs.ReadFile(files, file)
		if err != nil {
			return nil, fmt.Errorf("read catalog %s: %w", file, err)
		}
		catalog, err := parseCatalog(document)
		if err != nil {
			return nil, fmt.Errorf("catalog %s: %w", file, err)
		}
		if want := strings.TrimSuffix(entry.Name(), catalogSuffix); string(catalog.Name) != want {
			return nil, fmt.Errorf("catalog %s: name %q does not match the file name", file, catalog.Name)
		}
		catalogs[catalog.Name] = catalog
	}
	return catalogs, nil
}

// parseCatalog reads one catalog through JSON so that the generated enums
// (catalog name, job role, series name) validate their own values.
func parseCatalog(document []byte) (Catalog, error) {
	asJSON, err := yaml.YAMLToJSON(document)
	if err != nil {
		return Catalog{}, fmt.Errorf("convert from YAML: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(asJSON))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, err
	}
	if err := catalog.check(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (c Catalog) check() error {
	if c.Name == spec.CatalogNameExtra {
		return fmt.Errorf("%q is the RunSpec's extra_queries, not a catalog file", c.Name)
	}
	if c.Version < 1 {
		return fmt.Errorf("version must be at least 1, got %d", c.Version)
	}
	if strings.TrimSpace(c.Marker.Query) == "" {
		return fmt.Errorf("marker query is empty")
	}
	if len(c.Queries) == 0 {
		return fmt.Errorf("no queries")
	}
	var names []string
	for _, entry := range c.Queries {
		if strings.TrimSpace(entry.Query) == "" {
			return fmt.Errorf("query %q is empty", entry.Name)
		}
		if slices.Contains(names, string(entry.Name)) {
			return fmt.Errorf("query name %q appears twice", entry.Name)
		}
		names = append(names, string(entry.Name))
	}
	return nil
}
