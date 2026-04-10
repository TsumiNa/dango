package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/tsumina/dango/internal/logging"
)

// Request describes one LLM completion request.
type Request struct {
	SystemPrompt string
	UserPrompt   string
	Temperature  float64
}

// Client generates structured responses from an LLM provider.
type Client interface {
	CompleteJSON(ctx context.Context, request Request) ([]byte, error)
}

// Config configures the OpenAI-compatible chat client.
type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature float64
}

type openAICompatibleClient struct {
	httpClient  *http.Client
	baseURL     string
	apiKey      string
	model       string
	temperature float64
	logger      *slog.Logger
}

type disabledClient struct {
	reason string
}

type openAIChatRequest struct {
	Model          string              `json:"model"`
	Messages       []openAIChatMessage `json:"messages"`
	Temperature    float64             `json:"temperature,omitempty"`
	ResponseFormat any                 `json:"response_format,omitempty"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// NewOpenAICompatibleFromEnv constructs an OpenAI-compatible client using env
// fallbacks for base URL and API key.
func NewOpenAICompatibleFromEnv(model string, logger *slog.Logger) Client {
	baseURL := firstNonEmpty(
		strings.TrimSpace(os.Getenv("DANGO_LLM_BASE_URL")),
		strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		"https://api.openai.com/v1",
	)
	apiKey := firstNonEmpty(
		strings.TrimSpace(os.Getenv("DANGO_LLM_API_KEY")),
		strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
	)
	model = firstNonEmpty(
		strings.TrimSpace(model),
		strings.TrimSpace(os.Getenv("DANGO_LLM_MODEL")),
		strings.TrimSpace(os.Getenv("OPENAI_MODEL")),
	)

	if model == "" {
		return disabledClient{reason: "planner LLM model is not configured"}
	}
	if apiKey == "" {
		return disabledClient{reason: "planner LLM API key is not configured"}
	}

	return NewOpenAICompatible(Config{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		Model:       model,
		Temperature: 0.1,
	}, logger)
}

// NewOpenAICompatible constructs an OpenAI-compatible JSON completion client.
func NewOpenAICompatible(config Config, logger *slog.Logger) Client {
	if strings.TrimSpace(config.Model) == "" {
		return disabledClient{reason: "planner LLM model is not configured"}
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return disabledClient{reason: "planner LLM API key is not configured"}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	temperature := config.Temperature
	if temperature == 0 {
		temperature = 0.1
	}

	return &openAICompatibleClient{
		httpClient:  http.DefaultClient,
		baseURL:     baseURL,
		apiKey:      strings.TrimSpace(config.APIKey),
		model:       strings.TrimSpace(config.Model),
		temperature: temperature,
		logger:      logging.Component(logger, "llm.openai_compatible"),
	}
}

func (c disabledClient) CompleteJSON(context.Context, Request) ([]byte, error) {
	return nil, errors.New(c.reason)
}

// CompleteJSON requests a JSON object from an OpenAI-compatible chat endpoint.
func (c *openAICompatibleClient) CompleteJSON(ctx context.Context, request Request) ([]byte, error) {
	payload, err := json.Marshal(openAIChatRequest{
		Model: c.model,
		Messages: []openAIChatMessage{
			{Role: "system", Content: request.SystemPrompt},
			{Role: "user", Content: request.UserPrompt},
		},
		Temperature: c.temperature,
		ResponseFormat: map[string]string{
			"type": "json_object",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal llm request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build llm request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("send llm request: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read llm response: %w", err)
	}

	var parsed openAIChatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode llm response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
			return nil, fmt.Errorf("llm request failed: %s", strings.TrimSpace(parsed.Error.Message))
		}
		return nil, fmt.Errorf("llm request failed with status %s", response.Status)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("llm response did not include any choices")
	}

	content := stripMarkdownCodeFence(parsed.Choices[0].Message.Content)
	if !json.Valid([]byte(content)) {
		return nil, fmt.Errorf("llm response was not valid JSON")
	}
	return []byte(content), nil
}

func stripMarkdownCodeFence(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "```") {
		return value
	}
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "json") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "json"))
	}
	value = strings.TrimSuffix(value, "```")
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
