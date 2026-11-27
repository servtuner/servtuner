package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/bundle"
)

func newAnalyzeCommand(streams streams) *cobra.Command {
	return &cobra.Command{
		Use:   "analyze <bundle>",
		Short: "Recompute analysis.json from a bundle's raw files",
		Long: "Recomputes analysis.json (the data quality gate, per-stage client statistics with " +
			"bootstrap confidence intervals, and the capacity model) from the bundle's raw files only, " +
			"and writes it into the bundle. The result is validated against its schema before it is " +
			"written, and running it twice gives the same bytes. Exits 0 on success, 1 when the " +
			"analysis cannot be computed, 2 for a bad argument or an invalid RunSpec, and 4 when " +
			"analysis.json cannot be written.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return analyzeBundle(command, streams, args[0])
		},
	}
}

func analyzeBundle(command *cobra.Command, streams streams, bundleDir string) error {
	info, err := os.Stat(bundleDir)
	if err != nil {
		return usageError(fmt.Errorf("bundle %s: %w", bundleDir, err))
	}
	if !info.IsDir() {
		return usageError(fmt.Errorf("bundle %s is not a directory", bundleDir))
	}
	result, err := analyze.AnalyzeBundle(command.Context(), bundleDir)
	if err != nil {
		return failure(err)
	}
	fmt.Fprintf(streams.out, "ok: wrote %s (quality %s, %d stage(s))\n", bundle.AnalysisJSON, result.Quality.Grade, len(result.Stages))
	return nil
}
