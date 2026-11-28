package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

func newPresetsCommand(streams streams) *cobra.Command {
	return &cobra.Command{
		Use:   "presets",
		Short: "Print the workload presets built into this bench, as JSON",
		Long: "Prints every workload preset embedded in this binary as a " +
			"JSON array of WorkloadPreset documents, sorted by name and version. The api serves " +
			"them at GET /workload-presets, so the catalog has one source.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return printCatalog(streams, "presets", spec.WorkloadPresets)
		},
	}
}

func newSpacesCommand(streams streams) *cobra.Command {
	return &cobra.Command{
		Use:   "spaces",
		Short: "Print the starting search spaces built into this bench, as JSON",
		Long: "Prints every starting search space embedded in this binary (schema/search-spaces) " +
			"as a JSON array of SearchSpaceTemplate documents, sorted by name and version. The api serves " +
			"them at GET /search-spaces for New session, so the catalog has one source.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return printCatalog(streams, "search spaces", spec.SearchSpaceTemplates)
		},
	}
}

func newKnobsCommand(streams streams) *cobra.Command {
	server := string(spec.KnobCatalogServerVllm)
	command := &cobra.Command{
		Use:   "knobs [--server vllm|sglang]",
		Short: "Print every flag of a model server as a knob, with the server's own description and limits, as JSON",
		Long: "Prints a knob catalog embedded in this binary as one KnobCatalog document: every " +
			"flag of vllm serve (the default) or of SGLang's server (--server sglang) that is a knob, with " +
			"its label, kind, default, choices and limits, read from the server's source at the pinned " +
			"version, marking the knobs servtuner captures and tunes. The api serves it at " +
			"GET /knobs[?server=sglang], so each catalog has one source.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return printCatalog(streams, "knob catalog", func() (spec.KnobCatalog, error) {
				return target.KnobCatalogFor(spec.KnobCatalogServer(server))
			})
		},
	}
	command.Flags().StringVar(&server, "server", server, "the model server: vllm or sglang")
	return command
}

// printCatalog writes an embedded catalog as one JSON line.
func printCatalog[Document any](streams streams, what string, load func() (Document, error)) error {
	catalog, err := load()
	if err != nil {
		return failure(fmt.Errorf("read the embedded %s: %w", what, err))
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return failure(fmt.Errorf("encode %s: %w", what, err))
	}
	_, err = fmt.Fprintln(streams.out, string(encoded))
	return err
}
