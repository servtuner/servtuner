package sse

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var streamStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeClock only moves when a scripted reader says so, so no test waits.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// readStep is one Read: the clock advances by after, then data is returned.
type readStep struct {
	after time.Duration
	data  string
	err   error
}

// scriptedReader returns one step per Read, then io.EOF.
type scriptedReader struct {
	clock *fakeClock
	steps []readStep
}

func (r *scriptedReader) Read(buffer []byte) (int, error) {
	if len(r.steps) == 0 {
		return 0, io.EOF
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	r.clock.now = r.clock.now.Add(step.after)
	return copy(buffer, step.data), step.err
}

func parseSteps(t *testing.T, steps []readStep) (Result, error) {
	t.Helper()
	clock := &fakeClock{now: streamStart}
	return Parse(&scriptedReader{clock: clock, steps: steps}, clock)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// stepsPerEvent delivers one SSE event per Read, interval apart, so each event
// has its own arrival time: event i arrives at (i+1)*interval.
func stepsPerEvent(stream string, interval time.Duration) []readStep {
	var steps []readStep
	for _, event := range strings.SplitAfter(stream, "\n\n") {
		if event != "" {
			steps = append(steps, readStep{after: interval, data: event})
		}
	}
	return steps
}

// stepsOfSize delivers the stream in reads of at most size bytes, 1ms apart.
func stepsOfSize(stream string, size int) []readStep {
	var steps []readStep
	for start := 0; start < len(stream); start += size {
		steps = append(steps, readStep{after: time.Millisecond, data: stream[start:min(start+size, len(stream))]})
	}
	return steps
}

func at(offset time.Duration) time.Time { return streamStart.Add(offset) }

func mustParse(t *testing.T, steps []readStep) Result {
	t.Helper()
	result, err := parseSteps(t, steps)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return result
}

// TestRoleOnlyFirstChunkSeparatesFirstByteFromTTFT uses a stream recorded from the
// repo's mock with --role-only-first-chunk: event 0 is the role chunk (empty
// content), events 1-4 are tokens, then finish, usage and [DONE].
func TestRoleOnlyFirstChunkSeparatesFirstByteFromTTFT(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "mock-chat-role-only-first-chunk.sse"), 10*time.Millisecond))

	if result.FirstBodyByteAt != at(10*time.Millisecond) {
		t.Errorf("first body byte at %v, want 10ms", result.FirstBodyByteAt)
	}
	if ttft, ok := result.TTFT(streamStart); !ok || ttft != 20*time.Millisecond {
		t.Errorf("TTFT = %v, %v; want 20ms (first content, not the role chunk)", ttft, ok)
	}
	wantArrivals := []time.Time{at(20 * time.Millisecond), at(30 * time.Millisecond), at(40 * time.Millisecond), at(50 * time.Millisecond)}
	assertArrivals(t, result, wantArrivals)
	for _, gap := range result.InterTokenLatencies() {
		if gap != 10*time.Millisecond {
			t.Errorf("inter-token gaps %v, want all 10ms", result.InterTokenLatencies())
			break
		}
	}
	assertFinished(t, result, "stop", &Usage{PromptTokens: 1, CompletionTokens: 4, TotalTokens: 5})
}

// TestFirstChunkWithContentHasEqualFirstByteAndTTFT: the mock's default stream
// carries role and first token together.
func TestFirstChunkWithContentHasEqualFirstByteAndTTFT(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "mock-chat-usage.sse"), 10*time.Millisecond))
	if result.FirstBodyByteAt != result.FirstTokenAt || result.FirstTokenAt != at(10*time.Millisecond) {
		t.Errorf("first body byte %v, first token %v; want both at 10ms", result.FirstBodyByteAt, result.FirstTokenAt)
	}
	if len(result.TokenArrivals) != 4 {
		t.Errorf("%d token arrivals, want 4", len(result.TokenArrivals))
	}
	assertFinished(t, result, "stop", &Usage{PromptTokens: 1, CompletionTokens: 4, TotalTokens: 5})
}

