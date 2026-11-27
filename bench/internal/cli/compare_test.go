package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze/analyzetest"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// analysedBundle writes the clean-ladder scenario as run id with knobs and
// analyses it, as a real run's bundle would be.
func analysedBundle(t *testing.T, id string, concurrency int) string {
	t.Helper()
	scenario := analyzetest.ScenarioNamed("clean-ladder")
	scenario.ID = id
	scenario.Knobs = map[string]any{"bifrost.concurrency": concurrency}
	dir := t.TempDir()
	if err := scenario.WriteBundle(dir); err != nil {
		t.Fatal(err)
	}
	if result := run("analyze", dir); result.code != ExitOK {
		t.Fatalf("analyze exit %d: %s", result.code, result.stderr)
	}
	return dir
}

func TestCompareWritesAValidDeterministicComparison(t *testing.T) {
	dirs := []string{
		analysedBundle(t, "01JBX0000000000000000000A1", 100),
		analysedBundle(t, "01JBX0000000000000000000A2", 100),
		analysedBundle(t, "01JBX0000000000000000000B1", 400),
		analysedBundle(t, "01JBX0000000000000000000B2", 400),
	}
	bundles := dirs[0] + "," + dirs[1] + "," + dirs[2] + "," + dirs[3]
	out := filepath.Join(t.TempDir(), "comparison.json")
	result := run("compare", "--bundles", bundles, "--objective", "slo_max_rps", "--out", out)
	if result.code != ExitOK {
		t.Fatalf("exit %d: %s", result.code, result.stderr)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := spec.Parse[spec.Comparison](spec.SchemaComparison, written)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Configurations) != 2 || len(comparison.Configurations[0].RunIds) != 2 {
		t.Fatalf("configurations: %+v", comparison.Configurations)
	}
	if !comparison.Comparability.IsComparable || comparison.ReferenceStageIndex == nil {
		t.Errorf("identical ladders over one workload are comparable: %+v", comparison.Comparability)
	}
	if comparison.Recommendation != nil {
		t.Errorf("identical records cannot give a significant winner: %+v", comparison.Recommendation)
	}
	if ttft := comparison.Configurations[0].ReferenceTtftP95Seconds; ttft == nil || ttft.Value == nil {
		t.Errorf("the reference TTFT is bootstrapped from the records: %+v", ttft)
	}
	stdout := run("compare", "--bundles", bundles, "--objective", "slo_max_rps")
	if stdout.code != ExitOK || !bytes.Equal([]byte(stdout.stdout), written) {
		t.Errorf("standard output must equal --out, and repeat byte for byte")
	}
}

func TestCompareExitCodes(t *testing.T) {
	analysed := analysedBundle(t, "01JBX0000000000000000000A1", 100)
	raw := t.TempDir()
	if err := analyzetest.ScenarioNamed("clean-ladder").WriteBundle(raw); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		args []string
		want int
	}{
		"no bundles":          {[]string{"compare"}, ExitInvalidSpec},
		"unknown objective":   {[]string{"compare", "--bundles", analysed, "--objective", "fastest"}, ExitInvalidSpec},
		"not analysed":        {[]string{"compare", "--bundles", raw}, ExitInvalidSpec},
		"the same run twice":  {[]string{"compare", "--bundles", analysed + "," + analysed}, ExitInvalidSpec},
		"missing bundle":      {[]string{"compare", "--bundles", filepath.Join(raw, "nope")}, ExitRunFailed},
		"one bundle compares": {[]string{"compare", "--bundles", analysed}, ExitOK},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := run(c.args...); got.code != c.want {
				t.Errorf("exit %d, want %d: %s", got.code, c.want, got.stderr)
			}
		})
	}
}
