package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const anthropicAPI = "https://api.anthropic.com/v1/messages"

// AnthropicClient implements Client for the Anthropic Messages API.
type AnthropicClient struct {
	APIKey     string
	HTTPClient *http.Client
}

// NewAnthropic creates a client for the Anthropic API.
func NewAnthropic(apiKey string) *AnthropicClient {
	return &AnthropicClient{
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
}

type anthropicReq struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []anthropicMsg `json:"messages"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *AnthropicClient) Complete(ctx context.Context, req *Request) (*Response, error) {
	// Separate system message from conversation
	var system string
	var msgs []anthropicMsg
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
		} else {
			msgs = append(msgs, anthropicMsg{Role: m.Role, Content: m.Content})
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := anthropicReq{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  msgs,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPI, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http call: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api error %d: %s", httpResp.StatusCode, string(respBody))
	}

	var ar anthropicResp
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var content string
	for _, c := range ar.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}

	return &Response{
		Content:    content,
		StopReason: ar.StopReason,
		TokensIn:   ar.Usage.InputTokens,
		TokensOut:  ar.Usage.OutputTokens,
	}, nil
}
