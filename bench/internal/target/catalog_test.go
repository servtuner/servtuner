package target

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// pinnedVLLMCommit is the vLLM commit the knob table was checked against;
// the catalog must be read from the same one.
const pinnedVLLMCommit = "62d4bc3c7e33b1b6c906e233e6af2d3506c482ca"

// catalogKinds are the catalog kinds a knob table kind may stand for.
var catalogKinds = map[knobKind][]spec.KnobKind{
	knobNumber:  {spec.KnobKindInteger, spec.KnobKindNumber},
	knobInteger: {spec.KnobKindInteger},
	knobSize:    {spec.KnobKindSize},
	knobBoolean: {spec.KnobKindBoolean},
	knobSwitch:  {spec.KnobKindSwitch},
	knobText:    {spec.KnobKindText, spec.KnobKindChoice},
}

// kindExceptions: vLLM parses --optimization-level as text (it also accepts "O2"), while
// servtuner captures the level as a number.
var kindExceptions = map[string]bool{KnobVLLMOptimizationLevel: true}

// rewrittenFlags are not argparse options: vLLM's parser rewrites -O3, -O=3 and -O 3 into
// --optimization-level before parsing (argparse_utils.py), so the catalog has the long flag only.
var rewrittenFlags = map[string]bool{"-O": true}

func TestEveryBuiltInKnobIsInVLLMsCatalog(t *testing.T) {
	file, err := readVLLMCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if file.VLLMCommit != pinnedVLLMCommit {
		t.Fatalf("catalog read from vLLM %s, the knob table is pinned to %s: regenerate it", file.VLLMCommit, pinnedVLLMCommit)
	}
	byFlag := map[string]vllmCatalogFlag{}
	for _, flag := range file.Flags {
		for _, name := range append([]string{flag.Flag}, flag.Aliases...) {
			byFlag[name] = flag
		}
	}
	for _, row := range vllmFlags {
		for _, name := range row.flags {
			if rewrittenFlags[name] {
				continue
			}
			entry, found := byFlag[name]
			if !found {
				t.Errorf("%s: vLLM %s has no flag %s", row.knob, file.VLLMVersion, name)
				continue
			}
			if "vllm."+entry.Dest != row.knob {
				t.Errorf("%s: flag %s sets vLLM's %s", row.knob, name, entry.Dest)
			}
			if !kindExceptions[row.knob] && !slices.Contains(catalogKinds[row.kind], entry.Kind) {
				t.Errorf("%s: vLLM parses %s as %s, the knob table says %v", row.knob, name, entry.Kind, catalogKinds[row.kind])
			}
		}
	}
	for _, row := range builtInVLLMFlags {
		if _, labelled := knobLabels[row.knob]; !labelled {
			t.Errorf("%s has no label", row.knob)
		}
	}
}

// Every single-value catalog flag is a knob, and servtuner's own and multi-value flags are not.
func TestEveryCatalogFlagIsAKnobUnlessServtunerSetsIt(t *testing.T) {
	file, err := readVLLMCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range file.Flags {
		name := vllmPrefix + flag.Dest
		owned := servtunerOwnedFields[flag.Dest]
		multi := flag.Kind == spec.KnobKindJson || flag.Kind == spec.KnobKindList
		if want := !owned && !multi; IsVLLMKnob(name) != want {
			t.Errorf("%s (%s): IsVLLMKnob = %v, want %v", name, flag.Kind, !want, want)
		}
		// A boolean renders its false through falseFlag: vLLM must accept that spelling.
		if flag.Kind == spec.KnobKindBoolean && IsVLLMKnob(name) && !slices.Contains(flag.NegativeFlags, falseFlag(flag.Flag)) {
			t.Errorf("%s: renders false as %s, vLLM takes %v", name, falseFlag(flag.Flag), flag.NegativeFlags)
		}
	}
	for _, name := range []string{"vllm.model", "vllm.port", "vllm.ssl_certfile", "vllm.compilation_config", "vllm.api_key"} {
		if IsVLLMKnob(name) {
			t.Errorf("%s is a knob: servtuner sets it, or it takes more than one value", name)
		}
	}
	if !IsVLLMKnob("vllm.max_num_scheduled_tokens") || !IsVLLMKnob("vllm.trust_remote_code") {
		t.Error("a catalog flag outside the built-in table is not a knob")
	}
}

