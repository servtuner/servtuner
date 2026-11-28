package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

func newVLLMArgsCommand(streams streams) *cobra.Command {
	var knobsPath string
	command := &cobra.Command{
		Use:   "vllm-args --knobs <knobs.json>",
		Short: "Print the vLLM container args for a knob set, as JSON",
		Long: "Reads a JSON object of vLLM knobs (vllm.max_num_seqs: 256, ...) and prints the " +
			"container args that set them, as a JSON array, through the same table that captures " +
			"knobs from a pod's args, so the args always capture back as the same knobs. The api's " +
			"deployer uses it for managed targets. Exits 2 listing every " +
			"knob it cannot spell.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			document, err := os.ReadFile(knobsPath)
			if err != nil {
				return usageError(fmt.Errorf("read knobs: %w", err))
			}
			var knobs map[string]spec.KnobValue
			if err := json.Unmarshal(document, &knobs); err != nil {
				return usageError(fmt.Errorf("knobs must be a JSON object of knob values: %w", err))
			}
			args, err := target.RenderVLLMArgs(target.Knobs(knobs))
			if err != nil {
				return usageError(err)
			}
			if args == nil {
				args = []string{}
			}
			encoded, err := json.Marshal(args)
			if err != nil {
				return failure(fmt.Errorf("encode args: %w", err))
			}
			_, err = fmt.Fprintln(streams.out, string(encoded))
			return err
		},
	}
	command.Flags().StringVar(&knobsPath, "knobs", "", "path to a JSON object of vLLM knobs")
	_ = command.MarkFlagRequired("knobs")
	return command
}

func newServerArgsCommand(streams streams) *cobra.Command {
	var knobsPath, server string
	command := &cobra.Command{
		Use:   "server-args --server vllm|sglang --knobs <knobs.json>",
		Short: "Print a model server's container args for a knob set, as JSON",
		Long: "Reads a JSON object of knobs (vllm.max_num_seqs: 256, or sglang.mem_fraction_static: 0.8) " +
			"and prints the server args that set them, as a JSON array: through vLLM's knob table, as " +
			"vllm-args does, or through SGLang's catalog. The api's deployer uses it for managed " +
			"targets. Exits 2 listing every knob it cannot spell.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			render := target.RenderVLLMArgs
			switch server {
			case string(spec.KnobCatalogServerVllm):
			case string(spec.KnobCatalogServerSglang):
				render = target.RenderSGLangArgs
			default:
				return usageError(fmt.Errorf("--server %q: vllm or sglang", server))
			}
			document, err := os.ReadFile(knobsPath)
			if err != nil {
				return usageError(fmt.Errorf("read knobs: %w", err))
			}
			var knobs map[string]spec.KnobValue
			if err := json.Unmarshal(document, &knobs); err != nil {
				return usageError(fmt.Errorf("knobs must be a JSON object of knob values: %w", err))
			}
			args, err := render(target.Knobs(knobs))
			if err != nil {
				return usageError(err)
			}
			if args == nil {
				args = []string{}
			}
			encoded, err := json.Marshal(args)
			if err != nil {
				return failure(fmt.Errorf("encode args: %w", err))
			}
			_, err = fmt.Fprintln(streams.out, string(encoded))
			return err
		},
	}
	command.Flags().StringVar(&server, "server", string(spec.KnobCatalogServerVllm), "the model server: vllm or sglang")
	command.Flags().StringVar(&knobsPath, "knobs", "", "path to a JSON object of knobs")
	_ = command.MarkFlagRequired("knobs")
	return command
}
