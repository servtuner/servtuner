package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestValidateExpandsPresetsAndRejectsBadOnes(t *testing.T) {
	valid := run("validate", "--spec", "../../../schema/examples/runspec/valid/workload-preset.json")
	if valid.code != ExitOK {
		t.Fatalf("a preset RunSpec: exit %d: %s%s", valid.code, valid.stdout, valid.stderr)
	}
	cases := map[string]struct{ engine, workload string }{
		"unknown name":    {"loadgen", `{"preset": "chat-tiny@1", "streaming": true}`},
		"unknown version": {"loadgen", `{"preset": "chat-short@7", "streaming": true}`},
		"engine":          {"inference-perf", `{"preset": "chat-multiturn@1", "streaming": true}`}, // loadgen only
	}
	for name, c := range cases {
		document := `{"apiVersion":"servtuner.dev/v2","kind":"RunSpec","name":"x",
"target":{"type":"vllm","endpoint":"http://vllm.inference.svc:8000/v1","model":"m","tls":{"enabled":false}},
"engine":{"type":"` + c.engine + `"},"workload":` + c.workload + `,"load":{"stages":[{"rate":1,"duration":"90s"}]}}`
		path := filepath.Join(t.TempDir(), "spec.json")
		if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := run("validate", "--spec", path); got.code != ExitInvalidSpec || !strings.Contains(got.stderr, "preset") {
			t.Errorf("%s: exit %d, stderr %q; want exit %d naming the preset", name, got.code, got.stderr, ExitInvalidSpec)
		}
	}
}

// TestPresetsPrintsTheEmbeddedCatalog: servtuner presets prints every embedded
// preset as JSON, the document the api serves at GET /workload-presets.
func TestPresetsPrintsTheEmbeddedCatalog(t *testing.T) {
	got := run("presets")
	if got.code != ExitOK {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	var printed []spec.WorkloadPreset
	if err := json.Unmarshal([]byte(got.stdout), &printed); err != nil {
		t.Fatalf("not a JSON array of presets: %v\n%s", err, got.stdout)
	}
	embedded, err := spec.WorkloadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(printed) != len(embedded) || len(printed) == 0 {
		t.Fatalf("printed %d presets, embedded %d", len(printed), len(embedded))
	}
	for i := range printed {
		if printed[i].Name != embedded[i].Name || printed[i].Version != embedded[i].Version {
			t.Errorf("preset %d: printed %s@%d, embedded %s@%d", i, printed[i].Name, printed[i].Version, embedded[i].Name, embedded[i].Version)
		}
	}
}

// TestSpacesPrintsTheEmbeddedCatalog: servtuner spaces prints every starting search space as JSON,
// the document the api serves at GET /search-spaces.
func TestSpacesPrintsTheEmbeddedCatalog(t *testing.T) {
	got := run("spaces")
	if got.code != ExitOK {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	var printed []spec.SearchSpaceTemplate
	if err := json.Unmarshal([]byte(got.stdout), &printed); err != nil {
		t.Fatalf("not a JSON array of search spaces: %v\n%s", err, got.stdout)
	}
	var names []string
	for _, template := range printed {
		names = append(names, template.Name)
	}
	want := []string{"memory-bound", "no-gpu", "single-gpu", "two-gpu-tensor-parallel"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("printed %v, want %v (sorted by name)", names, want)
	}
}

// TestKnobsPrintsTheCatalog: servtuner knobs prints the knob catalog the api serves at GET /knobs,
// valid against its schema.
func TestKnobsPrintsTheCatalog(t *testing.T) {
	got := run("knobs")
	if got.code != ExitOK {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	if err := spec.Validate(spec.SchemaKnobCatalog, []byte(got.stdout)); err != nil {
		t.Fatalf("breaks its schema: %v", err)
	}
	var printed spec.KnobCatalog
	if err := json.Unmarshal([]byte(got.stdout), &printed); err != nil {
		t.Fatal(err)
	}
	if printed.VllmVersion == "" || len(printed.Knobs) < 250 {
		t.Errorf("vLLM %q, %d knobs: want every single-value flag of the pinned version", printed.VllmVersion, len(printed.Knobs))
	}
}
