package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/plan"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func newPlanCommand(streams streams) *cobra.Command {
	var requestPath, outPath, outDir string
	var modelPaths, seriesPaths []string
	command := &cobra.Command{
		Use:   "plan --input <plan-request.json> --models <capacity_model.json>...",
		Short: "Plan replicas, HPA and totals for a traffic level from measured capacity models",
		Long: "Plans each capacity model against the request: the per-replica " +
			"SLO-max at the requested SLO, interpolated only between measured stages, then replicas, HPA, " +
			"totals, queue concurrency and sensitivity, with the request's overrides applied. " +
			"--payload-models adds runs of a tier's configuration at other request sizes, which only fit " +
			"its memory per in-flight request m(s). " +
			"Writes plan.json to --out, or to standard output; --out-dir writes plan.json, " +
			"patches/plan-values.yaml and plan.md there instead. It reads only the given files. Exits 0 on " +
			"success, 2 for a bad argument or an invalid document, and 1 when the plan cannot be computed.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return makePlan(streams, requestPath, modelPaths, seriesPaths, outPath, outDir)
		},
	}
	set := command.Flags()
	set.StringVar(&requestPath, "input", "", "the plan request (plan-request.schema.json)")
	set.StringSliceVar(&modelPaths, "models", nil, "capacity_model.json files, one per tier")
	set.StringSliceVar(&seriesPaths, "payload-models", nil, "capacity_model.json files of a payload-size series, for m(s)")
	set.StringVar(&outPath, "out", "", "where to write plan.json (default: standard output)")
	set.StringVar(&outDir, "out-dir", "", "write plan.json, patches/plan-values.yaml and plan.md into this directory")
	_ = command.MarkFlagRequired("input")
	_ = command.MarkFlagRequired("models")
	return command
}

func makePlan(streams streams, requestPath string, modelPaths, seriesPaths []string, outPath, outDir string) error {
	if outPath != "" && outDir != "" {
		return usageError(errors.New("--out and --out-dir cannot be combined"))
	}
	request, err := readValidated[spec.PlanRequest](requestPath, spec.SchemaPlanRequest)
	if err != nil {
		return usageError(err)
	}
	models, err := readModels(modelPaths)
	if err != nil {
		return usageError(err)
	}
	series, err := readModels(seriesPaths)
	if err != nil {
		return usageError(err)
	}
	result, err := plan.Make(request, models, series, analyze.AnalyzerVersion)
	if err != nil {
		return failure(err)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return failure(fmt.Errorf("encode plan.json: %w", err))
	}
	if err := spec.Validate(spec.SchemaPlan, encoded); err != nil {
		return failure(fmt.Errorf("plan.json breaks its schema: %w", err))
	}
	encoded = append(encoded, '\n')
	if outDir != "" {
		return writePlanFiles(outDir, request, result, encoded)
	}
	if outPath == "" {
		_, err = streams.out.Write(encoded)
		return err
	}
	if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
		return failure(fmt.Errorf("write %s: %w", outPath, err))
	}
	return nil
}

// readValidated reads a JSON document and parses it against its schema.
func readValidated[T any](path, schemaName string) (T, error) {
	var zero T
	raw, err := os.ReadFile(path)
	if err != nil {
		return zero, fmt.Errorf("read %s: %w", path, err)
	}
	document, err := spec.Parse[T](schemaName, raw)
	if err != nil {
		return zero, fmt.Errorf("%s: %w", path, err)
	}
	return document, nil
}

// writePlanFiles writes the planner's three output files.
func writePlanFiles(outDir string, request spec.PlanRequest, result spec.Plan, encoded []byte) error {
	values, err := plan.RenderValues(request, result)
	if err != nil {
		return failure(err)
	}
	files := map[string][]byte{
		bundle.PlanJSON: encoded, bundle.PlanValuesYAML: values, bundle.PlanMarkdown: plan.RenderMarkdown(request, result),
	}
	for name, content := range files {
		path := filepath.Join(outDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return failure(fmt.Errorf("create %s: %w", filepath.Dir(path), err))
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return failure(fmt.Errorf("write %s: %w", path, err))
		}
	}
	return nil
}

func readModels(paths []string) ([]spec.RunCapacityModel, error) {
	var models []spec.RunCapacityModel
	for _, path := range paths {
		model, err := readValidated[spec.RunCapacityModel](path, spec.SchemaCapacityModel)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, nil
}
