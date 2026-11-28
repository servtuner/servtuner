package workload

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Request fingerprints tie the requests the generator sends to the workload
// hash. RPS is compared only between runs with equal hashes, so a change to
// the requests must change the hash (bump GeneratorVersion, or mark the
// feature's canonical block as sharedPrefixSizing does), and a hash change
// must come with a change to the requests. The golden file records both for
// every embedded preset and for the options the presets do not cover.
//
//	go test ./internal/workload -run TestRequestFingerprints -update
//
// rewrites it, except for a case whose requests changed while its hash did
// not: that is the mistake this test exists to catch.
var updateFingerprints = flag.Bool("update", false, "rewrite bench/testdata/workload/fingerprints.json")

const fingerprintsFile = "../../testdata/workload/fingerprints.json"

const (
	// fingerprintWarmup requests are sized at the default ratio, as a run's
	// warmup is before any usage comes back; then one observation moves the
	// ratio and fingerprintStage requests follow at the calibrated ratio. The
	// move matters: the 0.1.0 shared-prefix sizing only differed from the
	// fixed one once the ratio changed mid-run.
	fingerprintWarmup = 8
	fingerprintStage  = 24
	// fingerprintRatio is the calibrated chars-per-token ratio (3,200 chars
	// over 1,000 tokens), away from DefaultCharsPerToken on purpose.
	fingerprintChars, fingerprintTokens = 3200, 1000
	fingerprintModel                    = "fingerprint-model"
)

type fingerprintCase struct {
	workload spec.Workload
	options  Options
}

type fingerprintEntry struct {
	Hash        string `json:"hash"`
	Fingerprint string `json:"fingerprint"`
}

// fingerprintCases is every embedded preset, expanded as a RunSpec expands it,
// plus single-feature variants of a plain workload for what no preset uses.
func fingerprintCases(t *testing.T) map[string]fingerprintCase {
	t.Helper()
	presets, err := spec.WorkloadPresets()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]fingerprintCase{}
	for _, preset := range presets {
		reference := spec.FormatPresetReference(preset.Name, preset.Version)
		runSpec, _, err := spec.LoadRunSpec([]byte(`{
  "apiVersion": "servtuner.dev/v2", "kind": "RunSpec", "name": "fingerprint",
  "target": {"type": "vllm", "endpoint": "http://vllm.inference.svc:8000/v1", "model": "example/model-8b", "tls": {"enabled": false}},
  "engine": {"type": "` + string(preset.Engines[0]) + `"},
  "workload": {"preset": "` + reference + `", "streaming": true, "seed": 42},
  "load": {"stages": [{"rate": 1, "duration": "90s"}]}
}`))
		if err != nil {
			t.Fatalf("expand %s: %v", reference, err)
		}
		cases["preset/"+reference] = fingerprintCase{workload: runSpec.Workload}
	}

	plain := func(api spec.WorkloadApi, streaming bool) spec.Workload {
		return spec.Workload{
			Api: api, Streaming: streaming, Seed: 42,
			Dataset: &spec.Dataset{
				Type:         spec.DatasetTypeSynthetic,
				InputTokens:  spec.TokenDistribution{Mean: 512, Stddev: 64},
				OutputTokens: spec.TokenDistribution{Mean: 128, Stddev: 16},
			},
		}
	}
	padded := plain(spec.WorkloadApiChat, true)
	padded.Dataset.PaddingBytes = 2048
	cases["chat"] = fingerprintCase{workload: plain(spec.WorkloadApiChat, true)}
	cases["chat/not-streaming"] = fingerprintCase{workload: plain(spec.WorkloadApiChat, false)}
	cases["chat/set-min-tokens"] = fingerprintCase{workload: plain(spec.WorkloadApiChat, true), options: Options{SetMinTokens: true}}
	cases["chat/padding"] = fingerprintCase{workload: padded}
	cases["completion"] = fingerprintCase{workload: plain(spec.WorkloadApiCompletion, true)}
	cases["embeddings"] = fingerprintCase{workload: plain(spec.WorkloadApiEmbeddings, false)}
	return cases
}

