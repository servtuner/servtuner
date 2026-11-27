// Package bundle writes and reads a run bundle: the file names, the
// writer that checksums and orders them, and the requests.parquet encoding.
package bundle

// File names inside a bundle, defined once.
const (
	SpecYAML         = "spec.yaml"
	ResolvedSpecJSON = "spec.resolved.json"
	EnvironmentJSON  = "environment.json"
	PhasesJSON       = "phases.json"
	RequestsParquet  = "requests.parquet"
	// ClientSamplesParquet holds the self-monitoring samples (client/).
	ClientSamplesParquet = "client/samples.parquet"
	SummaryJSON          = "summary.json"
	ManifestJSON         = "manifest.json"
	// AnalysisJSON is the output of servtuner analyze.
	AnalysisJSON = "analysis.json"
	// RecommendationsJSON is servtuner analyze's sizing and advice, each value with
	// its derivation.
	RecommendationsJSON = "recommendations.json"
	// CapacityJSON and CapacityCSV are the capacity table.
	CapacityJSON = "capacity.json"
	CapacityCSV  = "capacity.csv"
	// CapacityModelJSON is the planner's input for this run.
	CapacityModelJSON = "capacity_model.json"
	// ResourcesPatch is the per-container resources block to apply.
	ResourcesPatch = "patches/resources.yaml"
	// BifrostPatch is the Bifrost config delta, when the target is Bifrost.
	BifrostPatch = "patches/bifrost.yaml"
	// The planner's output files, written by servtuner plan --out-dir.
	PlanJSON       = "plan.json"
	PlanValuesYAML = "patches/plan-values.yaml"
	PlanMarkdown   = "plan.md"
	// The UI's chart-ready files, written by servtuner analyze.
	ChartClientTimeseriesJSON = "charts/client_timeseries.json"
	ChartHistogramsJSON       = "charts/histograms.json"
	ChartInFlightJSON         = "charts/inflight.json"
	ChartServerJSON           = "charts/server.json"
	// ComparisonJSON is servtuner compare's experiment comparison.
	ComparisonJSON = "comparison.json"
	// The Prometheus and snapshot files.
	PrometheusTargetsJSON = "prometheus/targets.json"
	PrometheusQueriesJSON = "prometheus/queries.json"
	SnapshotPreJSON       = "target/snapshot-pre.json"
	SnapshotPostJSON      = "target/snapshot-post.json"
	// InfraPricingJSON is the run's resolved infrastructure prices.
	InfraPricingJSON = "pricing/infra.json"
	// DatasheetJSON is the token pricing datasheet, byte for byte as fetched,
	// and TokenPricingJSON where it came from.
	DatasheetJSON    = "pricing/datasheet.json"
	TokenPricingJSON = "pricing/token.json"
)

// PrometheusSeriesFile is the bundle path of one collected catalog entry,
// prometheus/<catalog>/<name>.json.
func PrometheusSeriesFile(catalog, name string) string {
	return "prometheus/" + catalog + "/" + name + ".json"
}

// TargetConfigFile is where a target's redacted config document, captured in
// a snapshot phase, is stored: target/config/<phase>-<document>.
func TargetConfigFile(phase, document string) string {
	return TargetConfigPrefix + phase + "-" + document
}

// TargetConfigPrefix is the directory of the captured config documents.
const TargetConfigPrefix = "target/config/"

// PartsDirSuffix is appended to a streamed Parquet file's name to name the
// directory of part files that holds its rows until the file is finalized
// (stream.go). For example requests.parquet.d/part-0000.parquet.
const PartsDirSuffix = ".d"

// BundleVersion is the manifest's bundle_version. It changes only on a
// breaking change to the layout (schema/CLAUDE.md).
const BundleVersion = 1
