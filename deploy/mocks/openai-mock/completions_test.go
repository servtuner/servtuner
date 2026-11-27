package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

var updateFixtures = flag.Bool("update", false, "rewrite testdata/*.sse from the running mock")

const recordedStreamFixture = "testdata/mock-completions-usage.sse"

// poster sends a request to a running mock and returns the response and the time it was sent.
type poster func(path, body string) (*http.Response, time.Time)

// eachTLSMode runs test once against a plain-HTTP mock and once against one that requires client certificates, with cfg's
// timing and token settings applied to both.
func eachTLSMode(t *testing.T, cfg Config, test func(t *testing.T, post poster)) {
	t.Helper()
	run := func(t *testing.T, scheme, address string, client *http.Client) {
		test(t, func(path, body string) (*http.Response, time.Time) {
			t.Helper()
			sent := time.Now()
			response, err := client.Post(scheme+"://"+address+path, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = response.Body.Close() })
			return response, sent
		})
	}
	t.Run("plain", func(t *testing.T) {
		plain := cfg
		plain.TLS = tlsOff
		run(t, "http", startMock(t, plain), newClient(nil))
	})
	t.Run("mtls", func(t *testing.T) {
		pki := newTestPKI(t)
		mutual := mutualConfig(pki)
		mutual.OutputTokens, mutual.TimeToFirstToken, mutual.InterTokenLatency = cfg.OutputTokens, cfg.TimeToFirstToken, cfg.InterTokenLatency
		mutual.SendRoleOnlyFirstChunk, mutual.HonorRequestMaxTokens = cfg.SendRoleOnlyFirstChunk, cfg.HonorRequestMaxTokens
		client := newClient(&tls.Config{RootCAs: pki.caPool, Certificates: []tls.Certificate{pki.clientCertificate}, MinVersion: tls.VersionTLS12})
		run(t, "https", startMock(t, mutual), client)
	})
}

type textChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Choices []struct {
		Text         string  `json:"text"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
}

func decodeTextChunk(t *testing.T, event sseEvent) textChunk {
	t.Helper()
	var decoded textChunk
	if err := json.Unmarshal([]byte(event.data), &decoded); err != nil {
		t.Fatalf("event %q is not JSON: %v", event.data, err)
	}
	return decoded
}

func TestCompletionsStreamingFormatAndUsage(t *testing.T) {
	cfg := plainConfig()
	cfg.OutputTokens = 4
	eachTLSMode(t, cfg, func(t *testing.T, post poster) {
		response, _ := post(completionsPath, `{"model":"m","prompt":"one two three","stream":true,"stream_options":{"include_usage":true}}`)
		if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
			t.Fatalf("content type %q", got)
		}
		events := readEvents(t, response)

		// 4 text chunks + finish chunk + usage chunk + [DONE]
		if len(events) != 7 {
			t.Fatalf("got %d events: %+v", len(events), events)
		}
		if events[6].data != "[DONE]" {
			t.Fatalf("last event %q", events[6].data)
		}
		for _, event := range events[:4] {
			text := decodeTextChunk(t, event)
			if text.Object != "text_completion" || text.Choices[0].Text != contentToken || text.Choices[0].FinishReason != nil {
				t.Fatalf("token chunk %+v", text)
			}
		}
		finish := decodeTextChunk(t, events[4])
		if finish.Choices[0].FinishReason == nil || *finish.Choices[0].FinishReason != "stop" || finish.Choices[0].Text != "" {
			t.Fatalf("finish chunk %+v", finish)
		}
		final := decodeTextChunk(t, events[5])
		if len(final.Choices) != 0 || final.Usage == nil || *final.Usage != (usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}) {
			t.Fatalf("usage chunk %+v", final)
		}
	})
}

func TestCompletionsStreamingOmitsUsageUnlessRequested(t *testing.T) {
	cfg := plainConfig()
	cfg.OutputTokens = 2
	eachTLSMode(t, cfg, func(t *testing.T, post poster) {
		response, _ := post(completionsPath, `{"prompt":"hi","stream":true}`)
		events := readEvents(t, response)
		for _, event := range events[:len(events)-1] {
			if decodeTextChunk(t, event).Usage != nil {
				t.Fatalf("unexpected usage in %q", event.data)
			}
		}
		if len(events) != 4 { // 2 text + finish + [DONE]
			t.Fatalf("got %d events", len(events))
		}
	})
}

func TestCompletionsNonStreamingReturnsTextAndUsage(t *testing.T) {
	cfg := plainConfig()
	cfg.OutputTokens = 3
	eachTLSMode(t, cfg, func(t *testing.T, post poster) {
		response, _ := post(completionsPath, `{"model":"m","prompt":"a b c d","max_tokens":5}`)
		var body struct {
			Object  string `json:"object"`
			Choices []struct {
				Text         string `json:"text"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage usage `json:"usage"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Object != "text_completion" || len(body.Choices) != 1 {
			t.Fatalf("body %+v", body)
		}
		if got := len(strings.Fields(body.Choices[0].Text)); got != 5 || body.Choices[0].FinishReason != "length" {
			t.Errorf("text has %d tokens, finish %q; want 5 and length", got, body.Choices[0].FinishReason)
		}
		if body.Usage != (usage{PromptTokens: 4, CompletionTokens: 5, TotalTokens: 9}) {
			t.Errorf("usage %+v", body.Usage)
		}
	})
}

func TestCompletionsMaxTokensIgnoredWhenNotHonored(t *testing.T) {
	cfg := plainConfig()
	cfg.OutputTokens = 3
	cfg.HonorRequestMaxTokens = false
	address := startMock(t, cfg)
	completion, err := newClient(nil).Post("http://"+address+completionsPath, "application/json", strings.NewReader(`{"prompt":"x","max_tokens":9}`))
	if err != nil {
		t.Fatal(err)
	}
	defer completion.Body.Close()
	var body struct {
		Usage usage `json:"usage"`
	}
	if err := json.NewDecoder(completion.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Usage.CompletionTokens != 3 {
		t.Errorf("usage %+v, want 3 completion tokens", body.Usage)
	}
}

func TestCompletionsPromptArrayUsesFirstPrompt(t *testing.T) {
	cfg := plainConfig()
	cfg.OutputTokens = 1
	eachTLSMode(t, cfg, func(t *testing.T, post poster) {
		response, _ := post(completionsPath, `{"prompt":["one two","three four five six"]}`)
		var body struct{ Usage usage }
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Usage.PromptTokens != 2 {
			t.Errorf("prompt tokens %d, want 2 (first prompt only)", body.Usage.PromptTokens)
		}
	})
}

func TestCompletionsRejectsBadBodies(t *testing.T) {
	eachTLSMode(t, plainConfig(), func(t *testing.T, post poster) {
		for _, body := range []string{`{not json`, `{"prompt":42}`, `{"prompt":[1,2,3]}`} {
			response, _ := post(completionsPath, body)
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400", body, response.StatusCode)
			}
		}
	})
}

func TestCompletionsStreamingTimingsAreHonoured(t *testing.T) {
	cfg := plainConfig()
	cfg.TimeToFirstToken = 150 * time.Millisecond
	cfg.InterTokenLatency = 40 * time.Millisecond
	cfg.OutputTokens = 4
	eachTLSMode(t, cfg, func(t *testing.T, post poster) {
		response, sent := post(completionsPath, `{"prompt":"hi","stream":true}`)
		headersAt := time.Now()
		events := readEvents(t, response)

		// Without an opening chunk the headers wait for the first token.
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
	})
}

func TestCompletionsOpeningChunkSeparatesTTFBFromTTFT(t *testing.T) {
	cfg := plainConfig()
	cfg.TimeToFirstToken = 200 * time.Millisecond
	cfg.OutputTokens = 2
	cfg.SendRoleOnlyFirstChunk = true
	eachTLSMode(t, cfg, func(t *testing.T, post poster) {
		response, sent := post(completionsPath, `{"prompt":"hi","stream":true}`)
		headersAt := time.Now()
		events := readEvents(t, response)

		if ttfb := headersAt.Sub(sent); ttfb > 120*time.Millisecond {
			t.Errorf("TTFB %s should not wait for the TTFT delay", ttfb)
		}
		if opening := decodeTextChunk(t, events[0]); opening.Choices[0].Text != "" {
			t.Fatalf("first chunk should carry no text: %+v", opening)
		}
		if ttft := events[1].receivedAt.Sub(sent); ttft < 170*time.Millisecond {
			t.Errorf("first text after %s, want about 200ms", ttft)
		}
	})
}

func TestNonStreamingTakesTimeToFinishGenerating(t *testing.T) {
	cfg := plainConfig()
	cfg.TimeToFirstToken = 100 * time.Millisecond
	cfg.InterTokenLatency = 30 * time.Millisecond
	cfg.OutputTokens = 4
	eachTLSMode(t, cfg, func(t *testing.T, post poster) {
		response, sent := post(completionsPath, `{"prompt":"hi"}`)
		_, _ = io.Copy(io.Discard, response.Body)
		if elapsed := time.Since(sent); elapsed < 160*time.Millisecond || elapsed > 2*time.Second {
			t.Errorf("non-streaming took %s, want about 190ms", elapsed)
		}
	})
}

func TestCompletionsAreCountedByPath(t *testing.T) {
	address := startMock(t, plainConfig())
	postChat(t, address, `{"messages":[]}`)
	for _, body := range []string{`{"prompt":"x"}`, `{"prompt":"x","stream":true}`} {
		response, err := newClient(nil).Post("http://"+address+completionsPath, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	response, err := newClient(nil).Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	for _, want := range []string{
		`openai_mock_requests_total{code="200",path="/v1/chat/completions",stream="false"} 1`,
		`openai_mock_requests_total{code="200",path="/v1/completions",stream="false"} 1`,
		`openai_mock_requests_total{code="200",path="/v1/completions",stream="true"} 1`,
		"openai_mock_time_to_first_token_seconds_count 3",
		"openai_mock_request_duration_seconds_count 3",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

var createdField = regexp.MustCompile(`"created":\d+`)

// TestRecordedStreamFixture keeps testdata/mock-completions-usage.sse equal to what the mock emits (with the
// timestamp zeroed). The bench SSE parser tests use a copy of it. Regenerate with `go test -run
// TestRecordedStreamFixture -update`.
func TestRecordedStreamFixture(t *testing.T) {
	cfg := plainConfig()
	cfg.OutputTokens = 4
	cfg.ModelName = "mock-model"
	address := startMock(t, cfg)
	response, err := newClient(nil).Post("http://"+address+completionsPath, "application/json",
		strings.NewReader(`{"model":"mock-model","prompt":"one two three","stream":true,"stream_options":{"include_usage":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	recorded := createdField.ReplaceAllString(string(raw), `"created":0`)

	if *updateFixtures {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recordedStreamFixture, []byte(recorded), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile(recordedStreamFixture)
	if err != nil {
		t.Fatal(err)
	}
	if string(committed) != recorded {
		t.Errorf("fixture is stale; run with -update.\nwant:\n%s\ngot:\n%s", recorded, committed)
	}
}
