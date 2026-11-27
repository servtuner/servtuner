package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const proposeTestdata = "../../testdata/propose"

func proposeBundles(names ...string) string {
	dirs := make([]string, len(names))
	for i, name := range names {
		dirs[i] = filepath.Join(proposeTestdata, "runs", name)
	}
	return strings.Join(dirs, ",")
}

func TestProposeIsByteIdenticalForTheSameInputs(t *testing.T) {
	space := filepath.Join(proposeTestdata, "space.yaml")
	bundles := proposeBundles("seqs64-probe", "seqs128", "seqs256")
	first, second := filepath.Join(t.TempDir(), "first.json"), filepath.Join(t.TempDir(), "second.json")
	for _, out := range []string{first, second} {
		if result := run("propose", "--space", space, "--bundles", bundles, "--out", out); result.code != ExitOK {
			t.Fatalf("exit %d: %s", result.code, result.stderr)
		}
	}
	one, _ := os.ReadFile(first)
	two, _ := os.ReadFile(second)
	if len(one) == 0 || !bytes.Equal(one, two) {
		t.Fatal("two runs on the same inputs differ")
	}
	var decoded struct {
		Status    string
		Proposals []struct{ Action string }
	}
	if err := json.Unmarshal(one, &decoded); err != nil || decoded.Status != "propose" || len(decoded.Proposals) != 1 { // one coordinate step
		t.Fatalf("%v: %s", err, one)
	}
	// Standard output carries the same bytes as --out.
	if result := run("propose", "--space", space, "--bundles", bundles); result.code != ExitOK || result.stdout != string(one) {
		t.Fatalf("exit %d, stdout differs from --out", result.code)
	}
}

func TestProposeReadsBootResults(t *testing.T) {
	result := run("propose", "--space", filepath.Join(proposeTestdata, "space-feasibility.yaml"), "--infeasible", filepath.Join(proposeTestdata, "boots.yaml"))
	if result.code != ExitOK || !strings.Contains(result.stdout, `"phase": "baseline"`) || strings.Contains(result.stdout, "16384") {
		t.Fatalf("exit %d: %s%s", result.code, result.stdout, result.stderr)
	}
}

func TestProposeRejectsBadInputs(t *testing.T) {
	space := filepath.Join(proposeTestdata, "space.yaml")
	bad := filepath.Join(t.TempDir(), "space.yaml")
	if err := os.WriteFile(bad, []byte("knobs: [{name: nope, tier: scheduler, values: [1]}]"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"no --space":      {"propose"},
		"invalid space":   {"propose", "--space", bad},
		"missing space":   {"propose", "--space", filepath.Join(t.TempDir(), "absent.yaml")},
		"missing boots":   {"propose", "--space", space, "--infeasible", filepath.Join(t.TempDir(), "absent.yaml")},
		"not a bundle":    {"propose", "--space", space, "--bundles", t.TempDir()},
		"missing bundle ": {"propose", "--space", space, "--bundles", filepath.Join(t.TempDir(), "absent")},
	}
	for name, args := range cases {
		if result := run(args...); result.code == ExitOK {
			t.Errorf("%s: exit 0", name)
		}
	}
}

func TestProposeRefusesABundleWithoutTheLadderLabel(t *testing.T) {
	source := filepath.Join(proposeTestdata, "runs", "seqs128")
	dir := t.TempDir()
	for _, name := range []string{"manifest.json", "analysis.json", "spec.resolved.json"} {
		content, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "spec.resolved.json" {
			var document map[string]any
			if err := json.Unmarshal(content, &document); err != nil {
				t.Fatal(err)
			}
			delete(document, "labels")
			if content, err = json.Marshal(document); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := run("propose", "--space", filepath.Join(proposeTestdata, "space.yaml"), "--bundles", dir)
	if result.code != ExitInvalidSpec || !strings.Contains(result.stderr, "01M808ZJD5N88S47PGSSC5WK78") {
		t.Fatalf("exit %d: %s", result.code, result.stderr)
	}
}
