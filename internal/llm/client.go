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

// Request describes one structured completion request sent through a
// repository-owned LLM client.
//
// Callers typically build Request after rendering a system prompt from package
// prompts and deciding on a short user instruction that asks for JSON output.
type Request struct {
	// SystemPrompt contains the main instruction set and structured context.
	SystemPrompt string
	// UserPrompt contains the final imperative user message sent after the system prompt.
	UserPrompt string
	// Temperature requests the sampling temperature for the completion.
	Temperature float64
}

// Client generates structured responses from an LLM provider.
//
// Implementations are expected to be safe for reuse across concurrent planning
// and execution flows.
type Client interface {
	// CompleteJSON submits request and returns the raw JSON payload selected by
	// the model.
	CompleteJSON(ctx context.Context, request Request) ([]byte, error)
}

// Config configures the OpenAI-compatible chat client used by the built-in AI
// paths.
//
// The same structure is used for orchestrator intent understanding, runner
// planning and review, and executor-side built-in AI generation.
type Config struct {
	// BaseURL is the OpenAI-compatible API root without a trailing slash.
	BaseURL string
	// APIKey is the bearer token sent to the upstream provider.
	APIKey string
	// Model is the model identifier sent in the chat completion request.
	Model string
	// Temperature is the default sampling temperature used for each request.
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

// NewOpenAICompatibleFromEnv constructs an OpenAI-compatible client from the
// repository's environment-variable conventions.
//
// The lookup order is DANGO-prefixed variables first, then OPENAI-prefixed
// variables, and finally hard-coded defaults for the base URL. When model or
// API key configuration is missing, the returned Client is a disabled client
// that fails calls with an explanatory error so higher-level planning and
// orchestration code can keep a uniform dependency shape without nil checks.
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
//
// The client always asks the upstream provider for JSON object output and is
// the main transport used by the built-in intent, planning, review, repair,
// and executor generation paths. It also normalizes base URL and temperature
// defaults so callers can pass partial configuration.
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
//
// The method converts Request into a two-message chat completion call, strips
// any surrounding Markdown code fence from the chosen answer, and validates
// that the resulting payload is well-formed JSON before returning it.
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
