package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/tsumina/dango/internal/logging"
)

// Request describes one structured completion request sent through a
// repository-owned LLM client.
//
// Callers typically build Request after rendering a system prompt from package
// prompts and deciding on a short user instruction that asks for JSON output.
// Set PreviousResponseID to continue a multi-turn conversation using the
// OpenAI Responses API.
type Request struct {
	// SystemPrompt contains the main instruction set and structured context.
	SystemPrompt string
	// UserPrompt contains the final imperative user message sent after the system prompt.
	UserPrompt string
	// Temperature requests the sampling temperature for the completion.
	Temperature float64
	// PreviousResponseID carries the response ID returned by a prior CompleteJSON
	// call. When non-empty the upstream Responses API continues the existing
	// conversation so the model retains context from that previous turn.
	PreviousResponseID string
}

// Client generates structured responses from an LLM provider.
//
// Implementations are expected to be safe for reuse across concurrent planning
// and execution flows.
type Client interface {
	// CompleteJSON submits request and returns the raw JSON payload selected by
	// the model together with the response ID of the completed turn. The
	// response ID can be passed in a subsequent Request.PreviousResponseID to
	// continue a multi-turn conversation.
	CompleteJSON(ctx context.Context, request Request) ([]byte, string, error)
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

// ToolCatalogEntry describes one registered tool exposed to planning prompts.
type ToolCatalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	InputTypes  []string `json:"input_types"`
	OutputTypes []string `json:"output_types"`
	Model       string   `json:"model,omitempty"`
}

type openAICompatibleClient struct {
	sdkClient   *openai.Client
	model       string
	temperature float64
	logger      *slog.Logger
}

// NewOpenAICompatibleFromEnv constructs an OpenAI-compatible client from the
// repository's environment-variable conventions.
//
// The lookup order is DANGO-prefixed variables first, then OPENAI-prefixed
// variables, and finally hard-coded defaults for the base URL. When model or
// API key configuration is missing, the function returns nil and leaves it to
// higher-level planning or orchestration code to report that built-in AI is not
// configured.
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
		return nil
	}
	if apiKey == "" {
		return nil
	}

	return NewOpenAICompatible(Config{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		Model:       model,
		Temperature: 0.1,
	}, logger)
}

// NewOpenAICompatible constructs an OpenAI-compatible JSON completion client
// backed by the official openai-go/v3 SDK using the Responses API.
//
// The client always asks the upstream provider for JSON object output and is
// the main transport used by the built-in intent, planning, review, repair,
// and executor generation paths. It also normalizes base URL and temperature
// defaults so callers can pass partial configuration. When required config is
// missing, NewOpenAICompatible returns nil.
func NewOpenAICompatible(config Config, logger *slog.Logger) Client {
	if strings.TrimSpace(config.Model) == "" {
		return nil
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	temperature := config.Temperature
	if temperature == 0 {
		temperature = 0.1
	}

	sdkClient := openai.NewClient(
		option.WithAPIKey(strings.TrimSpace(config.APIKey)),
		option.WithBaseURL(baseURL),
	)

	return &openAICompatibleClient{
		sdkClient:   &sdkClient,
		model:       strings.TrimSpace(config.Model),
		temperature: temperature,
		logger:      logging.Component(logger, "llm.openai_compatible"),
	}
}

// CompleteJSON requests a JSON object via the OpenAI Responses API.
//
// The method maps Request into a Responses API call: SystemPrompt becomes the
// Instructions field, UserPrompt is the user-role input message, and a
// non-empty PreviousResponseID continues an existing multi-turn conversation.
// The returned string is the response ID, which callers can pass back in
// Request.PreviousResponseID to chain a follow-up turn.
func (c *openAICompatibleClient) CompleteJSON(ctx context.Context, request Request) ([]byte, string, error) {
	jsonFmt := shared.NewResponseFormatJSONObjectParam()
	params := responses.ResponseNewParams{
		Model: c.model,
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONObject: &jsonFmt,
			},
		},
		Temperature: param.NewOpt(c.temperature),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: param.NewOpt(request.UserPrompt),
		},
	}
	if request.SystemPrompt != "" {
		params.Instructions = param.NewOpt(request.SystemPrompt)
	}
	if request.PreviousResponseID != "" {
		params.PreviousResponseID = param.NewOpt(request.PreviousResponseID)
	}

	response, err := c.sdkClient.Responses.New(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("llm request failed: %w", err)
	}

	content := stripMarkdownCodeFence(response.OutputText())
	if !json.Valid([]byte(content)) {
		return nil, "", fmt.Errorf("llm response was not valid JSON")
	}
	return []byte(content), response.ID, nil
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