// TestMockCompletionsStream uses a stream recorded from the mock's
// /v1/completions: text chunks, an empty-text finish chunk and a usage-only
// chunk. Every text chunk is content, so first content equals first token.
func TestMockCompletionsStream(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "mock-completions-usage.sse"), 10*time.Millisecond))
	if result.FirstContentAt != result.FirstTokenAt || result.FirstTokenAt != at(10*time.Millisecond) {
		t.Errorf("first content %v, first token %v; want both at 10ms", result.FirstContentAt, result.FirstTokenAt)
	}
	if len(result.TokenArrivals) != 4 {
		t.Errorf("%d token arrivals, want 4", len(result.TokenArrivals))
	}
	assertFinished(t, result, "stop", &Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7})
}

// TestOpenAIChatStream: role-only chunk with refusal and logprobs members, an
// empty-delta finish chunk and a usage-only chunk with empty choices.
func TestOpenAIChatStream(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-chat-usage.sse"), 5*time.Millisecond))
	assertArrivals(t, result, []time.Time{at(10 * time.Millisecond), at(15 * time.Millisecond), at(20 * time.Millisecond)})
	if result.FirstBodyByteAt != at(5*time.Millisecond) {
		t.Errorf("first byte at %v, want 5ms", result.FirstBodyByteAt)
	}
	assertFinished(t, result, "stop", &Usage{PromptTokens: 9, CompletionTokens: 3, TotalTokens: 12})
}

func TestCompletionsStreamUsesTextField(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-completions.sse"), 5*time.Millisecond))
	// The last chunk has empty text and the finish reason: not a token.
	assertArrivals(t, result, []time.Time{at(5 * time.Millisecond), at(10 * time.Millisecond)})
	assertFinished(t, result, "length", &Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7})
}

// TestKeepAliveCommentsCountAsFirstByteOnly: comments are not events, so they
// move the first body byte but never TTFT.
func TestKeepAliveCommentsCountAsFirstByteOnly(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-keepalive-comments.sse"), 10*time.Millisecond))
	if result.FirstBodyByteAt != at(10*time.Millisecond) {
		t.Errorf("first byte at %v, want the first keep-alive at 10ms", result.FirstBodyByteAt)
	}
	// Events: 2 comments, role chunk, comment+token (one event), finish, DONE.
	assertArrivals(t, result, []time.Time{at(40 * time.Millisecond)})
	assertFinished(t, result, "stop", nil)
}

func TestMultipleDataLinesFormOneEvent(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-multi-data-line.sse"), 10*time.Millisecond))
	assertArrivals(t, result, []time.Time{at(10 * time.Millisecond)})
	assertFinished(t, result, "stop", nil)
}

// TestCRLFLineEndings is the same stream with CRLF terminators, delivered whole
// and one byte at a time (which splits every CRLF), and with bare CR.
func TestCRLFLineEndings(t *testing.T) {
	for _, name := range []string{"openai-chat-usage.sse", "openai-keepalive-comments.sse", "openai-multi-data-line.sse"} {
		lf := fixture(t, name)
		want := mustParse(t, stepsOfSize(lf, 4096))
		for terminatorName, terminator := range map[string]string{"CRLF": "\r\n", "CR": "\r"} {
			converted := strings.ReplaceAll(lf, "\n", terminator)
			for _, size := range []int{4096, 1} {
				got := mustParse(t, stepsOfSize(converted, size))
				if len(got.TokenArrivals) != len(want.TokenArrivals) || got.FinishReason != want.FinishReason || !got.Done {
					t.Errorf("%s as %s in %d-byte reads: %d tokens, finish %q, done %v; want %d tokens, finish %q",
						name, terminatorName, size, len(got.TokenArrivals), got.FinishReason, got.Done, len(want.TokenArrivals), want.FinishReason)
				}
				if (got.Usage == nil) != (want.Usage == nil) || got.Usage != nil && *got.Usage != *want.Usage {
					t.Errorf("%s as %s in %d-byte reads: usage %v, want %v", name, terminatorName, size, got.Usage, want.Usage)
				}
			}
		}
	}
}

