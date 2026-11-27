package workload

import (
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

func testWorkload(api spec.WorkloadApi, streaming bool) spec.Workload {
	return spec.Workload{
		Api:       api,
		Streaming: streaming,
		Seed:      42,
		Dataset: &spec.Dataset{
			Type:         spec.DatasetTypeSynthetic,
			InputTokens:  spec.TokenDistribution{Mean: 1024, Stddev: 128},
			OutputTokens: spec.TokenDistribution{Mean: 256, Stddev: 32},
		},
	}
}

func newTestGenerator(t *testing.T, workload spec.Workload, calibrator *Calibrator, options Options) *Generator {
	t.Helper()
	generator, err := New(workload, "test-model", calibrator, options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return generator
}

func decodeBody(t *testing.T, request Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("decode body %s: %v", request.Body, err)
	}
	return body
}

func TestDeterministicSequence(t *testing.T) {
	first := newTestGenerator(t, testWorkload(spec.WorkloadApiChat, true), nil, Options{})
	second := newTestGenerator(t, testWorkload(spec.WorkloadApiChat, true), nil, Options{})
	for i := 0; i < 50; i++ {
		a, b := first.Next(), second.Next()
		if string(a.Body) != string(b.Body) || a.ExpectedInputTokens != b.ExpectedInputTokens || a.ExpectedOutputTokens != b.ExpectedOutputTokens {
			t.Fatalf("request %d differs between identical generators", i)
		}
	}
	if first.Info().Hash != second.Info().Hash {
		t.Error("hash differs between identical workloads")
	}

	otherSeed := testWorkload(spec.WorkloadApiChat, true)
	otherSeed.Seed = 43
	other := newTestGenerator(t, otherSeed, nil, Options{})
	fresh := newTestGenerator(t, testWorkload(spec.WorkloadApiChat, true), nil, Options{})
	if string(other.Next().Body) == string(fresh.Next().Body) {
		t.Error("different seeds gave the same first request")
	}
	if other.Info().Hash == fresh.Info().Hash {
		t.Error("different seeds gave the same hash")
	}
}

func TestConcurrentNextKeepsSequence(t *testing.T) {
	sequential := newTestGenerator(t, testWorkload(spec.WorkloadApiCompletion, false), nil, Options{})
	want := map[int]string{}
	for i := 0; i < 200; i++ {
		request := sequential.Next()
		want[request.Index] = string(request.Body)
	}

	concurrent := newTestGenerator(t, testWorkload(spec.WorkloadApiCompletion, false), nil, Options{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[int]string{}
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				request := concurrent.Next()
				mu.Lock()
				got[request.Index] = string(request.Body)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for index, body := range want {
		if got[index] != body {
			t.Fatalf("request %d differs under concurrent Next", index)
		}
	}
}

func TestHashCoversDefinitionAndGenerator(t *testing.T) {
	base := testWorkload(spec.WorkloadApiChat, true)
	baseHash, err := Hash(base, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseHash) != 64 {
		t.Errorf("hash %q: want 64 hex characters", baseHash)
	}
	changes := map[string]func(*spec.Workload){
		"api":       func(w *spec.Workload) { w.Api = spec.WorkloadApiCompletion },
		"streaming": func(w *spec.Workload) { w.Streaming = false },
		"input":     func(w *spec.Workload) { w.Dataset.InputTokens.Mean++ },
		"output":    func(w *spec.Workload) { w.Dataset.OutputTokens.Stddev++ },
		"seed":      func(w *spec.Workload) { w.Seed++ },
		"padding":   func(w *spec.Workload) { w.Dataset.PaddingBytes = 1048576 }, // 1 MiB
	}
	for name, change := range changes {
		changed := cloneWorkload(base)
		change(&changed)
		hash, err := Hash(changed, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if hash == baseHash {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
	defaulted := cloneWorkload(base)
	defaulted.Api = ""
	if hash, _ := Hash(defaulted, Options{}); hash != baseHash {
		t.Error("an empty api must hash like the chat default")
	}
	if got := newTestGenerator(t, base, nil, Options{}).Info().Generator; got != GeneratorName+"/"+GeneratorVersion {
		t.Errorf("generator = %q", got)
	}
}

func meanAndStddev(values []float64) (mean, stddev float64) {
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	for _, value := range values {
		stddev += (value - mean) * (value - mean)
	}
	return mean, math.Sqrt(stddev / float64(len(values)))
}

func TestTokenTargetsFollowDistributions(t *testing.T) {
	const samples = 5000
	const knownRatio = 3.5
	generator := newTestGenerator(t, testWorkload(spec.WorkloadApiChat, true), NewCalibrator(knownRatio), Options{})
	var inputs, outputs, estimatedInputTokens []float64
	for i := 0; i < samples; i++ {
		request := generator.Next()
		inputs = append(inputs, float64(request.ExpectedInputTokens))
		outputs = append(outputs, float64(request.ExpectedOutputTokens))
		estimatedInputTokens = append(estimatedInputTokens, float64(request.PromptChars)/knownRatio)
		if maxTokens := decodeBody(t, request)["max_tokens"]; int(maxTokens.(float64)) != request.ExpectedOutputTokens {
			t.Fatalf("max_tokens %v != expected output %d", maxTokens, request.ExpectedOutputTokens)
		}
	}
	check := func(name string, values []float64, wantMean, wantStddev float64) {
		mean, stddev := meanAndStddev(values)
		if math.Abs(mean-wantMean) > 0.02*wantMean {
			t.Errorf("%s mean = %.1f, want %.1f within 2%%", name, mean, wantMean)
		}
		if math.Abs(stddev-wantStddev) > 0.1*wantStddev {
			t.Errorf("%s stddev = %.1f, want %.1f within 10%%", name, stddev, wantStddev)
		}
	}
	check("input tokens", inputs, 1024, 128)
	check("output tokens", outputs, 256, 32)
	check("prompt chars / known ratio", estimatedInputTokens, 1024, 128)
}

func TestZeroStddevIsConstant(t *testing.T) {
	workload := testWorkload(spec.WorkloadApiChat, false)
	workload.Dataset.InputTokens.Stddev = 0
	workload.Dataset.OutputTokens.Stddev = 0
	generator := newTestGenerator(t, workload, nil, Options{})
	for i := 0; i < 20; i++ {
		request := generator.Next()
		if request.ExpectedInputTokens != 1024 || request.ExpectedOutputTokens != 256 {
			t.Fatalf("request %d targets = %d/%d, want 1024/256", i, request.ExpectedInputTokens, request.ExpectedOutputTokens)
		}
	}
}

func TestCalibratorDefaultThenConverges(t *testing.T) {
	calibrator := NewCalibrator(0)
	if ratio, isCalibrated := calibrator.Calibrated("m"); ratio != DefaultCharsPerToken || isCalibrated {
		t.Fatalf("before observations: %v, %v", ratio, isCalibrated)
	}
	calibrator.Observe("m", 0, 10)
	calibrator.Observe("m", 10, -1)
	if _, isCalibrated := calibrator.Calibrated("m"); isCalibrated {
		t.Fatal("non-positive observations must be ignored")
	}

	const trueRatio = 3.1
	generator := newTestGenerator(t, testWorkload(spec.WorkloadApiChat, false), calibrator, Options{})
	if generator.Info().CharsPerToken != nil {
		t.Error("an uncalibrated ratio must not be recorded")
	}
	// A server that tokenizes at trueRatio reports prompt_tokens for each prompt.
	for i := 0; i < 20; i++ {
		request := generator.Next()
		calibrator.Observe("test-model", request.PromptChars, int(math.Round(float64(request.PromptChars)/trueRatio)))
	}
	if got := calibrator.CharsPerToken("test-model"); math.Abs(got-trueRatio) > 0.02 {
		t.Fatalf("calibrated ratio = %.3f, want %.1f", got, trueRatio)
	}
	if calibrator.CharsPerToken("other-model") != DefaultCharsPerToken {
		t.Error("calibration must be per model")
	}
	info := generator.Info()
	if info.CharsPerToken == nil || math.Abs(*info.CharsPerToken-trueRatio) > 0.02 {
		t.Fatalf("Info().CharsPerToken = %v", info.CharsPerToken)
	}

	// Prompts are now sized so the server would count the target tokens.
	var tokens []float64
	for i := 0; i < 2000; i++ {
		tokens = append(tokens, float64(generator.Next().PromptChars)/trueRatio)
	}
	if mean, _ := meanAndStddev(tokens); math.Abs(mean-1024) > 0.03*1024 {
		t.Errorf("post-calibration mean prompt tokens = %.1f, want about 1024", mean)
	}
}

func TestCalibratorConcurrent(t *testing.T) {
	calibrator := NewCalibrator(0)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				calibrator.Observe("m", 300, 100)
				calibrator.CharsPerToken("m")
			}
		}()
	}
	wg.Wait()
	if got := calibrator.CharsPerToken("m"); got != 3 {
		t.Errorf("ratio = %v, want 3", got)
	}
}

func TestChatBody(t *testing.T) {
	request := newTestGenerator(t, testWorkload(spec.WorkloadApiChat, true), nil, Options{}).Next()
	body := decodeBody(t, request)
	if request.Path != PathChat || !request.Streaming {
		t.Errorf("path %q streaming %v", request.Path, request.Streaming)
	}
	messages := body["messages"].([]any)
	message := messages[0].(map[string]any)
	if len(messages) != 1 || message["role"] != "user" || len(message["content"].(string)) != request.PromptChars {
		t.Errorf("messages = %v", messages)
	}
	if body["model"] != "test-model" || body["stream"] != true {
		t.Errorf("model/stream = %v/%v", body["model"], body["stream"])
	}
	if options, _ := body["stream_options"].(map[string]any); options["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage", body["stream_options"])
	}
	if _, has := body["min_tokens"]; has {
		t.Error("min_tokens sent without SetMinTokens")
	}
	if _, has := body["prompt"]; has {
		t.Error("chat body must not carry prompt")
	}
}

func TestCompletionBodyNonStreaming(t *testing.T) {
	request := newTestGenerator(t, testWorkload(spec.WorkloadApiCompletion, false), nil, Options{SetMinTokens: true}).Next()
	body := decodeBody(t, request)
	if request.Path != PathCompletion || request.Streaming {
		t.Errorf("path %q streaming %v", request.Path, request.Streaming)
	}
	if prompt, _ := body["prompt"].(string); len(prompt) != request.PromptChars || prompt == "" {
		t.Errorf("prompt length %d, want %d", len(prompt), request.PromptChars)
	}
	if body["stream"] != false {
		t.Errorf("stream = %v", body["stream"])
	}
	if _, has := body["stream_options"]; has {
		t.Error("stream_options must only be sent when streaming")
	}
	if body["min_tokens"] != body["max_tokens"] || int(body["max_tokens"].(float64)) != request.ExpectedOutputTokens {
		t.Errorf("min/max tokens = %v/%v", body["min_tokens"], body["max_tokens"])
	}
	if _, has := body["messages"]; has {
		t.Error("completion body must not carry messages")
	}
}

func TestEmbeddingsBody(t *testing.T) {
	request := newTestGenerator(t, testWorkload(spec.WorkloadApiEmbeddings, true), nil, Options{SetMinTokens: true}).Next()
	body := decodeBody(t, request)
	if request.Path != PathEmbeddings || request.Streaming || request.ExpectedOutputTokens != 0 {
		t.Errorf("path %q streaming %v output %d", request.Path, request.Streaming, request.ExpectedOutputTokens)
	}
	if input, _ := body["input"].(string); len(input) != request.PromptChars || input == "" {
		t.Errorf("input length %d, want %d", len(input), request.PromptChars)
	}
	for _, field := range []string{"max_tokens", "min_tokens", "stream", "stream_options", "messages", "prompt"} {
		if _, has := body[field]; has {
			t.Errorf("embeddings body must not carry %s", field)
		}
	}
}

func TestNewRejectsBadWorkloads(t *testing.T) {
	badAPI := testWorkload("images", true)
	noInput := testWorkload(spec.WorkloadApiChat, true)
	noInput.Dataset.InputTokens.Mean = 0
	noOutput := testWorkload(spec.WorkloadApiChat, true)
	noOutput.Dataset.OutputTokens.Mean = 0
	for name, workload := range map[string]spec.Workload{"api": badAPI, "input": noInput, "output": noOutput} {
		if _, err := New(workload, "m", nil, Options{}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	embeddingsNoOutput := noOutput
	embeddingsNoOutput.Api = spec.WorkloadApiEmbeddings
	if _, err := New(embeddingsNoOutput, "m", nil, Options{}); err != nil {
		t.Errorf("embeddings need no output distribution: %v", err)
	}
}

func TestPromptsAreRandomWords(t *testing.T) {
	generator := newTestGenerator(t, testWorkload(spec.WorkloadApiCompletion, false), nil, Options{})
	first := decodeBody(t, generator.Next())["prompt"].(string)
	second := decodeBody(t, generator.Next())["prompt"].(string)
	if first == second || !strings.Contains(first, " ") {
		t.Error("consecutive prompts must differ and be multi-word text")
	}
}

func TestSetMinTokensChangesHash(t *testing.T) {
	workload := testWorkload(spec.WorkloadApiChat, true)
	without := newTestGenerator(t, workload, nil, Options{}).Info().Hash
	with := newTestGenerator(t, workload, nil, Options{SetMinTokens: true}).Info().Hash
	if without == with {
		t.Error("toggling SetMinTokens must change the hash")
	}
	if direct, _ := Hash(workload, Options{SetMinTokens: true}); direct != with {
		t.Error("New and Hash disagree")
	}
}

func TestCacheSalt(t *testing.T) {
	workload := testWorkload(spec.WorkloadApiCompletion, false)
	saltA := newTestGenerator(t, workload, nil, Options{CacheSalt: "run-a"})
	saltB := newTestGenerator(t, workload, nil, Options{CacheSalt: "run-b"})
	plain := newTestGenerator(t, workload, nil, Options{})
	unsalted := newTestGenerator(t, workload, nil, Options{})
	if saltA.Info().Hash != plain.Info().Hash || saltB.Info().Hash != plain.Info().Hash {
		t.Error("the salt must not change the hash")
	}
	for i := 0; i < 50; i++ {
		a, b, p, u := saltA.Next(), saltB.Next(), plain.Next(), unsalted.Next()
		if a.PromptChars != b.PromptChars || a.ExpectedInputTokens != b.ExpectedInputTokens {
			t.Fatalf("request %d: salts must give the same lengths", i)
		}
		promptA := decodeBody(t, a)["prompt"].(string)
		promptB := decodeBody(t, b)["prompt"].(string)
		if promptA == promptB || promptA[:20] == promptB[:20] {
			t.Fatalf("request %d: salted prompts must not share a prefix", i)
		}
		if len(promptA) != a.PromptChars || a.PromptChars != p.PromptChars {
			t.Fatalf("request %d: salt must be inside PromptChars (%d, %d, %d)", i, len(promptA), a.PromptChars, p.PromptChars)
		}
		if string(p.Body) != string(u.Body) {
			t.Fatalf("request %d: no salt must be deterministic", i)
		}
	}
	if prompt := decodeBody(t, plain.Next())["prompt"].(string); strings.HasPrefix(prompt, "[") {
		t.Error("no salt must add no prefix")
	}
	repeat := newTestGenerator(t, workload, nil, Options{CacheSalt: "run-a"})
	again := newTestGenerator(t, workload, nil, Options{CacheSalt: "run-a"})
	if string(repeat.Next().Body) != string(again.Next().Body) {
		t.Error("the same salt must reproduce the same prompts")
	}
}

// knownHash is the hash of testWorkload(chat, streaming) from before
// padding_bytes existed: a workload without padding must keep it, or every
// earlier run would stop being comparable.
const knownHash = "52b47c018da662156dcf68f0e5ed338e3315fdc2ddd0755adadd717184ab4885"

func TestPaddingGrowsTheBodyAndKeepsUnpaddedHashes(t *testing.T) {
	base := testWorkload(spec.WorkloadApiChat, true)
	if hash, _ := Hash(base, Options{}); hash != knownHash {
		t.Errorf("an unpadded workload's hash changed to %s", hash)
	}
	padded := cloneWorkload(base)
	padded.Dataset.PaddingBytes = 64 << 10
	plain, wide := newTestGenerator(t, base, nil, Options{}).Next(), newTestGenerator(t, padded, nil, Options{}).Next()
	if grown := len(wide.Body) - len(plain.Body); grown != 64<<10 {
		t.Errorf("padding grew the body by %d bytes, want %d", grown, 64<<10)
	}
	if wide.PromptChars-plain.PromptChars != 64<<10 {
		t.Errorf("the calibrator must see the padding: %d → %d chars", plain.PromptChars, wide.PromptChars)
	}
	if !strings.HasPrefix(decodeBody(t, wide)["messages"].([]any)[0].(map[string]any)["content"].(string), decodeBody(t, plain)["messages"].([]any)[0].(map[string]any)["content"].(string)) {
		t.Error("padding must follow the unpadded prompt, not replace it")
	}
}

// cloneWorkload copies the Dataset the workload points to, so a test that
// changes the copy leaves the original alone.
func cloneWorkload(workload spec.Workload) spec.Workload {
	if workload.Dataset != nil {
		dataset := *workload.Dataset
		workload.Dataset = &dataset
	}
	return workload
}
