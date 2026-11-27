package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/propose"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func newProposeCommand(streams streams) *cobra.Command {
	var spacePath, infeasiblePath, outPath string
	var bundleDirs []string
	command := &cobra.Command{
		Use:   "propose --space space.yaml [--bundles <bundle>,<bundle>...] [--infeasible <file>] [--out proposal.json]",
		Short: "Choose the next configuration of a tuning session, or say the search is done",
		Long: "The tuning loop's choice of what to run next. It reads the search space, " +
			"the boot-probe results (--infeasible: boots that failed, and those that worked with their c_kv) " +
			"and the session's analysed bundles, and writes the next configurations to boot or run, each with " +
			"its ladder and a rationale, or a done verdict. Tiers run in order (feasibility, structural, " +
			"scheduler, fine_tuning), then the best configurations are confirmed with the full ladder and " +
			"repeats. It is pure: it reads files, writes JSON, and gives byte-identical output for the same " +
			"inputs. The session's budget is the caller's. Writes to --out, or to standard output. Exits 0 on " +
			"success, 2 for a bad argument or an invalid space, and 1 when no proposal can be computed.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runPropose(command, streams, spacePath, infeasiblePath, bundleDirs, outPath)
		},
	}
	set := command.Flags()
	set.StringVar(&spacePath, "space", "", "the search space (YAML or JSON)")
	set.StringSliceVar(&bundleDirs, "bundles", nil, "analysed bundle directories of the session so far")
	set.StringVar(&infeasiblePath, "infeasible", "", "boot-probe results (YAML or JSON)")
	set.StringVar(&outPath, "out", "", "where to write the proposal (default: standard output)")
	_ = command.MarkFlagRequired("space")
	return command
}

func runPropose(command *cobra.Command, streams streams, spacePath, infeasiblePath string, bundleDirs []string, outPath string) error {
	spaceDocument, err := os.ReadFile(spacePath)
	if err != nil {
		return usageError(fmt.Errorf("read --space: %w", err))
	}
	space, err := propose.ParseSearchSpace(spaceDocument)
	if err != nil {
		return usageError(err)
	}
	var boots propose.BootResults
	if infeasiblePath != "" {
		document, err := os.ReadFile(infeasiblePath)
		if err != nil {
			return usageError(fmt.Errorf("read --infeasible: %w", err))
		}
		if boots, err = propose.ParseBootResults(document); err != nil {
			return usageError(err)
		}
	}
	evidence, err := propose.LoadEvidence(command.Context(), bundleDirs)
	if err != nil {
		if errors.Is(err, analyze.ErrNotAnalysed) {
			return usageError(err)
		}
		return failure(err)
	}
	proposal, err := propose.Propose(space, boots, evidence)
	if err != nil {
		if errors.Is(err, propose.ErrInvalidInput) {
			return usageError(err)
		}
		return failure(err)
	}
	encoded, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		return failure(fmt.Errorf("encode the proposal: %w", err))
	}
	if err := spec.Validate(spec.SchemaProposal, encoded); err != nil {
		return failure(fmt.Errorf("the proposal breaks its schema: %w", err))
	}
	encoded = append(encoded, '\n')
	if outPath != "" {
		if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
			return failure(fmt.Errorf("write %s: %w", outPath, err))
		}
		return nil
	}
	_, err = streams.out.Write(encoded)
	return err
}
