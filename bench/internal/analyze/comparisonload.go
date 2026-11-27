package analyze

import (
	"context"
	"errors"
	"fmt"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

// ErrNotAnalysed is returned for a bundle with no analysis.json.
var ErrNotAnalysed = errors.New("the bundle has no analysis.json: run servtuner analyze on it first")

// LoadComparisonRun reads what the experiment comparison needs from an analysed bundle directory: the manifest's run id, the
// resolved RunSpec, analysis.json, capacity_model.json when present, and the
// request records, read one stage at a time only when a latency objective
// asks for them.
func LoadComparisonRun(ctx context.Context, bundleDir string) (compare.Run, error) {
	store := storage.NewFileStore(bundleDir)
	manifest, err := readDocument[spec.Manifest](ctx, store, bundle.ManifestJSON, spec.SchemaManifest)
	if err != nil {
		return compare.Run{}, err
	}
	runSpec, err := readDocument[spec.RunSpec](ctx, store, bundle.ResolvedSpecJSON, spec.SchemaRunSpec)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return compare.Run{}, err
		}
		return compare.Run{}, validate.WrapParseError(err)
	}
	analysis, err := readDocument[spec.AnalysisResult](ctx, store, bundle.AnalysisJSON, spec.SchemaAnalysis)
	if errors.Is(err, storage.ErrNotFound) {
		return compare.Run{}, ErrNotAnalysed
	}
	if err != nil {
		return compare.Run{}, err
	}
	model, err := readOptionalDocument[spec.RunCapacityModel](ctx, store, bundle.CapacityModelJSON, spec.SchemaCapacityModel)
	if err != nil {
		return compare.Run{}, err
	}
	records := ParquetRecords(bundleDir)
	ttft := func(stageIndex int) ([]float64, error) {
		rows, err := rowsOfStage(records, stageIndex)
		if err != nil {
			return nil, fmt.Errorf("stage %d: %w", stageIndex, err)
		}
		values := make([]float64, 0, len(rows))
		for _, row := range rows {
			if value, ok := aggregate.TTFT(row); ok {
				values = append(values, value)
			}
		}
		return values, nil
	}
	return compare.Run{ID: manifest.RunId, Spec: runSpec, Analysis: analysis, Model: model, TTFT: ttft}, nil
}
