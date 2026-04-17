package llm

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// Provider identifies which upstream LLM service a Client is configured for.
type Provider string

const (
	ProviderOpenAI     Provider = "openai"
	ProviderOpenRouter Provider = "openrouter"
	ProviderGemini     Provider = "gemini"
)

// baseURL returns the Responses API base URL for the provider. An empty string
// means the SDK default (OpenAI) should be used.
func (p Provider) baseURL() string {
	switch p {
	case ProviderOpenRouter:
		return "https://openrouter.ai/api/v1/"
	case ProviderGemini:
		return "https://generativelanguage.googleapis.com/v1beta/openai/"
	default:
		return ""
	}
}

// Client is an LLM client bound to a specific provider and orchestration model.
//
// The zero value is not usable; construct one with NewClientFromEnv. Client is
// safe for concurrent use by multiple goroutines.
type Client struct {
	provider Provider
	model    string
	raw      openai.Client
}

// Provider returns the provider this client is bound to.
func (c *Client) Provider() Provider { return c.provider }

// Model returns the orchestration model identifier.
func (c *Client) Model() string { return c.model }

// Raw exposes the underlying OpenAI SDK client for advanced use cases.
func (c *Client) Raw() *openai.Client { return &c.raw }

// Respond issues a single-turn request against the Responses API using the
// configured model and returns the concatenated output text.
func (c *Client) Respond(ctx context.Context, input string) (string, error) {
	resp, err := c.raw.Responses.New(ctx, responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(input)},
		Model: c.model,
	})
	if err != nil {
		return "", err
	}
	return resp.OutputText(), nil
}

// String returns a human-readable description of the client.
func (c *Client) String() string {
	return fmt.Sprintf("llm.Client{provider=%s, model=%s}", c.provider, c.model)
}

// ErrNoAPIKey is returned when no supported *_API_KEY variable is set.
var ErrNoAPIKey = errors.New("llm: no supported API key found (OPENAI_API_KEY, OPENROUTER_API_KEY, GEMINI_API_KEY)")

// ErrNoModel is returned when ORCHESTRATION_MODEL is not set.
var ErrNoModel = errors.New("llm: ORCHESTRATION_MODEL environment variable is not set")

// NewClientFromEnv constructs a Client from the environment.
//
// If a .env file is present in the current working directory, its values are
// loaded into the process environment without overriding existing variables.
// The first matching API key in the order OPENAI, OPENROUTER, GEMINI selects
// the provider and base URL. The ORCHESTRATION_MODEL variable must be set.
func NewClientFromEnv() (*Client, error) {
	_ = godotenv.Load()

	provider, apiKey, ok := detectProvider()
	if !ok {
		return nil, ErrNoAPIKey
	}

	model := os.Getenv("ORCHESTRATION_MODEL")
	if model == "" {
		return nil, ErrNoModel
	}

	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if base := provider.baseURL(); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}

	return &Client{
		provider: provider,
		model:    model,
		raw:      openai.NewClient(opts...),
	}, nil
}

// detectProvider inspects well-known environment variables and returns the
// first provider that has a non-empty API key configured.
func detectProvider() (Provider, string, bool) {
	candidates := []struct {
		provider Provider
		envVar   string
	}{
		{ProviderOpenAI, "OPENAI_API_KEY"},
		{ProviderOpenRouter, "OPENROUTER_API_KEY"},
		{ProviderGemini, "GEMINI_API_KEY"},
	}
	for _, c := range candidates {
		if v := os.Getenv(c.envVar); v != "" {
			return c.provider, v, true
		}
	}
	return "", "", false
}
