package main

import (
	"encoding/json"
	"errors"
	"io"
)

// completionsRequest is the OpenAI legacy completions body. min_tokens and ignore_eos are accepted but, as
// on the chat path, not acted on: the length comes from OutputTokens or max_tokens.
type completionsRequest struct {
	Model         string          `json:"model"`
	Prompt        json.RawMessage `json:"prompt"`
	MaxTokens     int             `json:"max_tokens"`
	Stream        bool            `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

var completionsEndpoint = endpoint{
	path:         completionsPath,
	idPrefix:     "cmpl",
	decode:       decodeCompletionsRequest,
	completeBody: completionsCompleteBody,
	streamChunk:  completionsStreamChunk,
}

func decodeCompletionsRequest(body io.Reader) (inferenceRequest, error) {
	var req completionsRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return inferenceRequest{}, err
	}
	prompt, err := firstPrompt(req.Prompt)
	if err != nil {
		return inferenceRequest{}, err
	}
	return inferenceRequest{
		promptTokens: countWords(prompt), maxTokens: req.MaxTokens,
		stream: req.Stream, includeUsage: req.StreamOptions.IncludeUsage,
	}, nil
}

// firstPrompt returns the prompt string. The API also allows an array of prompts (a batch); the mock serves
// only the first and ignores the rest, since it generates one choice per request. A missing or null prompt
// is empty, as an empty messages list is on the chat path.
func firstPrompt(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single, nil
	}
	var batch []string
	if json.Unmarshal(raw, &batch) != nil {
		return "", errors.New("prompt must be a string or an array of strings")
	}
	if len(batch) == 0 {
		return "", nil
	}
	return batch[0], nil
}

func completionsCompleteBody(plan completionPlan, model, text string) map[string]any {
	return map[string]any{
		"id": plan.id, "object": "text_completion", "created": plan.created, "model": model,
		"choices": []map[string]any{{
			"index": 0, "text": text, "logprobs": nil, "finish_reason": plan.finishReason,
		}},
		"usage": plan.usage(),
	}
}

// completionsStreamChunk has no role, so the opening chunk of SendRoleOnlyFirstChunk is an empty-text
// delta, which the bench SSE parser does not count as a token.
func completionsStreamChunk(plan completionPlan, model string, event streamEvent) map[string]any {
	chunk := map[string]any{
		"id": plan.id, "object": "text_completion", "created": plan.created, "model": model,
		"choices": []map[string]any{},
	}
	choice := func(text string, finishReason any) {
		chunk["choices"] = []map[string]any{{"index": 0, "text": text, "logprobs": nil, "finish_reason": finishReason}}
	}
	switch event.kind {
	case openingEvent:
		choice("", nil)
	case tokenEvent:
		choice(contentToken, nil)
	case finishEvent:
		choice("", plan.finishReason)
	case usageEvent:
		chunk["usage"] = event.usage
	}
	return chunk
}
