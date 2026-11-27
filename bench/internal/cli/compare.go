package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func newCompareCommand(streams streams) *cobra.Command {
	var bundleDirs []string
	var objective, outPath string
	command := &cobra.Command{
		Use:   "compare --bundles <bundle>,<bundle>... [--objective NAME]",
		Short: "Compare analysed runs: configurations, objectives with intervals, the winner and the Pareto set",
		Long: "The experiment comparison. Runs with identical declared knobs are " +
			"repeats of one configuration. Every objective is estimated per configuration, with a Student t " +
			"interval across repeats or a bootstrap over the pooled request records, and a configuration wins " +
			"only when its interval clears the runner-up's (with single samples, by more than twice the 5% " +
			"noise floor). Runs that measured different workloads, ladders or SLOs are not comparable and " +
			"nothing wins. The bundles must have been analysed. --objective defaults to slo_rps_per_gpu for " +
			"vllm, slo_rps_per_core for routers and slo_max_rps otherwise. Writes comparison.json to --out, " +
			"or to standard output. Exits 0 on success, 2 for a bad argument or an invalid document, and 1 " +
			"when the comparison cannot be computed.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return compareBundles(command, streams, bundleDirs, objective, outPath)
		},
	}
	set := command.Flags()
	set.StringSliceVar(&bundleDirs, "bundles", nil, "analysed bundle directories")
	set.StringVar(&objective, "objective", "", "the primary objective (comparison.schema.json objectiveName)")
	set.StringVar(&outPath, "out", "", "where to write comparison.json (default: standard output)")
	_ = command.MarkFlagRequired("bundles")
	return command
}

func compareBundles(command *cobra.Command, streams streams, bundleDirs []string, objective, outPath string) error {
	var name spec.ObjectiveName
	if objective != "" {
		quoted, _ := json.Marshal(objective)
		if err := json.Unmarshal(quoted, &name); err != nil {
			return usageError(fmt.Errorf("--objective: %w", err))
		}
	}
	runs := make([]compare.Run, 0, len(bundleDirs))
	for _, dir := range bundleDirs {
		run, err := analyze.LoadComparisonRun(command.Context(), dir)
		if err != nil {
			if errors.Is(err, analyze.ErrNotAnalysed) {
				return usageError(fmt.Errorf("bundle %s: %w", dir, err))
			}
			return failure(fmt.Errorf("bundle %s: %w", dir, err))
		}
		runs = append(runs, run)
	}
	result, err := compare.Compare(runs, name, analyze.AnalyzerVersion)
	if err != nil {
		return usageError(err)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return failure(fmt.Errorf("encode comparison.json: %w", err))
	}
	if err := spec.Validate(spec.SchemaComparison, encoded); err != nil {
		return failure(fmt.Errorf("comparison.json breaks its schema: %w", err))
	}
	encoded = append(encoded, '\n')
	if outPath == "" {
		_, err = streams.out.Write(encoded)
		return err
	}
	if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
		return failure(fmt.Errorf("write %s: %w", outPath, err))
	}
	return nil
}
