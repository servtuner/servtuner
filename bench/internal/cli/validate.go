package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

func newValidateCommand(streams streams) *cobra.Command {
	var specPath string
	var strict, asJSON bool
	command := &cobra.Command{
		Use:   "validate --spec <path>",
		Short: "Check a RunSpec: schema, transport mode rules and stage lengths",
		Long: "Checks a RunSpec (YAML or JSON) against the schema, the transport rules " +
			"(mtls needs https://, platform needs http://) and the minimum stage length. " +
			"Exits 0 when valid and 2 when not, listing every problem. Stage-length findings " +
			"are warnings unless --strict is given.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return validateSpecFile(streams, specPath, strict, asJSON)
		},
	}
	command.Flags().StringVar(&specPath, "spec", "", "path to the RunSpec (YAML or JSON)")
	command.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors")
	command.Flags().BoolVar(&asJSON, "json", false, `print {"valid", "errors", "warnings"} as JSON on standard output (the api reads it); the exit code is unchanged`)
	_ = command.MarkFlagRequired("spec")
	return command
}

// validationReport is servtuner validate --json's output.
type validationReport struct {
	Valid    bool     `json:"valid"`
	Name     string   `json:"name,omitempty"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

func validateSpecFile(streams streams, specPath string, strict, asJSON bool) error {
	document, err := os.ReadFile(specPath)
	if err != nil {
		return usageError(fmt.Errorf("read spec: %w", err))
	}
	runSpec, _, err := spec.LoadRunSpec(document)
	if err != nil {
		if asJSON {
			writeValidationReport(streams, validationReport{Errors: problemLines(err.Error()), Warnings: []string{}})
		}
		return failure(validate.WrapParseError(err))
	}
	report := validate.Check(runSpec)
	if asJSON {
		errors := append([]string{}, report.Errors...)
		if strict {
			errors = append(errors, report.Warnings...)
		}
		writeValidationReport(streams, validationReport{Valid: len(errors) == 0, Name: runSpec.Name, Errors: errors, Warnings: append([]string{}, report.Warnings...)})
		if len(errors) > 0 {
			return failure(fmt.Errorf("%w: %d problem(s)", validate.ErrInvalidSpec, len(errors)))
		}
		return nil
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(streams.err, "warning: %s\n", warning)
	}
	if strict {
		report.Errors = append(report.Errors, report.Warnings...)
	}
	if err := report.Err(); err != nil {
		return failure(err)
	}
	fmt.Fprintf(streams.out, "ok: %s is a valid RunSpec (%s)\n", specPath, runSpec.Name)
	return nil
}

func writeValidationReport(streams streams, report validationReport) {
	encoded, _ := json.Marshal(report) // strings and booleans only
	fmt.Fprintln(streams.out, string(encoded))
}

// problemLines splits a parse error into one problem per line, dropping the
// "N problem(s):" header the schema validator writes.
func problemLines(message string) []string {
	var lines []string
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line != "" && !strings.Contains(line, "jsonschema validation failed with") {
			lines = append(lines, line)
		}
	}
	return lines
}
