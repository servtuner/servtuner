package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

const schemaExamples = "../../../schema/examples"

func TestPlanWritesAValidPlanFromTheExamples(t *testing.T) {
	result := run("plan", "--input", filepath.Join(schemaExamples, "plan-request", "valid", "minimal.json"),
		"--models", filepath.Join(schemaExamples, "capacity-model", "valid", "bifrost-two-stages.json"))
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	if err := spec.Validate(spec.SchemaPlan, []byte(result.stdout)); err != nil {
		t.Errorf("plan.json is invalid: %v\n%s", err, result.stdout)
	}
}

func TestPlanRefusesInvalidDocuments(t *testing.T) {
	model := filepath.Join(schemaExamples, "capacity-model", "valid", "bifrost-two-stages.json")
	for name, args := range map[string][]string{
		"invalid request": {"plan", "--input", filepath.Join(schemaExamples, "plan-request", "invalid", "slos-empty.json"), "--models", model},
		"invalid model":   {"plan", "--input", filepath.Join(schemaExamples, "plan-request", "valid", "minimal.json"), "--models", filepath.Join(schemaExamples, "capacity-model", "invalid", "grade-unknown.json")},
		"missing models":  {"plan", "--input", filepath.Join(schemaExamples, "plan-request", "valid", "minimal.json")},
	} {
		if result := run(args...); result.code != ExitInvalidSpec || strings.Contains(result.stdout, "tiers") {
			t.Errorf("%s: exit %d", name, result.code)
		}
	}
}

func TestPlanWritesItsThreeFilesToADirectory(t *testing.T) {
	dir := t.TempDir()
	result := run("plan", "--input", filepath.Join(schemaExamples, "plan-request", "valid", "peak-baseline-zones.json"),
		"--models", filepath.Join(schemaExamples, "capacity-model", "valid", "bifrost-two-stages.json"), "--out-dir", dir)
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	values, err := os.ReadFile(filepath.Join(dir, "patches", "plan-values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# servtuner plan values", "# override: parameter bifrost.h_rep", "replicas:", "minReplicas:"} {
		if !strings.Contains(string(values), want) {
			t.Errorf("plan-values.yaml lacks %q:\n%s", want, values)
		}
	}
	markdown, err := os.ReadFile(filepath.Join(dir, "plan.md"))
	if err != nil || !strings.Contains(string(markdown), "# Capacity plan for 400 RPS") || !strings.Contains(string(markdown), "Replicas at peak") {
		t.Errorf("plan.md = %s, %v", markdown, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.json")); err != nil {
		t.Error(err)
	}
}
