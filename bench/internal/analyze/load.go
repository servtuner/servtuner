package analyze

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze/charts"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/analyze/sizing"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/target"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

// parquetRecords is the seam to requests.parquet: it streams the rows with the
// bundle package's reader, which also reads a stream that was never finalized.
// It is the only place the analyzer touches the record format.
type parquetRecords struct{ bundleDir string }

// Records implements input.RecordSource.
func (p parquetRecords) Records() iter.Seq2[engine.RequestRow, error] {
	return bundle.ReadRequestRows(p.bundleDir)
}

// ParquetRecords returns the request records of a bundle directory.
func ParquetRecords(bundleDir string) input.RecordSource { return parquetRecords{bundleDir} }

// LoadRun reads every raw file the analysis needs from the bundle. The spec,
// phases and manifest are required; the environment, Prometheus files and
// snapshots are optional, and a run without them is analysed with less
// evidence, not refused. Every document goes through its schema before it is
// decoded.
func LoadRun(ctx context.Context, store storage.BundleStore, records input.RecordSource) (input.Run, error) {
	manifest, err := readDocument[spec.Manifest](ctx, store, bundle.ManifestJSON, spec.SchemaManifest)
	if err != nil {
		return input.Run{}, err
	}
	runSpec, err := readDocument[spec.RunSpec](ctx, store, bundle.ResolvedSpecJSON, spec.SchemaRunSpec)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return input.Run{}, err
		}
		return input.Run{}, validate.WrapParseError(err) // a bad RunSpec exits 2, like servtuner validate
	}
	phases, err := readDocument[spec.Phases](ctx, store, bundle.PhasesJSON, spec.SchemaPhases)
	if err != nil {
		return input.Run{}, err
	}
	run := input.Run{ID: manifest.RunId, Spec: runSpec, Phases: phases, Records: records, Matrix: NewBundleMatrix(ctx, store)}

	environment, err := readOptionalDocument[spec.Environment](ctx, store, bundle.EnvironmentJSON, spec.SchemaEnvironment)
	if err != nil {
		return input.Run{}, err
	}
	if environment != nil && environment.Pod != nil {
		run.BenchPod = *environment.Pod
		run.ShardPods = environment.ShardPods
	}
	if environment != nil && environment.Engine.MaxInFlight != nil {
		run.EngineMaxInFlight = *environment.Engine.MaxInFlight
	}
	if run.Targets, err = readOptionalDocument[spec.PrometheusTargets](ctx, store, bundle.PrometheusTargetsJSON, spec.SchemaPrometheusTargets); err != nil {
		return input.Run{}, err
	}
	if run.Queries, err = readOptionalDocument[spec.PrometheusQueries](ctx, store, bundle.PrometheusQueriesJSON, spec.SchemaPrometheusQueries); err != nil {
		return input.Run{}, err
	}
	if run.PreSnapshot, err = readOptionalDocument[spec.TargetSnapshot](ctx, store, bundle.SnapshotPreJSON, spec.SchemaTargetSnapshot); err != nil {
		return input.Run{}, err
	}
	if run.InfraPrices, err = readOptionalDocument[spec.InfraPricing](ctx, store, bundle.InfraPricingJSON, spec.SchemaInfraPricing); err != nil {
		return input.Run{}, err
	}
	if run.Datasheet, err = readOptionalFile(ctx, store, bundle.DatasheetJSON); err != nil {
		return input.Run{}, err
	}
	if run.TokenPricing, err = readOptionalDocument[spec.TokenPricing](ctx, store, bundle.TokenPricingJSON, spec.SchemaTokenPricing); err != nil {
		return input.Run{}, err
	}
	if run.PostSnapshot, err = readOptionalDocument[spec.TargetSnapshot](ctx, store, bundle.SnapshotPostJSON, spec.SchemaTargetSnapshot); err != nil {
		return input.Run{}, err
	}
	run.CapturedKnobs, err = capturedKnobs(runSpec, firstSnapshot(run))
	if err != nil {
		return input.Run{}, err
	}
	if run.ConfigDocuments, err = preRunConfigDocuments(ctx, store); err != nil {
		return input.Run{}, err
	}
	// environment.json holds every knob the adapter captured, including those
	// read from the target's config API (Bifrost's concurrency and buffer
	// size), which the pod snapshot alone does not show (environment.schema.json).
	if environment != nil && len(environment.Knobs) > 0 {
		if run.CapturedKnobs == nil {
			run.CapturedKnobs = map[string]spec.KnobValue{}
		}
		for name, value := range environment.Knobs {
			run.CapturedKnobs[name] = value
		}
	}
	return run, nil
}

// capturedKnobs reads the knobs back out of the snapshot with the target
// adapter that wrote them (target.KnobsFromWorkload), so the extraction rules
// live in one place. Knobs that only the live endpoints reveal are not in the
// snapshot document; the pipeline passes those in input.Run.CapturedKnobs
// itself.
func capturedKnobs(runSpec spec.RunSpec, snapshot *spec.TargetSnapshot) (map[string]spec.KnobValue, error) {
	if snapshot == nil {
		return nil, nil
	}
	adapter, err := target.AdapterFor(runSpec.Target.Type)
	if err != nil {
		return nil, err
	}
	for _, workload := range snapshot.Workloads {
		if workload.Role == spec.WorkloadRoleTarget {
			return adapter.KnobsFromWorkload(workload), nil
		}
	}
	return nil, nil
}