func TestTheKnobCatalogDescribesEveryKnobOnce(t *testing.T) {
	catalog, err := KnobCatalog()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaKnobCatalog, encoded); err != nil {
		t.Fatalf("servtuner knobs output breaks its schema: %v", err)
	}
	seen := map[string]bool{}
	builtIn := 0
	for _, knob := range catalog.Knobs {
		if seen[knob.Name] {
			t.Errorf("%s is listed twice", knob.Name)
		}
		seen[knob.Name] = true
		if strings.HasPrefix(knob.Label, "-") || strings.Contains(knob.Label, "_") {
			t.Errorf("%s: label %q looks like a flag or a field", knob.Name, knob.Label)
		}
		if !IsVLLMKnob(knob.Name) {
			t.Errorf("%s is listed but bench cannot render it", knob.Name)
		}
		if knob.BuiltIn {
			builtIn++
		}
	}
	if want := len(builtInVLLMFlags) + 1; builtIn != want { // + cudagraph_mode, set through -cc only
		t.Errorf("%d built-in knobs, want %d", builtIn, want)
	}
	byName := map[string]spec.KnobDescription{}
	for _, knob := range catalog.Knobs {
		byName[knob.Name] = knob
	}
	gpu := byName[KnobVLLMGPUMemoryUtil]
	if gpu.Label != "GPU memory utilization" || gpu.Limits == nil || gpu.Limits.Le == nil || *gpu.Limits.Le != 1 {
		t.Errorf("gpu_memory_utilization = %+v, want its label and vLLM's le=1", gpu)
	}
	if !strings.HasPrefix(byName[KnobVLLMMaxNumSeqs].Help, "Maximum number of sequences") {
		t.Errorf("max_num_seqs help = %q, want vLLM's own text", byName[KnobVLLMMaxNumSeqs].Help)
	}
	if other := byName["vllm.max_num_scheduled_tokens"]; other.BuiltIn || other.Label != "Max number of scheduled tokens" {
		t.Errorf("a flag outside the knob table = %+v", other)
	}
}

// The tuning rules come from the catalog, so a knob made tunable is checked like a built-in one.
func TestTuningRulesFollowTheCatalog(t *testing.T) {
	for name, whole := range map[string]bool{
		KnobVLLMMaxNumSeqs: true, KnobVLLMMaxModelLen: true, "vllm.max_num_scheduled_tokens": true,
		KnobVLLMGPUMemoryUtil: false, KnobVLLMDtype: false,
	} {
		if IsWholeNumberKnob(name) != whole {
			t.Errorf("IsWholeNumberKnob(%s) = %v", name, !whole)
		}
	}
	catalog, err := KnobCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var ranged []string
	for _, knob := range catalog.Knobs {
		if knob.TunedAsRange {
			ranged = append(ranged, knob.Name)
		}
	}
	slices.Sort(ranged)
	if want := []string{KnobVLLMMaxModelLen, KnobVLLMMaxNumBatchedTokens, KnobVLLMMaxNumSeqs}; !slices.Equal(ranged, want) {
		t.Errorf("tuned as a range: %v, want %v", ranged, want)
	}
}

func TestTheCatalogListsTheFlagsThatAreNotKnobs(t *testing.T) {
	catalog, err := KnobCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--port", "--compilation-config", "-cc", "--served-model-name"} {
		if !slices.Contains(catalog.OtherFlags, flag) {
			t.Errorf("other_flags lacks %s", flag)
		}
	}
	if slices.Contains(catalog.OtherFlags, "--max-num-seqs") {
		t.Error("a knob's flag is listed among the other flags")
	}
}