// TestFramesSplitAcrossReads: a token arrives when the Read that completes its
// event returns, not when its first bytes did.
func TestFramesSplitAcrossReads(t *testing.T) {
	stream := fixture(t, "openai-chat-usage.sse")
	const size = 7
	result := mustParse(t, stepsOfSize(stream, size))

	var wantArrivals []time.Time
	for _, token := range []string{`"content":"Hello"`, `"content":" there"`, `"content":"!"`} {
		eventEnd := strings.Index(stream[strings.Index(stream, token):], "\n\n") + strings.Index(stream, token) + 1
		readIndex := eventEnd / size
		wantArrivals = append(wantArrivals, at(time.Duration(readIndex+1)*time.Millisecond))
	}
	assertArrivals(t, result, wantArrivals)
	assertFinished(t, result, "stop", &Usage{PromptTokens: 9, CompletionTokens: 3, TotalTokens: 12})
}

func TestMidStreamErrorIsRecorded(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-error-midstream.sse"), 10*time.Millisecond))
	want := StreamError{Message: "The server had an error while processing your request.", Type: "server_error", Code: "internal_error"}
	if result.Error == nil || *result.Error != want {
		t.Errorf("Error = %+v, want %+v", result.Error, want)
	}
	if !result.Truncated || result.Done {
		t.Errorf("truncated %v, done %v; an errored stream is unfinished", result.Truncated, result.Done)
	}
	assertArrivals(t, result, []time.Time{at(20 * time.Millisecond)})
}

func TestStringErrorMember(t *testing.T) {
	result := mustParse(t, []readStep{{after: time.Millisecond, data: "data: {\"error\": \"upstream timed out\"}\n\n"}})
	if result.Error == nil || result.Error.Message != "upstream timed out" {
		t.Errorf("Error = %+v, want message %q", result.Error, "upstream timed out")
	}
}

// TestMissingDoneIsTruncated: a stream that just ends keeps what it delivered.
func TestMissingDoneIsTruncated(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-truncated.sse"), 10*time.Millisecond))
	if !result.Truncated || result.Done {
		t.Errorf("truncated %v, done %v; want truncated", result.Truncated, result.Done)
	}
	assertArrivals(t, result, []time.Time{at(20 * time.Millisecond), at(30 * time.Millisecond)})
}

// TestEventCutMidLineIsDropped: the connection died inside an event, so its
// half-received JSON is neither a token nor a parse error.
func TestEventCutMidLineIsDropped(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"cont"
	result := mustParse(t, []readStep{{after: time.Millisecond, data: stream}})
	assertArrivals(t, result, []time.Time{at(time.Millisecond)})
	if !result.Truncated {
		t.Error("want truncated")
	}
}

