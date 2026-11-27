package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sseEvent struct {
	data       string
	receivedAt time.Time
}

func postChat(t *testing.T, address, body string) (*http.Response, time.Time) {
	t.Helper()
	sent := time.Now()
	response, err := newClient(nil).Post("http://"+address+chatCompletionsPath, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response, sent
}

func readEvents(t *testing.T, response *http.Response) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
			events = append(events, sseEvent{data: data, receivedAt: time.Now()})
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

type chunk struct {
	Object  string `json:"object"`
	Choices []struct {
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
}

func decodeChunk(t *testing.T, event sseEvent) chunk {
	t.Helper()
	var decoded chunk
	if err := json.Unmarshal([]byte(event.data), &decoded); err != nil {
		t.Fatalf("event %q is not JSON: %v", event.data, err)
	}
	return decoded
}

func TestStreamingFormatAndUsage(t *testing.T) {
	cfg := platformConfig()
	cfg.OutputTokens = 4
	address := startMock(t, cfg)

	response, _ := postChat(t, address, `{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"one two three"}]}`)
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type %q", got)
	}
	events := readEvents(t, response)

	// 4 content chunks + finish chunk + usage chunk + [DONE]
	if len(events) != 7 {
		t.Fatalf("got %d events: %+v", len(events), events)
	}
	if events[len(events)-1].data != "[DONE]" {
		t.Fatalf("last event %q", events[len(events)-1].data)
	}
	first := decodeChunk(t, events[0])
	if first.Object != "chat.completion.chunk" || first.Choices[0].Delta.Role != "assistant" || first.Choices[0].Delta.Content == "" {
		t.Fatalf("first chunk should carry role and content when role-only is off: %+v", first)
	}
	finish := decodeChunk(t, events[4])
	if finish.Choices[0].FinishReason == nil || *finish.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish chunk %+v", finish)
	}
	final := decodeChunk(t, events[5])
	if len(final.Choices) != 0 || final.Usage == nil {
		t.Fatalf("usage chunk %+v", final)
	}
	if *final.Usage != (usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}) {
		t.Fatalf("usage %+v", *final.Usage)
	}
}

func TestStreamingOmitsUsageUnlessRequested(t *testing.T) {
	cfg := platformConfig()
	cfg.OutputTokens = 2
	response, _ := postChat(t, startMock(t, cfg), `{"stream":true,"messages":[]}`)
	events := readEvents(t, response)
	for _, event := range events[:len(events)-1] {
		if decodeChunk(t, event).Usage != nil {
			t.Fatalf("unexpected usage in %q", event.data)
		}
	}
	if len(events) != 4 { // 2 content + finish + [DONE]
		t.Fatalf("got %d events", len(events))
	}
}

