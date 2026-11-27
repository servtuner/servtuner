package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/servtuner/servtuner/bench/internal/pipeline"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/target"
)

func newCheckTargetCommand(streams streams) *cobra.Command {
	var requestPath, secretsDirFlag, kubeconfig string
	command := &cobra.Command{
		Use:   "check-target --input <check-target-request.json>",
		Short: "Check that a run could reach, authenticate to and observe a target",
		Long: "Runs the target checks against the request's target with the TLS, " +
			"credential and Prometheus code a run uses: resolve, connect, tls, auth, model, generate (only " +
			"when the request asks), metrics, gpu_metrics and workload. Secrets are read from --secrets-dir as " +
			"servtuner run reads them. Writes check-target-result.json to standard output. Exits 0 whenever the " +
			"checks ran, whatever they found, and 2 for a bad argument or an invalid request.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return checkTarget(command.Context(), streams, requestPath, secretsDirFlag, kubeconfig)
		},
	}
	set := command.Flags()
	set.StringVar(&requestPath, "input", "", "the check request (check-target-request.schema.json)")
	set.StringVar(&secretsDirFlag, "secrets-dir", "", "directory where the Secrets the target names are, one subdirectory per Secret (default: $SERVTUNER_SECRETS_DIR, else "+pipeline.DefaultSecretsDir+")")
	set.StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig for reading a workload target's pods (default: in-cluster config; none means that check is a warning)")
	_ = command.MarkFlagRequired("input")
	return command
}

func checkTarget(ctx context.Context, streams streams, requestPath, secretsDirFlag, kubeconfig string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := readValidated[spec.CheckTargetRequest](requestPath, spec.SchemaCheckTargetRequest)
	if err != nil {
		return usageError(err)
	}
	result := pipeline.CheckTarget(ctx, pipeline.CheckOptions{
		SecretsDir: secretsDir(secretsDirFlag),
		KubeClient: func() (kubernetes.Interface, string, error) { return target.NewKubeClient(kubeconfig) },
	}, request)
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return failure(fmt.Errorf("encode the check result: %w", err))
	}
	if err := spec.Validate(spec.SchemaCheckTargetResult, encoded); err != nil {
		return failure(fmt.Errorf("the check result breaks its schema: %w", err))
	}
	_, err = streams.out.Write(append(encoded, '\n'))
	return err
}
