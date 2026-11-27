package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestAnalyzeWritesAValidDeterministicAnalysis(t *testing.T) {
	dir := scenarioBundle(t, "clean-ladder")
	first := run("analyze", dir)
	if first.code != ExitOK {
		t.Fatalf("exit %d: %s", first.code, first.stderr)
	}
	written, err := os.ReadFile(filepath.Join(dir, "analysis.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaAnalysis, written); err != nil {
		t.Fatal(err)
	}
	if second := run("analyze", dir); second.code != ExitOK {
		t.Fatalf("second run exit %d: %s", second.code, second.stderr)
	}
	again, _ := os.ReadFile(filepath.Join(dir, "analysis.json"))
	if !bytes.Equal(written, again) {
		t.Error("running servtuner analyze twice must give the same bytes")
	}
}

func TestAnalyzeExitCodes(t *testing.T) {
	missing := run("analyze", filepath.Join(t.TempDir(), "nope"))
	if missing.code != ExitInvalidSpec {
		t.Errorf("a missing bundle directory exits %d, want 2", missing.code)
	}
	if got := run("analyze").code; got != ExitInvalidSpec {
		t.Errorf("no argument exits %d, want 2", got)
	}
	empty := run("analyze", t.TempDir())
	if empty.code != ExitRunFailed {
		t.Errorf("a bundle with no files exits %d, want 1", empty.code)
	}
	dir := scenarioBundle(t, "knobs")
	if err := os.WriteFile(filepath.Join(dir, "spec.resolved.json"), []byte(`{"kind":"RunSpec"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run("analyze", dir).code; got != ExitInvalidSpec {
		t.Errorf("an invalid RunSpec exits %d, want 2", got)
	}
}

func TestAnalyzeExitsFourWhenAnalysisCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := scenarioBundle(t, "knobs")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if got := run("analyze", dir); got.code != ExitBundleWriteFailed {
		t.Errorf("exit %d, want 4: %s", got.code, got.stderr)
	}
}

func scenarioBundle(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := analyzetest.ScenarioNamed(name).WriteBundle(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}