func TestMaxTokensDeterminesLengthWhenHonored(t *testing.T) {
	for _, honor := range []bool{true, false} {
		cfg := platformConfig()
		cfg.OutputTokens = 3
		cfg.HonorRequestMaxTokens = honor
		response, _ := postChat(t, startMock(t, cfg), `{"max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
		var body struct {
			Choices []struct {
				Message      struct{ Content string } `json:"message"`
				FinishReason string                   `json:"finish_reason"`
			} `json:"choices"`
			Usage usage `json:"usage"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want, wantReason := 3, "stop"
		if honor {
			want, wantReason = 5, "length"
		}
		if body.Usage.CompletionTokens != want || body.Choices[0].FinishReason != wantReason {
			t.Errorf("honor=%v: usage %+v finish %q", honor, body.Usage, body.Choices[0].FinishReason)
		}
		if got := len(strings.Fields(body.Choices[0].Message.Content)); got != want {
			t.Errorf("honor=%v: content has %d tokens, want %d", honor, got, want)
		}
	}
}

func TestStreamingTimingsAreHonoured(t *testing.T) {
	cfg := platformConfig()
	cfg.TimeToFirstToken = 150 * time.Millisecond
	cfg.InterTokenLatency = 40 * time.Millisecond
	cfg.OutputTokens = 4
	address := startMock(t, cfg)

	response, sent := postChat(t, address, `{"stream":true,"messages":[]}`)
	headersAt := time.Now()
	events := readEvents(t, response)

	// Without a role-only chunk the headers wait for the first token.
	if headersAt.Sub(sent) < 120*time.Millisecond {
		t.Errorf("TTFB %s is shorter than the configured TTFT", headersAt.Sub(sent))
	}
	lastToken := events[3].receivedAt
	if gap := lastToken.Sub(events[0].receivedAt); gap < 90*time.Millisecond {
		t.Errorf("3 inter-token gaps took only %s, want about 120ms", gap)
	}
	if total := lastToken.Sub(sent); total < 250*time.Millisecond || total > 2*time.Second {
		t.Errorf("total %s outside the expected range", total)
	}
}

func TestRoleOnlyFirstChunkSeparatesTTFBFromTTFT(t *testing.T) {
	cfg := platformConfig()
	cfg.TimeToFirstToken = 200 * time.Millisecond
	cfg.OutputTokens = 2
	cfg.SendRoleOnlyFirstChunk = true
	address := startMock(t, cfg)

	response, sent := postChat(t, address, `{"stream":true,"messages":[]}`)
	headersAt := time.Now()
	events := readEvents(t, response)

	if ttfb := headersAt.Sub(sent); ttfb > 120*time.Millisecond {
		t.Errorf("TTFB %s should not wait for the TTFT delay", ttfb)
	}
	role := decodeChunk(t, events[0])
	if role.Choices[0].Delta.Role != "assistant" || role.Choices[0].Delta.Content != "" {
		t.Fatalf("first chunk should be role-only: %+v", role)
	}
	if ttft := events[1].receivedAt.Sub(sent); ttft < 170*time.Millisecond {
		t.Errorf("first content token after %s, want about 200ms", ttft)
	}
}

func TestModelsHealthzAndMetrics(t *testing.T) {
	cfg := platformConfig()
	cfg.ModelName = "my-model"
	address := startMock(t, cfg)
	postChat(t, address, `{"messages":[]}`)

	get := func(path string) string {
		response, err := newClient(nil).Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, response.StatusCode)
		}
		return string(body)
	}
	if body := get(modelsPath); !strings.Contains(body, `"id":"my-model"`) {
		t.Errorf("models: %s", body)
	}
	if get("/healthz") != "ok" {
		t.Error("healthz")
	}
	metricsBody := get("/metrics")
	for _, want := range []string{
		`openai_mock_requests_total{code="200",path="/v1/chat/completions",stream="false"} 1`,
		"openai_mock_requests_in_flight 0",
		"openai_mock_time_to_first_token_seconds_count 1",
		"openai_mock_request_duration_seconds_count 1",
	} {
		if !strings.Contains(metricsBody, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestInvalidBodyIsRejected(t *testing.T) {
	response, _ := postChat(t, startMock(t, platformConfig()), `{not json`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", response.StatusCode)
	}
}

// OpenAI's newer chat field max_completion_tokens wins over max_tokens when both are present.
// Bifrost forwards only max_completion_tokens (clamped to at least 16), so the mock must honour it.
func TestChatMaxCompletionTokensTakesPrecedenceInBothTLSModes(t *testing.T) {
	cases := []struct {
		name, body string
		want       int
	}{
		{"max_completion_tokens alone", `{"max_completion_tokens":7,"messages":[{"role":"user","content":"hi"}]}`, 7},
		{"max_tokens alone", `{"max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, 5},
		{"both: max_completion_tokens wins", `{"max_tokens":5,"max_completion_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, 9},
	}
	post := func(t *testing.T, scheme, address string, client *http.Client, body string) int {
		response, err := client.Post(scheme+"://"+address+chatCompletionsPath, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var decoded struct {
			Usage usage `json:"usage"`
		}
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.Usage.CompletionTokens
	}
	modes := map[string]func(t *testing.T) (string, string, *http.Client){
		"platform": func(t *testing.T) (string, string, *http.Client) {
			cfg := platformConfig()
			cfg.OutputTokens = 3
			return "http", startMock(t, cfg), newClient(nil)
		},
		"mtls": func(t *testing.T) (string, string, *http.Client) {
			pki := newTestPKI(t)
			cfg := mutualConfig(pki)
			cfg.OutputTokens = 3
			client := newClient(&tls.Config{RootCAs: pki.caPool, Certificates: []tls.Certificate{pki.clientCertificate}, MinVersion: tls.VersionTLS12})
			return "https", startMock(t, cfg), client
		},
	}
	for mode, start := range modes {
		t.Run(mode, func(t *testing.T) {
			scheme, address, client := start(t)
			for _, c := range cases {
				if got := post(t, scheme, address, client, c.body); got != c.want {
					t.Errorf("%s: completion_tokens %d, want %d", c.name, got, c.want)
				}
			}
		})
	}
}
