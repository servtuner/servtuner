// Package sse parses the Server-Sent Events stream of a streaming OpenAI-style
// response (chat completions or completions) and timestamps what it sees, so
// loadgen can record the client-side latencies (TTFB, TTFT, ITL, E2E).
//
// Definitions, fixed here because every engine's records depend on them:
//
//   - TTFB is not measured here. It is the arrival of the response headers,
//
// which the worker takes from httptrace.ClientTrace's
//
//	  GotFirstResponseByte before it has a body to give this parser.
//	  FirstBodyByteAt is the first body byte, which differs when a server sends
//	  headers early; a large gap to TTFB points at a proxy that buffers.
//	- TTFT is the time to the first generated token of any kind (FirstTokenAt):
//	  a chunk with non-empty delta.content, delta.reasoning_content,
//	  delta.reasoning, delta.tool_calls, or text (completions). That agrees
//	  with server-side TTFT, which counts reasoning tokens, and it does not hide
//	  queueing behind a long reasoning phase. A role-only chunk, an empty delta
//	  and a usage-only chunk are not tokens.
//	- FirstContentAt is the first non-empty content or text, the time to the
//	  first visible answer. For a reasoning model it is later than FirstTokenAt.
//	  It has no schema column yet.
//	- A token arrival is the arrival of one such chunk (TokenArrivals). A chunk
//	  may carry more than one token (vLLM stream intervals, speculative
//	  decoding), so the gap between arrivals (ITL) is a chunk interval, not
//	  always a token interval. TPOT is computed from usage instead:
//	  (e2e - ttft) / (completion_tokens - 1), see Result.TPOT.
//
// With n > 1 a stream interleaves several choices. The parser follows choice
// index 0 only for token arrivals and the finish reason; servtuner never sends
// n > 1. Usage is request-wide and is read from any chunk.
//
// A line, and the data of one event, is capped at maxLineBytes, so a server
// that never sends a newline cannot make the client buffer without limit.
//
// The parser reads through a Clock so tests are deterministic. An event's
// arrival time is the time of the Read that completed it, the first moment the
// client could act on it.
package sse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Clock is the source of arrival times.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock; time.Now carries the monotonic reading, so
// differences between arrivals are not affected by clock steps.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// doneMarker is the data payload that ends an OpenAI stream.
const doneMarker = "[DONE]"

// readBufferBytes is the size of one Read. It bounds how much arrives under one
// timestamp, so it is kept small enough for tokens to stay distinguishable.
const readBufferBytes = 4096

// maxLineBytes caps one line, and the data of one event, so that a server that
// never sends a newline cannot make the client buffer without limit. A real
// chunk is a few hundred bytes; 4 MiB leaves room for large tool-call payloads.
const maxLineBytes = 4 << 20

// byteOrderMark is the UTF-8 BOM, which the SSE specification says to strip
// from the start of a stream.
var byteOrderMark = []byte{0xEF, 0xBB, 0xBF}

// Usage is the token accounting a server reports when asked for it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamError is an error object the server sent inside the stream, after the
// response had already started with status 200.
type StreamError struct {
	Message string
	Type    string
	Code    string
}

func (e StreamError) Error() string {
	return fmt.Sprintf("stream error %s (%s): %s", e.Type, e.Code, e.Message)
}

// Result is everything observed on one stream. Times are absolute readings of
// the Clock; subtract the send time to get TTFT.
type Result struct {
	// FirstBodyByteAt is when the first body byte arrived. Zero when none did.
	// It is not TTFB, which the worker takes from the response headers.
	FirstBodyByteAt time.Time
	// LastByteAt is when the last body byte arrived, the stream's end.
	LastByteAt time.Time
	// FirstTokenAt is when the first chunk with generated text of any kind
	// arrived (the TTFT instant). Zero when the stream produced none.
	FirstTokenAt time.Time
	// FirstContentAt is when the first non-empty content or text arrived. Zero
	// when there was none; later than FirstTokenAt after reasoning.
	FirstContentAt time.Time
	// TokenArrivals holds one time per chunk with generated text, in order. The
	// first entry equals FirstTokenAt.
	TokenArrivals []time.Time
	// Usage is the last usage object seen, nil when the stream had none.
	Usage *Usage
	// FinishReason is the last finish_reason seen, empty when none was sent.
	FinishReason string
	// Error is set when the server sent an error object in the stream.
	Error *StreamError
	// Done is true when the [DONE] marker was received.
	Done bool
	// Truncated is true when the stream ended without [DONE]: the connection
	// dropped or the server stopped early. A stream that ended with Error is
	// also truncated, since the server did not finish it.
	Truncated bool
}

// TTFT returns the time from sentAt to the first generated token, and false
// when there was none.
func (r Result) TTFT(sentAt time.Time) (time.Duration, bool) {
	return since(sentAt, r.FirstTokenAt)
}

