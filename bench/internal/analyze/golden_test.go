package analyze_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

var update = flag.Bool("update", false, "rewrite the expected analyses under testdata/analysis from the scenarios")

const goldenRoot = "../../testdata/analysis"

// writeScenario writes a scenario's bundle into a fresh directory.
func writeScenario(t testing.TB, scenario analyzetest.Scenario) string {
	t.Helper()
	dir := t.TempDir()
	if err := scenario.WriteBundle(dir); err != nil {
		t.Fatalf("write scenario %s: %v", scenario.Name, err)
	}
	return dir
}

// TestGoldenAnalyses compares the analysis of every scenario byte for byte with
// the expected analysis checked in beside it (compact JSON), and likewise its
// recommendations.json under recommendations/. The bundle itself is generated at test time from the scenario,
// which is the definition; only the answers are stored. After an intended
// change run `go test ./internal/analyze -run TestGoldenAnalyses -update` and
// review the diff.
func TestGoldenAnalyses(t *testing.T) {
	for _, scenario := range analyzetest.Scenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			_, dir := analyzeGolden(t, scenario.Name)
			compareGolden(t, filepath.Join(dir, bundle.AnalysisJSON), filepath.Join(goldenRoot, scenario.Name+".json"))
			written := filepath.Join(dir, bundle.RecommendationsJSON)
			expected := filepath.Join(goldenRoot, "recommendations", scenario.Name+".json")
			if _, err := os.Stat(written); err == nil {
				compareGolden(t, written, expected)
			} else if _, err := os.Stat(expected); err == nil {
				t.Errorf("%s wrote no %s, but %s expects one", scenario.Name, bundle.RecommendationsJSON, expected)
			}
		})
	}
}

// compareGolden compares a written file, compacted, with its expected copy,
// or rewrites the expected copy under -update.
func compareGolden(t *testing.T, writtenPath, expected string) {
	t.Helper()
	written, err := os.ReadFile(writtenPath)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, written); err != nil {
		t.Fatal(err)
	}
	compact.WriteByte('\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(expected), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(expected, compact.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("no expected file (run with -update): %v", err)
	}
	if !bytes.Equal(want, compact.Bytes()) {
		t.Errorf("%s differs from %s; review and run with -update if intended", writtenPath, expected)
	}
}

// internalDocReference matches a citation of servtuner's own design documents,
// which users of the Recommendations page do not have.
var internalDocReference = regexp.MustCompile(`§|\bSPEC\b|\bANALYSIS\b|docs/|\bGuard rail\b|\bRule [A-Z]\b`)

// TestRecommendationTextCitesNoInternalDocs checks the text the Recommendations
// page shows (operating-point caveats, and each recommendation's evidence and
// caveats) in every golden recommendations.json.
func TestRecommendationTextCitesNoInternalDocs(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(goldenRoot, "recommendations", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no golden recommendations: %v", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document spec.Recommendations
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		shown := append([]string{}, document.OperatingPoint.Caveats...)
		for _, recommendation := range document.Recommendations {
			shown = append(shown, recommendation.Caveats...)
			for _, evidence := range recommendation.Evidence {
				shown = append(shown, evidence.Text)
			}
		}
		for _, text := range shown {
			if internalDocReference.MatchString(text) {
				t.Errorf("%s: user-facing text cites an internal document: %q", filepath.Base(path), text)
			}
		}
	}
}
