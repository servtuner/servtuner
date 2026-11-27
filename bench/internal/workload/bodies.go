package workload

// Request body shapes. The embedded sampling struct flattens into the JSON
// object, so max_tokens and the streaming fields sit beside model.

// streamOptions asks the server for a final chunk carrying usage, which the
// calibrator and the token accounting need.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type sampling struct {
	MaxTokens     int            `json:"max_tokens"`
	MinTokens     *int           `json:"min_tokens,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatBody struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	sampling
}

type completionBody struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	sampling
}

type embeddingsBody struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// sampling builds the output-length and streaming fields. max_tokens always
// carries the sampled output target; min_tokens is best-effort (Options).
func (g *Generator) sampling(outputTokens int) sampling {
	fields := sampling{MaxTokens: outputTokens, Stream: g.streaming}
	if g.options.SetMinTokens {
		fields.MinTokens = &outputTokens
	}
	if g.streaming {
		fields.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return fields
}
