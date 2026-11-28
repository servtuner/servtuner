package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/pipeline"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func newBootProbeCommand(streams streams) *cobra.Command {
	var requestPath, secretsDirFlag string
	command := &cobra.Command{
		Use:   "boot-probe --input <boot-probe-request.json>",
		Short: "Read a booted vLLM's or SGLang's KV capacity and print a boot result",
		Long: "Reads the KV capacity through the TLS and credential code a run uses, and prints a " +
			"tuningBootResult (boot-results.schema.json): for vLLM, vllm:cache_config_info from /metrics, with " +
			"c_kv = num_gpu_blocks x block_size / tokens_per_request; for SGLang, max_total_num_tokens from " +
			"/server_info, with c_kv = max_total_num_tokens / tokens_per_request and the pool in pages as " +
			"num_gpu_blocks. The tuning loop feeds it to servtuner propose " +
			". Exits 1 when the target cannot be read or serves no cache config, 2 for an " +
			"invalid request.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx := command.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			request, err := readValidated[spec.BootProbeRequest](requestPath, spec.SchemaBootProbeRequest)
			if err != nil {
				return usageError(err)
			}
			result, err := pipeline.BootProbe(ctx, secretsDir(secretsDirFlag), request)
			if err != nil {
				return failure(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return failure(fmt.Errorf("encode the boot result: %w", err))
			}
			_, err = fmt.Fprintln(streams.out, string(encoded))
			return err
		},
	}
	set := command.Flags()
	set.StringVar(&requestPath, "input", "", "the probe request (boot-probe-request.schema.json)")
	set.StringVar(&secretsDirFlag, "secrets-dir", "", "directory where the Secrets the target names are, one subdirectory per Secret (default: $SERVTUNER_SECRETS_DIR, else "+pipeline.DefaultSecretsDir+")")
	_ = command.MarkFlagRequired("input")
	return command
}
