package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateJSONReportsProblemsAndWarnings(t *testing.T) {
	valid := run("validate", "--json", "--spec", "../../../schema/examples/runspec/valid/off-cluster-vllm-tls-off.json")
	if valid.code != ExitOK || !strings.Contains(valid.stdout, `"valid":true`) {
		t.Fatalf("exit %d: %s", valid.code, valid.stdout)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"apiVersion":"servtuner.dev/v2","kind":"RunSpec","name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	invalid := run("validate", "--json", "--spec", bad)
	if invalid.code != ExitInvalidSpec {
		t.Errorf("an invalid spec still exits %d, got %d", ExitInvalidSpec, invalid.code)
	}
	var report struct {
		Valid  bool     `json:"valid"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(invalid.stdout), &report); err != nil {
		t.Fatalf("stdout is the JSON report: %v (%q)", err, invalid.stdout)
	}
	if report.Valid || len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "missing properties") {
		t.Errorf("one problem, without the validator's header line: %+v", report)
	}
}
