package main

import (
	"encoding/json"
	"io"
	"strings"
)

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	MaxTokens           int           `json:"max_tokens"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	Stream              bool          `json:"stream"`
	StreamOptions       struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

var chatEndpoint = endpoint{
	path:         chatCompletionsPath,
	idPrefix:     "chatcmpl",
	decode:       decodeChatRequest,
	completeBody: chatCompleteBody,
	streamChunk:  chatStreamChunk,
}

func decodeChatRequest(body io.Reader) (inferenceRequest, error) {
	var req chatRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return inferenceRequest{}, err
	}
	maxTokens := req.MaxCompletionTokens
	if maxTokens == 0 {
		maxTokens = req.MaxTokens
	}
	promptTokens := 0
	for _, message := range req.Messages {
		promptTokens += countWords(messageText(message.Content))
	}
	return inferenceRequest{
		promptTokens: promptTokens, maxTokens: maxTokens,
		stream: req.Stream, includeUsage: req.StreamOptions.IncludeUsage,
	}, nil
}

// messageText flattens string content and the array-of-parts form.
func messageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, " ")
}

func chatCompleteBody(plan completionPlan, model, text string) map[string]any {
	return map[string]any{
		"id": plan.id, "object": "chat.completion", "created": plan.created, "model": model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": plan.finishReason,
		}},
		"usage": plan.usage(),
	}
}

func chatStreamChunk(plan completionPlan, model string, event streamEvent) map[string]any {
	chunk := map[string]any{
		"id": plan.id, "object": "chat.completion.chunk", "created": plan.created, "model": model,
		"choices": []map[string]any{},
	}
	choice := func(delta map[string]any, finishReason any) {
		chunk["choices"] = []map[string]any{{"index": 0, "delta": delta, "finish_reason": finishReason}}
	}
	switch event.kind {
	case openingEvent:
		choice(map[string]any{"role": "assistant", "content": ""}, nil)
	case tokenEvent:
		delta := map[string]any{"content": contentToken}
		if event.isFirstChunk {
			delta["role"] = "assistant"
		}
		choice(delta, nil)
	case finishEvent:
		choice(map[string]any{}, plan.finishReason)
	case usageEvent:
		chunk["usage"] = event.usage
	}
	return chunk
}
