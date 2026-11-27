package workload

import (
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func sharedPrefixWorkload(tokens, groups int) spec.Workload {
	workload := testWorkload(spec.WorkloadApiCompletion, false)
	workload.Dataset.SharedPrefix = &spec.SharedPrefix{Tokens: tokens, Groups: groups}
	return workload
}

// prefixOf returns the part of a prompt before the separator that follows the
// shared prefix, after the run's salt.
func prefixOf(t *testing.T, generator *Generator, prompt string) string {
	t.Helper()
	body := strings.TrimPrefix(prompt, generator.saltPrefix)
	for group := range generator.prefixes.groups {
		text := generator.prefixes.groups[group].text
		if text != "" && strings.HasPrefix(body, text+" ") {
			return text
		}
	}
	t.Fatalf("prompt starts with no group's prefix: %.60q", body)
	return ""
}

// TestSharedPrefixGroupsRepeatByteForByte: requests share one of `groups`
// prefixes exactly, every group is used, and the whole prompt keeps the
// input_tokens target (the prefix is inside it, not added on top).
func TestSharedPrefixGroupsRepeatByteForByte(t *testing.T) {
	generator := newTestGenerator(t, sharedPrefixWorkload(512, 4), nil, Options{CacheSalt: "run-a"})
	plain := newTestGenerator(t, testWorkload(spec.WorkloadApiCompletion, false), nil, Options{CacheSalt: "run-a"})
	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		request := generator.Next()
		prompt := decodeBody(t, request)["prompt"].(string)
		if !strings.HasPrefix(prompt, generator.saltPrefix) {
			t.Fatalf("request %d: the run's salt must stay first", i)
		}
		seen[prefixOf(t, generator, prompt)]++
		if want := plain.Next().ExpectedInputTokens; request.ExpectedInputTokens != want {
			t.Fatalf("request %d: input tokens %d, want the same target as without a prefix (%d)", i, request.ExpectedInputTokens, want)
		}
		if len(prompt) != request.PromptChars {
			t.Fatalf("request %d: PromptChars %d, prompt %d", i, request.PromptChars, len(prompt))
		}
	}
	if len(seen) != 4 {
		t.Errorf("used %d prefixes, want all 4 groups", len(seen))
	}
	for prefix := range seen {
		// About 512 tokens at the default ratio.
		if tokens := float64(len(prefix)) / generator.calibrator.CharsPerToken("test-model"); tokens < 500 || tokens > 524 {
			t.Errorf("prefix of %.0f tokens, want about 512", tokens)
		}
	}
}

// TestSharedPrefixIsStableAfterCalibration: once a group's prefix exists, a
// new chars-per-token ratio does not change its bytes.
func TestSharedPrefixIsStableAfterCalibration(t *testing.T) {
	calibrator := NewCalibrator(0)
	generator := newTestGenerator(t, sharedPrefixWorkload(256, 1), calibrator, Options{})
	before := prefixOf(t, generator, decodeBody(t, generator.Next())["prompt"].(string))
	for i := 0; i < 100; i++ {
		calibrator.Observe("test-model", 10000, 1000) // 10 chars per token
	}
	after := prefixOf(t, generator, decodeBody(t, generator.Next())["prompt"].(string))
	if before != after {
		t.Error("the prefix changed after calibration")
	}
}

func TestSharedPrefixRunsDiffer(t *testing.T) {
	a := newTestGenerator(t, sharedPrefixWorkload(256, 1), nil, Options{CacheSalt: "run-a"})
	b := newTestGenerator(t, sharedPrefixWorkload(256, 1), nil, Options{CacheSalt: "run-b"})
	promptA := decodeBody(t, a.Next())["prompt"].(string)
	promptB := decodeBody(t, b.Next())["prompt"].(string)
	if promptA[:20] == promptB[:20] {
		t.Error("two runs must not share a prefix: the salt comes first")
	}
}

func TestSharedPrefixHashKeepsOtherHashes(t *testing.T) {
	plain := newTestGenerator(t, testWorkload(spec.WorkloadApiChat, true), nil, Options{})
	if plain.Info().Hash != knownHash {
		t.Fatalf("a workload without a shared prefix changed its hash")
	}
	withPrefix := testWorkload(spec.WorkloadApiChat, true)
	withPrefix.Dataset.SharedPrefix = &spec.SharedPrefix{Tokens: 256, Groups: 2}
	moreGroups := testWorkload(spec.WorkloadApiChat, true)
	moreGroups.Dataset.SharedPrefix = &spec.SharedPrefix{Tokens: 256, Groups: 3}
	hashA := newTestGenerator(t, withPrefix, nil, Options{}).Info().Hash
	hashB := newTestGenerator(t, moreGroups, nil, Options{}).Info().Hash
	if hashA == knownHash || hashA == hashB {
		t.Error("the shared prefix must be part of the hash")
	}
}

func TestSharedPrefixRejections(t *testing.T) {
	tooLong := sharedPrefixWorkload(1024, 2) // input mean is 1024
	conversation := sharedPrefixWorkload(256, 2)
	conversation.Api = spec.WorkloadApiChat
	conversation.Conversation = &spec.Conversation{Turns: 2}
	embeddings := sharedPrefixWorkload(256, 2)
	embeddings.Api = spec.WorkloadApiEmbeddings
	noDataset := testWorkload(spec.WorkloadApiChat, true)
	noDataset.Dataset = nil
	for name, workload := range map[string]spec.Workload{"prefix as long as the prompt": tooLong, "conversation": conversation, "embeddings": embeddings, "no dataset": noDataset} {
		if _, err := New(workload, "m", nil, Options{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// TestEveryPresetBuildsAGenerator: every preset that lists loadgen gives a
// workload this generator accepts.
func TestEveryPresetBuildsAGenerator(t *testing.T) {
	presets, err := spec.WorkloadPresets()
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range presets {
		if !slices.Contains(preset.Engines, spec.EngineTypeLoadgen) {
			continue
		}
		t.Run(preset.Name, func(t *testing.T) {
			reference := spec.PresetReference(spec.FormatPresetReference(preset.Name, preset.Version))
			runSpec := spec.RunSpec{Engine: spec.Engine{Type: spec.EngineTypeLoadgen}, Workload: spec.Workload{Preset: &reference, Streaming: true}}
			expanded, err := spec.ExpandPreset(runSpec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := New(expanded.Workload, "m", nil, Options{}); err != nil {
				t.Errorf("preset lists loadgen but the generator refuses it: %v", err)
			}
		})
	}
}