// buildCharts builds the chart-ready files from the run's records, with each
// stage's band taken from the analysis it accompanies.
func buildCharts(run input.Run, result spec.AnalysisResult) (charts.Documents, error) {
	windows, err := input.StagesFromPhases(run.Phases)
	if err != nil {
		return charts.Documents{}, err
	}
	stages := make([]charts.Stage, len(windows))
	for i, window := range windows {
		window.OfferedRPS = result.Stages[i].OfferedRps
		quality := result.Quality.Stages[i]
		stages[i] = charts.Stage{Window: window, IsValid: quality.IsValid, IsExcluded: quality.IsExcluded}
	}
	return charts.Build(stages, run.Records, AnalyzerVersion)
}

// AnalyzeBundle recomputes the analysis of a bundle directory from its raw
// files and writes analysis.json, recommendations.json, the capacity table
// (JSON and CSV) and patches/resources.yaml; the JSON documents are validated
// against their schemas first. It is
// what `servtuner analyze` and the pipeline's analyze step both call.
func AnalyzeBundle(ctx context.Context, bundleDir string) (spec.AnalysisResult, error) {
	store := storage.NewFileStore(bundleDir)
	run, err := LoadRun(ctx, store, ParquetRecords(bundleDir))
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	result, err := Analyze(run)
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	outputs, err := Recommend(run, result)
	if err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("recommendations: %w", err)
	}
	if err := applyInfraCost(run, &outputs); err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("infrastructure cost: %w", err)
	}
	applyUnitCosts(result, &outputs)
	stageWindows, err := input.StagesFromPhases(run.Phases)
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	if outputs.Capacity.TokenCost, err = buildTokenCost(run, result, stageWindows); err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("token cost: %w", err)
	}
	model, err := BuildCapacityModel(run.Spec, run.ID, result, outputs, run.CapturedKnobs)
	if err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("capacity model: %w", err)
	}
	chartFiles, err := buildCharts(run, result)
	if err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("charts: %w", err)
	}
	windows, err := input.StagesFromPhases(run.Phases)
	if err != nil {
		return spec.AnalysisResult{}, err
	}
	serverChart, err := buildServerChart(run, windows)
	if err != nil {
		return spec.AnalysisResult{}, fmt.Errorf("server chart: %w", err)
	}
	writer := bundle.NewWriter(store)
	for _, document := range []struct {
		name, schema string
		content      any
	}{
		{bundle.AnalysisJSON, spec.SchemaAnalysis, result},
		{bundle.ChartClientTimeseriesJSON, spec.SchemaChartTimeseries, chartFiles.Timeseries},
		{bundle.ChartHistogramsJSON, spec.SchemaChartHistograms, chartFiles.Histograms},
		{bundle.ChartInFlightJSON, spec.SchemaChartInFlight, chartFiles.InFlight},
		{bundle.ChartServerJSON, spec.SchemaChartServer, serverChart},
		{bundle.RecommendationsJSON, spec.SchemaRecommendations, outputs.Recommendations},
		{bundle.CapacityJSON, spec.SchemaCapacity, outputs.Capacity},
		{bundle.CapacityModelJSON, spec.SchemaCapacityModel, model},
	} {
		if err := writer.WriteDocument(ctx, document.name, document.schema, document.content); err != nil {
			return spec.AnalysisResult{}, err
		}
	}
	if err := writer.WriteFile(ctx, bundle.CapacityCSV, sizing.CapacityCSV(outputs.Capacity)); err != nil {
		return spec.AnalysisResult{}, err
	}
	if err := writer.WriteFile(ctx, bundle.ResourcesPatch, outputs.ResourcesPatch); err != nil {
		return spec.AnalysisResult{}, err
	}
	if outputs.TargetPatch != nil {
		if err := writer.WriteFile(ctx, bundle.BifrostPatch, outputs.TargetPatch); err != nil {
			return spec.AnalysisResult{}, err
		}
	}
	return result, nil
}

func readDocument[T any](ctx context.Context, store storage.BundleStore, name, schemaName string) (T, error) {
	var zero T
	content, err := readAll(ctx, store, name)
	if err != nil {
		return zero, err
	}
	// A bundle servtuner 0.1 wrote is read in today's shape (spec/legacy.go).
	if content, err = spec.UpgradeBundleDocument(schemaName, content); err != nil {
		return zero, fmt.Errorf("read %s: %w", name, err)
	}
	document, err := spec.Parse[T](schemaName, content)
	if err != nil {
		return zero, fmt.Errorf("read %s: %w", name, err)
	}
	return document, nil
}

// readOptionalFile returns a file's bytes, or nil when the bundle does not have it.
func readOptionalFile(ctx context.Context, store storage.BundleStore, name string) ([]byte, error) {
	content, err := readAll(ctx, store, name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return content, err
}

// readOptionalDocument returns nil for a file the bundle does not have.
func readOptionalDocument[T any](ctx context.Context, store storage.BundleStore, name, schemaName string) (*T, error) {
	document, err := readDocument[T](ctx, store, name, schemaName)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &document, nil
}

// preRunConfigDocuments reads target/config/pre-*, keyed by document name.
func preRunConfigDocuments(ctx context.Context, store storage.BundleStore) (map[string][]byte, error) {
	prefix := bundle.TargetConfigFile(string(spec.SnapshotPhasePre), "")
	names, err := store.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", prefix, err)
	}
	documents := map[string][]byte{}
	for _, name := range names {
		content, err := readAll(ctx, store, name)
		if err != nil {
			return nil, err
		}
		documents[strings.TrimPrefix(name, prefix)] = content
	}
	return documents, nil
}