func TestDoneWithoutFinalBlankLine(t *testing.T) {
	result := mustParse(t, []readStep{{after: time.Millisecond, data: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]"}})
	if !result.Done || result.Truncated {
		t.Errorf("done %v, truncated %v; want done", result.Done, result.Truncated)
	}
}

func TestNothingAfterDoneIsRead(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n"
	result := mustParse(t, []readStep{{after: time.Millisecond, data: stream}})
	if len(result.TokenArrivals) != 1 {
		t.Errorf("%d tokens, want 1: data after [DONE] was counted", len(result.TokenArrivals))
	}
}

func TestEmptyStream(t *testing.T) {
	result := mustParse(t, nil)
	if !result.FirstBodyByteAt.IsZero() {
		t.Error("first body byte reported for an empty stream")
	}
	if _, ok := result.TTFT(streamStart); ok {
		t.Error("TTFT reported for an empty stream")
	}
	if !result.Truncated {
		t.Error("an empty stream is truncated")
	}
}

func TestReadErrorReturnsPartialResult(t *testing.T) {
	connectionReset := errors.New("connection reset")
	result, err := parseSteps(t, []readStep{
		{after: time.Millisecond, data: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"},
		{after: time.Millisecond, err: connectionReset},
	})
	if !errors.Is(err, connectionReset) {
		t.Errorf("err = %v, want the read error", err)
	}
	assertArrivals(t, result, []time.Time{at(time.Millisecond)})
	if !result.Truncated {
		t.Error("want truncated")
	}
}

func TestMalformedChunkIsAnError(t *testing.T) {
	_, err := parseSteps(t, []readStep{{after: time.Millisecond, data: "data: {not json}\n\n"}})
	if err == nil || !strings.Contains(err.Error(), "{not json}") {
		t.Errorf("err = %v, want a decode error naming the payload", err)
	}
}

func assertArrivals(t *testing.T, result Result, want []time.Time) {
	t.Helper()
	if len(result.TokenArrivals) != len(want) {
		t.Fatalf("token arrivals %v, want %v", result.TokenArrivals, want)
	}
	for i := range want {
		if !result.TokenArrivals[i].Equal(want[i]) {
			t.Errorf("token %d arrived at %v, want %v", i, result.TokenArrivals[i].Sub(streamStart), want[i].Sub(streamStart))
		}
	}
	if len(want) > 0 && !result.FirstTokenAt.Equal(want[0]) {
		t.Errorf("FirstTokenAt %v, want %v", result.FirstTokenAt, want[0])
	}
}

func assertFinished(t *testing.T, result Result, wantFinishReason string, wantUsage *Usage) {
	t.Helper()
	if !result.Done || result.Truncated {
		t.Errorf("done %v, truncated %v; want a complete stream", result.Done, result.Truncated)
	}
	if result.FinishReason != wantFinishReason {
		t.Errorf("finish reason %q, want %q", result.FinishReason, wantFinishReason)
	}
	if (result.Usage == nil) != (wantUsage == nil) || result.Usage != nil && *result.Usage != *wantUsage {
		t.Errorf("usage %+v, want %+v", result.Usage, wantUsage)
	}
	if result.Error != nil {
		t.Errorf("unexpected stream error %+v", result.Error)
	}
}

// TestReasoningChunksCountAsTTFT: TTFT is the first generated token of any
// kind, so reasoning before the answer sets it; FirstContentAt is the first
// visible answer. Events: role, 2 reasoning_content, 1 reasoning, 2 content.
func TestReasoningChunksCountAsTTFT(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-reasoning-then-content.sse"), 10*time.Millisecond))
	if ttft, ok := result.TTFT(streamStart); !ok || ttft != 20*time.Millisecond {
		t.Errorf("TTFT = %v, %v; want 20ms, the first reasoning chunk", ttft, ok)
	}
	if result.FirstContentAt != at(50*time.Millisecond) {
		t.Errorf("FirstContentAt = %v, want 50ms", result.FirstContentAt.Sub(streamStart))
	}
	if len(result.TokenArrivals) != 5 {
		t.Errorf("%d token arrivals, want 5", len(result.TokenArrivals))
	}
}

// TestToolCallChunksCountAsTokens: the stream has a role chunk, a tool-call
// header chunk, an argument fragment and a finish chunk with tool_calls.
func TestToolCallChunksCountAsTokens(t *testing.T) {
	result := mustParse(t, stepsPerEvent(fixture(t, "openai-tool-calls.sse"), 10*time.Millisecond))
	assertArrivals(t, result, []time.Time{at(20 * time.Millisecond), at(30 * time.Millisecond)})
	if !result.FirstContentAt.IsZero() {
		t.Errorf("FirstContentAt = %v, want zero: no content was generated", result.FirstContentAt)
	}
	assertFinished(t, result, "tool_calls", &Usage{PromptTokens: 20, CompletionTokens: 12, TotalTokens: 32})
}

// TestTPOTUsesUsageNotChunkCount: 3 chunks carry 9 tokens, so the chunk
// interval is not the per-token time. TPOT = (e2e - ttft) / (9 - 1).
func TestTPOTUsesUsageNotChunkCount(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"a b c\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" d e f\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" g h i\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":9,\"total_tokens\":13}}\n\n" +
		"data: [DONE]\n\n"
	result := mustParse(t, stepsPerEvent(stream, 40*time.Millisecond))
	if gaps := result.InterTokenLatencies(); len(gaps) != 2 || gaps[0] != 40*time.Millisecond {
		t.Errorf("chunk intervals %v, want two of 40ms", gaps)
	}
	// ttft 40ms, e2e 160ms (the [DONE] read), 8 tokens after the first.
	if tpot, ok := result.TPOT(streamStart); !ok || tpot != 15*time.Millisecond {
		t.Errorf("TPOT = %v, %v; want 15ms", tpot, ok)
	}
}

func TestTPOTNeedsUsageAndTwoTokens(t *testing.T) {
	noUsage := mustParse(t, stepsPerEvent(fixture(t, "openai-keepalive-comments.sse"), time.Millisecond))
	if _, ok := noUsage.TPOT(streamStart); ok {
		t.Error("TPOT reported without usage")
	}
	oneToken := Result{Usage: &Usage{CompletionTokens: 1}, FirstTokenAt: at(time.Millisecond), LastByteAt: at(2 * time.Millisecond)}
	if _, ok := oneToken.TPOT(streamStart); ok {
		t.Error("TPOT reported for a single completion token")
	}
}

const oneTokenChunk = "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"

// TestLongLineParses: a 1 MB line is legal and is found without rescanning the
// buffer on every Read.
func TestLongLineParses(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 1<<20) + "\"}}]}\n\ndata: [DONE]\n\n"
	result := mustParse(t, stepsOfSize(stream, readBufferBytes))
	if len(result.TokenArrivals) != 1 || !result.Done {
		t.Errorf("%d tokens, done %v; want 1 token and done", len(result.TokenArrivals), result.Done)
	}
}

// TestLineOverCapFailsFast: a server that never sends a newline is cut off at
// the cap instead of being buffered, and quickly.
func TestLineOverCapFailsFast(t *testing.T) {
	stream := "data: " + strings.Repeat("x", maxLineBytes+readBufferBytes)
	started := time.Now()
	_, err := parseSteps(t, stepsOfSize(stream, readBufferBytes))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want a line-cap error", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("took %s to reject an oversized line", elapsed)
	}
}

func TestEventDataOverCapFails(t *testing.T) {
	line := "data: " + strings.Repeat("x", 1<<20) + "\n"
	_, err := parseSteps(t, stepsOfSize(strings.Repeat(line, 5), readBufferBytes))
	if err == nil || !strings.Contains(err.Error(), "event data exceeds") {
		t.Errorf("err = %v, want an event-data cap error", err)
	}
}

func TestLeadingByteOrderMarkIsStripped(t *testing.T) {
	for _, size := range []int{4096, 1, 2} {
		result := mustParse(t, stepsOfSize("\xEF\xBB\xBF"+oneTokenChunk+"data: [DONE]\n\n", size))
		if len(result.TokenArrivals) != 1 || !result.Done {
			t.Errorf("%d-byte reads: %d tokens, done %v; want 1 token and done", size, len(result.TokenArrivals), result.Done)
		}
	}
}

func TestDoneMarkerTrimsWhitespace(t *testing.T) {
	result := mustParse(t, []readStep{{after: time.Millisecond, data: oneTokenChunk + "data:  [DONE] \n\n"}})
	if !result.Done {
		t.Error("[DONE] with surrounding spaces was not recognised")
	}
}

func TestArrayContentPartsCountAsTokens(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"\"}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"Hi\"}]}}]}\n\n" +
		"data: [DONE]\n\n"
	result := mustParse(t, stepsPerEvent(stream, 10*time.Millisecond))
	assertArrivals(t, result, []time.Time{at(20 * time.Millisecond)})
	if result.FirstContentAt != at(20*time.Millisecond) {
		t.Errorf("FirstContentAt = %v, want 20ms", result.FirstContentAt.Sub(streamStart))
	}
}

// TestOnlyChoiceZeroIsFollowed: with n > 1 a stream interleaves choices; only
// index 0 sets token arrivals and the finish reason.
func TestOnlyChoiceZeroIsFollowed(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"x\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":1,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	result := mustParse(t, stepsPerEvent(stream, 10*time.Millisecond))
	assertArrivals(t, result, []time.Time{at(20 * time.Millisecond)})
	assertFinished(t, result, "stop", nil)
}
