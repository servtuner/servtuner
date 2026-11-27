package spec_test

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/workload"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/presets")

const presetGoldenDir = "../../testdata/presets"

// presetRunSpec is a RunSpec whose workload block is the argument.
func presetRunSpec(engineType, workloadBlock string) []byte {
	return []byte(`{
  "apiVersion": "servtuner.dev/v1", "kind": "RunSpec", "name": "preset-test",
  "target": {"type": "vllm", "endpoint": "http://vllm.inference.svc:8000/v1", "model": "example/model-8b", "tls": {"mode": "platform"}},
  "engine": {"type": "` + engineType + `"},
  "workload": ` + workloadBlock + `,
  "load": {"stages": [{"rate": 1, "duration": "90s"}]}
}`)
}

func TestEmbeddedPresetsAreTheCatalog(t *testing.T) {
	presets, err := spec.WorkloadPresets()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, preset := range presets {
		got = append(got, spec.FormatPresetReference(preset.Name, preset.Version))
	}
	want := "agent-prefix@1 chat-long@1 chat-multiturn@1 chat-short@1 decode-long@1 rag-prefill@1 summarize@1"
	if strings.Join(got, " ") != want {
		t.Errorf("presets = %v, want %s", got, want)
	}
}

func TestExpandPresetIsByteStable(t *testing.T) {
	for _, reference := range []string{"chat-short@1", "chat-multiturn@1", "decode-long@1", "summarize@1", "rag-prefill@1", "agent-prefix@1", "chat-long@1"} {
		t.Run(reference, func(t *testing.T) {
			runSpec, _, err := spec.LoadRunSpec(presetRunSpec("loadgen", `{"preset": "`+reference+`", "streaming": true, "seed": 3}`))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(map[string]any{"workload": runSpec.Workload, "workload_preset": runSpec.WorkloadPreset}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join(presetGoldenDir, strings.Replace(reference, "@", ".v", 1)+".expanded.json")
			if *updateGolden {
				if err := os.MkdirAll(presetGoldenDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(append(got, '\n')) != string(want) {
				t.Errorf("expansion changed (rerun with -update if intended):\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestExpandedPresetHashEqualsHandWrittenWorkload(t *testing.T) {
	preset, _, err := spec.LoadRunSpec(presetRunSpec("loadgen",
		`{"preset": "chat-multiturn@1", "streaming": true}`))
	if err != nil {
		t.Fatal(err)
	}
	handWritten, _, err := spec.LoadRunSpec(presetRunSpec("loadgen", `{
    "streaming": true, "api": "chat",
    "dataset": {"type": "synthetic", "input_tokens": {"mean": 256}, "output_tokens": {"mean": 256}},
    "conversation": {"turns": 6}
  }`))
	if err != nil {
		t.Fatal(err)
	}
	presetHash, err := analyze.WorkloadHash(preset.Workload)
	if err != nil {
		t.Fatal(err)
	}
	handHash, err := analyze.WorkloadHash(handWritten.Workload)
	if err != nil {
		t.Fatal(err)
	}
	if presetHash != handHash {
		t.Errorf("analyze.WorkloadHash differs: preset %s, hand-written %s", presetHash, handHash)
	}
	presetGenerated, err := workload.Hash(preset.Workload, workload.Options{})
	if err != nil {
		t.Fatal(err)
	}
	handGenerated, err := workload.Hash(handWritten.Workload, workload.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if presetGenerated != handGenerated {
		t.Errorf("workload.Hash differs: preset %s, hand-written %s", presetGenerated, handGenerated)
	}
	if preset.WorkloadPreset == nil || preset.WorkloadPreset.Name != "chat-multiturn" || preset.WorkloadPreset.Version != 1 {
		t.Errorf("workload_preset = %+v", preset.WorkloadPreset)
	}
}

func TestExpandPresetRejects(t *testing.T) {
	cases := map[string]struct {
		engine, workload string
		want             error
		message          string
	}{
		"unknown name":    {"loadgen", `{"preset": "chat-tiny@1", "streaming": true}`, spec.ErrUnknownPreset, "available: agent-prefix, chat-long, chat-multiturn"},
		"unknown version": {"loadgen", `{"preset": "chat-short@9", "streaming": true}`, spec.ErrUnknownPreset, "no version 9 (available: 1)"},
		"api":             {"loadgen", `{"preset": "chat-short@1", "streaming": true, "api": "completion"}`, nil, "conflicts with preset chat-short@1"},
		"engine":          {"inference-perf", `{"preset": "chat-multiturn@1", "streaming": true}`, spec.ErrPresetEngine, "runs on [loadgen]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := spec.LoadRunSpec(presetRunSpec(c.engine, c.workload))
			if err == nil || (c.want != nil && !errors.Is(err, c.want)) || !strings.Contains(err.Error(), c.message) {
				t.Errorf("err = %v, want %v containing %q", err, c.want, c.message)
			}
		})
	}
}

func TestResolvedDocumentReloadsUnchanged(t *testing.T) {
	first, _, err := spec.LoadRunSpec(presetRunSpec("loadgen", `{"preset": "decode-long@1", "streaming": true}`))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := spec.LoadRunSpec(resolved)
	if err != nil {
		t.Fatalf("a resolved document must load again: %v", err)
	}
	if again.WorkloadPreset == nil || again.Workload.Preset != nil || again.Workload.Dataset == nil {
		t.Errorf("reloaded workload = %+v, preset %+v", again.Workload, again.WorkloadPreset)
	}
}