// requestFingerprint generates the case's requests the way a run does and
// hashes everything that reaches the server or the analysis.
func requestFingerprint(t *testing.T, c fingerprintCase) fingerprintEntry {
	t.Helper()
	calibrator := NewCalibrator(0)
	generator, err := New(c.workload, fingerprintModel, calibrator, c.options)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	field := func(value []byte) {
		_ = binary.Write(digest, binary.BigEndian, uint64(len(value)))
		digest.Write(value)
	}
	for i := range fingerprintWarmup + fingerprintStage {
		if i == fingerprintWarmup {
			calibrator.Observe(fingerprintModel, fingerprintChars, fingerprintTokens)
		}
		request := generator.Next()
		field([]byte(request.Path))
		field(fmt.Appendf(nil, "%t %d %d %d", request.Streaming, request.PromptChars, request.ExpectedInputTokens, request.ExpectedOutputTokens))
		field(request.Body)
	}
	return fingerprintEntry{Hash: generator.Info().Hash, Fingerprint: hex.EncodeToString(digest.Sum(nil))}
}

func readFingerprints(t *testing.T) map[string]fingerprintEntry {
	t.Helper()
	data, err := os.ReadFile(fingerprintsFile)
	if os.IsNotExist(err) && *updateFingerprints {
		return map[string]fingerprintEntry{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]fingerprintEntry
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatalf("%s: %v", fingerprintsFile, err)
	}
	return recorded
}

// fingerprintProblem says what is wrong with got against the recorded entry,
// or "" when they agree. isUpdatable is false for the one problem -update
// must not paper over: the requests changed but the hash did not.
func fingerprintProblem(recorded fingerprintEntry, isRecorded bool, got fingerprintEntry) (problem string, isUpdatable bool) {
	switch {
	case !isRecorded:
		return "new case, not in the golden file", true
	case got == recorded:
		return "", true
	case got.Hash == recorded.Hash:
		return "the generated requests changed but the workload hash did not, so runs from before and after " +
			"would be compared as the same workload: bump GeneratorVersion (hash.go), or mark the changed " +
			"feature's canonical block as sharedPrefixSizing does, then rerun with -update", false
	case got.Fingerprint == recorded.Fingerprint:
		return "the workload hash changed but the generated requests did not, which splits comparable runs: " +
			"keep the hash, or rerun with -update if the split is intended", true
	default:
		return "the requests and the hash both changed: rerun with -update to accept", true
	}
}

func TestRequestFingerprints(t *testing.T) {
	recorded := readFingerprints(t)
	cases := fingerprintCases(t)
	updated := map[string]fingerprintEntry{}
	refused := false
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range cases {
			if !yield(name) {
				return
			}
		}
	}) {
		got := requestFingerprint(t, cases[name])
		entry, isRecorded := recorded[name]
		problem, isUpdatable := fingerprintProblem(entry, isRecorded, got)
		switch {
		case problem == "":
			updated[name] = got
		case *updateFingerprints && isUpdatable:
			updated[name] = got
		default:
			t.Errorf("%s: %s\n  recorded %+v\n  got      %+v", name, problem, entry, got)
			refused = refused || !isUpdatable
			if isRecorded {
				updated[name] = entry
			}
		}
	}
	var stale []string
	for name := range recorded {
		if _, ok := cases[name]; !ok {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 && !*updateFingerprints {
		slices.Sort(stale)
		t.Errorf("cases in %s that no longer exist (rerun with -update): %s", fingerprintsFile, strings.Join(stale, ", "))
	}
	if !*updateFingerprints {
		return
	}
	if refused {
		t.Fatalf("%s not rewritten: fix the cases above first", fingerprintsFile)
	}
	data, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fingerprintsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fingerprintsFile, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The rule itself, without the golden file: every outcome of a comparison.
func TestFingerprintProblem(t *testing.T) {
	recorded := fingerprintEntry{Hash: "h1", Fingerprint: "f1"}
	for _, tc := range []struct {
		name        string
		got         fingerprintEntry
		isRecorded  bool
		wantProblem string
		wantUpdate  bool
	}{
		{"unchanged", recorded, true, "", true},
		{"requests only", fingerprintEntry{Hash: "h1", Fingerprint: "f2"}, true, "requests changed but the workload hash did not", false},
		{"hash only", fingerprintEntry{Hash: "h2", Fingerprint: "f1"}, true, "hash changed but the generated requests did not", true},
		{"both", fingerprintEntry{Hash: "h2", Fingerprint: "f2"}, true, "both changed", true},
		{"new", recorded, false, "new case", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problem, isUpdatable := fingerprintProblem(recorded, tc.isRecorded, tc.got)
			if !strings.Contains(problem, tc.wantProblem) || (tc.wantProblem == "" && problem != "") {
				t.Errorf("problem = %q, want it to contain %q", problem, tc.wantProblem)
			}
			if isUpdatable != tc.wantUpdate {
				t.Errorf("isUpdatable = %t, want %t", isUpdatable, tc.wantUpdate)
			}
		})
	}
}
