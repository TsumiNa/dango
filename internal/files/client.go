package llm

import "context"

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`    // "system", "user", "assistant"
	Content string `json:"content"`
}

// Request holds parameters for an LLM completion call.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
}

// Response holds the LLM's reply.
type Response struct {
	Content    string `json:"content"`
	StopReason string `json:"stop_reason,omitempty"`
	TokensIn   int    `json:"tokens_in,omitempty"`
	TokensOut  int    `json:"tokens_out,omitempty"`
}

// Client is the minimal interface for any LLM provider.
type Client interface {
	Complete(ctx context.Context, req *Request) (*Response, error)
}
