package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/storage"
)

// The analysis lists target/config/ through the store; a linked directory
// would hide it (no Bifrost config reached the advisor).
func TestTheAnalysisStagingShowsNestedFilesAndHidesTheOutputs(t *testing.T) {
	bundleDir, staging := t.TempDir(), t.TempDir()
	for _, name := range []string{
		bundle.TargetConfigFile("pre", "bifrost-config.json"), bundle.PhasesJSON,
		bundle.ManifestJSON, bundle.AnalysisJSON, bundle.ResourcesPatch,
	} {
		path := filepath.Join(bundleDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := mirrorForAnalysis(bundleDir, staging); err != nil {
		t.Fatal(err)
	}
	keys, err := storage.NewFileStore(staging).List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{bundle.PhasesJSON, bundle.TargetConfigFile("pre", "bifrost-config.json")}
	if !slices.Equal(keys, want) {
		t.Errorf("staging lists %v, want %v", keys, want)
	}
	if info, err := os.Lstat(filepath.Join(staging, "patches")); err != nil || !info.IsDir() {
		t.Errorf("patches/ must be a real directory in staging: %v", err)
	}
}

// `servtuner run --out ./bundle` gives a relative bundle directory; the links
// in staging must still reach the bundle's files.
func TestTheAnalysisStagingReadsARelativeBundleDirectory(t *testing.T) {
	work, staging := t.TempDir(), t.TempDir()
	t.Chdir(work)
	if err := os.MkdirAll("bundle", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("bundle", bundle.PhasesJSON), []byte(`{"stages":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mirrorForAnalysis("./bundle", staging); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(staging, bundle.PhasesJSON))
	if err != nil || string(content) != `{"stages":[]}` {
		t.Errorf("staging's %s: %q, %v", bundle.PhasesJSON, content, err)
	}
}