// TPOT returns the mean time per output token after the first,
// (e2e - ttft) / (completion_tokens - 1), with e2e measured to the last body
// byte. It uses usage.completion_tokens because a chunk may hold several
// tokens. It returns false without usage, with fewer than two completion
// tokens, or without a first token.
func (r Result) TPOT(sentAt time.Time) (time.Duration, bool) {
	if r.Usage == nil || r.Usage.CompletionTokens < 2 || r.FirstTokenAt.IsZero() {
		return 0, false
	}
	return r.LastByteAt.Sub(r.FirstTokenAt) / time.Duration(r.Usage.CompletionTokens-1), true
}

func since(start, event time.Time) (time.Duration, bool) {
	if event.IsZero() {
		return 0, false
	}
	return event.Sub(start), true
}

// InterTokenLatencies returns the gaps between consecutive token arrivals. A
// chunk can carry several tokens (stream intervals, speculative decoding), so
// these are chunk intervals; use TPOT for the per-token mean.
func (r Result) InterTokenLatencies() []time.Duration {
	if len(r.TokenArrivals) < 2 {
		return nil
	}
	gaps := make([]time.Duration, 0, len(r.TokenArrivals)-1)
	for i := 1; i < len(r.TokenArrivals); i++ {
		gaps = append(gaps, r.TokenArrivals[i].Sub(r.TokenArrivals[i-1]))
	}
	return gaps
}

