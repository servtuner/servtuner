package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeKnobs(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knobs.json")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestVLLMArgsSpellsKnobsAsFlags: the deployer's args come from the knob
// table, as a JSON array; an unknown knob is an invalid input (exit 2).
func TestVLLMArgsSpellsKnobsAsFlags(t *testing.T) {
	got := run("vllm-args", "--knobs", writeKnobs(t, `{"vllm.max_num_seqs": 256, "vllm.enable_prefix_caching": false}`))
	if got.code != ExitOK {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	var args []string
	if err := json.Unmarshal([]byte(got.stdout), &args); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, got.stdout)
	}
	if !slices.Equal(args, []string{"--max-num-seqs=256", "--disable-prefix-caching"}) {
		t.Errorf("args = %q", args)
	}
	if empty := run("vllm-args", "--knobs", writeKnobs(t, `{}`)); strings.TrimSpace(empty.stdout) != "[]" {
		t.Errorf("no knobs print [], got %q", empty.stdout)
	}
	if bad := run("vllm-args", "--knobs", writeKnobs(t, `{"vllm.nope": 1}`)); bad.code != ExitInvalidSpec || !strings.Contains(bad.stderr, "vllm.nope") {
		t.Errorf("unknown knob: exit %d, stderr %q", bad.code, bad.stderr)
	}
}
