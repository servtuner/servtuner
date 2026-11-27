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