// Parse reads the stream until [DONE], EOF or a read error. It returns the
// partial Result with the error when the reader fails or a data payload is not
// valid JSON; a server's own in-stream error is a Result.Error, not an error
// return, because it is a measurement of the target, not a failure of the
// parser.
func Parse(body io.Reader, clock Clock) (Result, error) {
	parser := &parser{}
	buffer := make([]byte, readBufferBytes)
	for {
		readBytes, readErr := body.Read(buffer)
		if readBytes > 0 {
			parser.arrivedAt = clock.Now()
			if parser.result.FirstBodyByteAt.IsZero() {
				parser.result.FirstBodyByteAt = parser.arrivedAt
			}
			parser.result.LastByteAt = parser.arrivedAt
			parser.pending = append(parser.pending, buffer[:readBytes]...)
			if err := parser.consumeLines(false); err != nil {
				return parser.finish(), err
			}
			if parser.result.Done {
				return parser.finish(), nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			if err := parser.consumeLines(true); err != nil && readErr == nil {
				readErr = err
			}
			return parser.finish(), readErr
		}
	}
}

type parser struct {
	arrivedAt time.Time
	// pending holds bytes not yet forming a complete line.
	pending []byte
	// scanned is how many leading bytes of pending are known to hold no line
	// terminator, so each byte is searched once, not once per Read.
	scanned int
	// hasCheckedByteOrderMark is set once the start of the stream was examined.
	hasCheckedByteOrderMark bool
	// eventData holds the data lines of the event being assembled.
	eventData      [][]byte
	eventDataBytes int
	result         Result
}

func (p *parser) finish() Result {
	p.result.Truncated = !p.result.Done
	return p.result
}

// consumeLines splits pending into lines, accepting LF, CRLF and a lone CR as
// terminators (the SSE specification). A CR at the very end of the buffer might
// be the first half of CRLF, so it waits for more bytes unless the stream
// ended.
func (p *parser) consumeLines(atEOF bool) error {
	if !p.stripByteOrderMark(atEOF) {
		return nil
	}
	for {
		relativeEnd := bytes.IndexAny(p.pending[p.scanned:], "\r\n")
		if relativeEnd < 0 {
			p.scanned = len(p.pending)
			break
		}
		end := p.scanned + relativeEnd
		terminatorBytes := 1
		if p.pending[end] == '\r' {
			if end+1 == len(p.pending) && !atEOF {
				p.scanned = end
				break
			}
			if end+1 < len(p.pending) && p.pending[end+1] == '\n' {
				terminatorBytes = 2
			}
		}
		line := p.pending[:end]
		err := p.consumeLine(line)
		p.pending = p.pending[end+terminatorBytes:]
		p.scanned = 0
		if err != nil || p.result.Done {
			return err
		}
	}
	if len(p.pending) > maxLineBytes {
		return fmt.Errorf("stream line exceeds %d bytes without a line terminator", maxLineBytes)
	}
	if atEOF {
		return p.finishAtEOF()
	}
	return nil
}

// stripByteOrderMark removes a BOM at the start of the stream. It returns false
// while the first bytes could still turn out to be a BOM, so the caller waits
// for more.
func (p *parser) stripByteOrderMark(atEOF bool) bool {
	if p.hasCheckedByteOrderMark {
		return true
	}
	if len(p.pending) < len(byteOrderMark) && bytes.HasPrefix(byteOrderMark, p.pending) && !atEOF {
		return false
	}
	p.hasCheckedByteOrderMark = true
	p.pending = bytes.TrimPrefix(p.pending, byteOrderMark)
	return true
}

// finishAtEOF handles bytes left when the stream ends. The SSE specification
// drops an event that has no terminating blank line, because the connection may
// have died mid-event. A [DONE] marker is the one exception: nothing follows
// it, and some proxies omit the final blank line.
func (p *parser) finishAtEOF() error {
	if len(p.pending) > 0 {
		if err := p.consumeLine(p.pending); err != nil {
			return err
		}
		p.pending = nil
	}
	if isDoneMarker(bytes.Join(p.eventData, []byte("\n"))) {
		p.result.Done = true
	}
	return nil
}

func (p *parser) consumeLine(line []byte) error {
	if len(line) == 0 {
		return p.dispatchEvent()
	}
	if line[0] == ':' {
		return nil // comment or keep-alive; it only counts as a first byte
	}
	field, value, _ := bytes.Cut(line, []byte(":"))
	if string(field) != "data" {
		return nil // event, id and retry carry nothing we measure
	}
	value = bytes.TrimPrefix(value, []byte(" "))
	p.eventDataBytes += len(value)
	if p.eventDataBytes > maxLineBytes {
		return fmt.Errorf("stream event data exceeds %d bytes", maxLineBytes)
	}
	p.eventData = append(p.eventData, append([]byte(nil), value...))
	return nil
}

// dispatchEvent handles one complete event: its data lines joined by newlines.
func (p *parser) dispatchEvent() error {
	if len(p.eventData) == 0 {
		return nil
	}
	payload := bytes.Join(p.eventData, []byte("\n"))
	p.eventData = nil
	p.eventDataBytes = 0
	if isDoneMarker(payload) {
		p.result.Done = true
		return nil
	}
	return p.recordChunk(payload)
}

// isDoneMarker matches [DONE] after trimming surrounding whitespace, which some
// servers and proxies add.
func isDoneMarker(payload []byte) bool {
	return string(bytes.TrimSpace(payload)) == doneMarker
}

// chunk is the part of a chat.completion.chunk or text_completion we read.
type chunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
			ToolCalls        json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
		Text         string  `json:"text"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage          `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func (p *parser) recordChunk(payload []byte) error {
	var decoded chunk
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return fmt.Errorf("decode stream chunk %q: %w", truncateForError(payload), err)
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		p.result.Error = decodeStreamError(decoded.Error)
	}
	if decoded.Usage != nil {
		p.result.Usage = decoded.Usage
	}
	hasToken, hasContent := false, false
	for _, choice := range decoded.Choices {
		if choice.Index != 0 {
			continue // n > 1: only choice 0 is followed; servtuner never sends n > 1
		}
		if hasText(choice.Delta.Content) || choice.Text != "" {
			hasContent = true
		}
		if hasContent || choice.Delta.ReasoningContent != "" || choice.Delta.Reasoning != "" || hasToolCalls(choice.Delta.ToolCalls) {
			hasToken = true
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			p.result.FinishReason = *choice.FinishReason
		}
	}
	if hasContent && p.result.FirstContentAt.IsZero() {
		p.result.FirstContentAt = p.arrivedAt
	}
	if hasToken {
		if p.result.FirstTokenAt.IsZero() {
			p.result.FirstTokenAt = p.arrivedAt
		}
		p.result.TokenArrivals = append(p.result.TokenArrivals, p.arrivedAt)
	}
	return nil
}

// hasText reports whether a delta's content holds text. Content is a string,
// null, or (for multimodal servers) an array of parts with a text member.
func hasText(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '"':
		var text string
		return json.Unmarshal(trimmed, &text) == nil && text != ""
	case '[':
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(trimmed, &parts) != nil {
			return false
		}
		for _, part := range parts {
			if part.Text != "" {
				return true
			}
		}
	}
	return false
}

// hasToolCalls reports whether a delta carries tool-call fragments: present,
// not null and not an empty array.
func hasToolCalls(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && string(trimmed) != "null" && string(trimmed) != "[]"
}

// decodeStreamError reads the error member, which is an object for OpenAI and
// vLLM and a bare string for some gateways.
func decodeStreamError(raw json.RawMessage) *StreamError {
	var object struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		var message string
		if json.Unmarshal(raw, &message) != nil {
			message = string(raw)
		}
		return &StreamError{Message: message}
	}
	streamError := &StreamError{Message: object.Message, Type: object.Type}
	if object.Code != nil {
		streamError.Code = fmt.Sprint(object.Code)
	}
	return streamError
}

// truncateForError keeps a malformed payload readable in an error message.
func truncateForError(payload []byte) string {
	const limit = 120
	if len(payload) > limit {
		return string(payload[:limit]) + "..."
	}
	return string(payload)
}
