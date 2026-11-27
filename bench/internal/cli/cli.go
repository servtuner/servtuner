// Package cli is the servtuner command line: run, validate, analyze, compare, plan, the
// tuning commands and the launcher.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/version"
)

// Execute runs the command line and returns the process exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	streams := streams{out: stdout, err: stderr}
	root := &cobra.Command{
		Use:           "servtuner",
		Short:         "Benchmark LLM serving stacks and analyze the runs",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	root.AddCommand(newRunCommand(streams), newValidateCommand(streams), newAnalyzeCommand(streams), newPlanCommand(streams), newCompareCommand(streams), newCheckTargetCommand(streams), newProposeCommand(streams), newPresetsCommand(streams), newSpacesCommand(streams), newKnobsCommand(streams), newVLLMArgsCommand(streams), newBootProbeCommand(streams), newLauncherCommand(streams))

	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintf(stderr, "%s%v\n", ErrorPrefix, err)
	var chosen exitError
	if errors.As(err, &chosen) {
		return chosen.code
	}
	// Anything cobra itself rejected (unknown command or flag, missing
	// required flag) is a command-line mistake.
	return ExitInvalidSpec
}

// ErrorPrefix starts the error servtuner prints last on stderr; the api strips it
// (api/src/servtuner_api/cli_contract.py).
const ErrorPrefix = "servtuner: "

// streams carries the writers of one invocation, so tests can capture them.
type streams struct {
	out io.Writer
	err io.Writer
}

func (s streams) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(s.err, nil))
}
