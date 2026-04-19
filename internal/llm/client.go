package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/joho/godotenv"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// ReasoningEffort constrains how much a reasoning-capable model thinks
// before producing its next response. The empty value leaves the
// provider default untouched.
//
// Supported values mirror the OpenAI Responses API and are forwarded
// verbatim; the Client does not validate them so newly introduced
// levels work without a code change.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
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
// The zero value is not usable; construct one with [NewClient] or
// [NewClientFromEnv]. Client is safe for concurrent use by multiple
// goroutines.
type Client struct {
	provider         Provider
	model            string
	raw              openai.Client
	reasoningEffort  ReasoningEffort
	replayReasoning  bool
	streamCategories StreamCategory
}

// Provider returns the provider this client is bound to.
func (c *Client) Provider() Provider { return c.provider }

// Model returns the orchestration model identifier.
func (c *Client) Model() string { return c.model }

// Raw exposes the underlying OpenAI SDK client for advanced use cases.
func (c *Client) Raw() *openai.Client { return &c.raw }

// ReasoningEffort returns the reasoning-effort level applied to every
// request this client issues, or an empty string when the provider
// default should be used.
func (c *Client) ReasoningEffort() ReasoningEffort { return c.reasoningEffort }

// ReplayReasoning reports whether this client captures and replays
// reasoning items to preserve tool-calling continuity on reasoning
// models. See [ClientConfig.ReplayReasoning] for details.
func (c *Client) ReplayReasoning() bool { return c.replayReasoning }

// StreamCategories reports which kinds of incremental fragments
// [Conversation.Stream] forwards to its consumer. See
// [ClientConfig.StreamCategories] for details.
func (c *Client) StreamCategories() StreamCategory { return c.streamCategories }

// Respond issues a single-turn request against the Responses API using the
// configured model and returns the concatenated output text.
func (c *Client) Respond(ctx context.Context, input string) (string, error) {
	params := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(input)},
		Model: c.model,
	}
	c.applyReasoning(&params)
	resp, err := c.raw.Responses.New(ctx, params)
	if err != nil {
		return "", err
	}
	return resp.OutputText(), nil
}

// applyReasoning mutates params to carry the configured reasoning
// effort when one is set. It is a no-op otherwise so non-reasoning
// models receive an unchanged request body.
func (c *Client) applyReasoning(params *responses.ResponseNewParams) {
	if c.reasoningEffort == "" {
		return
	}
	params.Reasoning = shared.ReasoningParam{
		Effort: shared.ReasoningEffort(c.reasoningEffort),
	}
}

// String returns a human-readable description of the client.
func (c *Client) String() string {
	return fmt.Sprintf("llm.Client{provider=%s, model=%s}", c.provider, c.model)
}

// ErrNoAPIKey is returned when no supported *_API_KEY variable is set.
var ErrNoAPIKey = errors.New("llm: no supported API key found (OPENAI_API_KEY, OPENROUTER_API_KEY, GEMINI_API_KEY)")

// ErrNoModel is returned when ORCHESTRATION_MODEL is not set.
var ErrNoModel = errors.New("llm: ORCHESTRATION_MODEL environment variable is not set")

// ClientConfig carries all the knobs used to construct a [Client]. New
// extension points (for example per-client timeouts, temperature
// defaults, or observability hooks) should be added as additional
// fields on this struct rather than as positional arguments on
// [NewClient], so call sites continue to compile when the set grows.
//
// Provider and Model are required. Raw is required when the caller
// already holds a preconfigured SDK client (common in tests); when Raw
// is the zero value, [NewClient] returns an error - use
// [NewClientFromEnv] for the env-driven construction path.
type ClientConfig struct {
	// Provider identifies the upstream service.
	Provider Provider
	// Model is the orchestration model identifier.
	Model string
	// Raw is the preconfigured OpenAI SDK client to wrap.
	Raw openai.Client
	// ReasoningEffort, when non-empty, is forwarded on every request
	// so reasoning-capable models think at the requested level.
	ReasoningEffort ReasoningEffort
	// ReplayReasoning enables capture and replay of reasoning items to
	// preserve tool-calling continuity on reasoning models. When true,
	// [Conversation.Send] requests reasoning.encrypted_content from the
	// provider, stores the full reasoning item on each [Turn], and
	// replays those items on subsequent requests that still belong to
	// the same user turn. Defaults to false so non-reasoning models
	// and debug-only setups keep the Phase 1 observability-only
	// behavior.
	ReplayReasoning bool
	// StreamCategories selects which kinds of incremental fragments
	// [Conversation.Stream] forwards to its consumer. The zero value means
	// "use the default set" ([DefaultStreamCategories], currently
	// text + reasoning). To stream only one kind, set this to that
	// flag explicitly (for example [StreamText] alone). Filtering
	// affects only what crosses the channel; the final committed
	// conversation state is unaffected.
	StreamCategories StreamCategory
}

// NewClient wraps an already-constructed openai SDK client using cfg.
//
// NewClient is useful when callers need to supply a pre-configured SDK
// client (for example, to route requests through a custom HTTP
// transport or a test server). Required fields are validated and an
// error is returned when any of them is missing.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Provider == "" {
		return nil, fmt.Errorf("llm: NewClient requires a provider")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm: NewClient requires a model")
	}
	if reflect.DeepEqual(cfg.Raw, openai.Client{}) {
		return nil, fmt.Errorf("llm: NewClient requires a configured Raw SDK client")
	}
	return &Client{
		provider:         cfg.Provider,
		model:            cfg.Model,
		raw:              cfg.Raw,
		reasoningEffort:  cfg.ReasoningEffort,
		replayReasoning:  cfg.ReplayReasoning,
		streamCategories: resolveStreamCategories(cfg.StreamCategories),
	}, nil
}

// NewClientFromEnv constructs a Client from the environment.
//
// If a .env file is present in the current working directory, its values are
// loaded into the process environment without overriding existing variables.
// The first matching API key in the order OPENAI, OPENROUTER, GEMINI selects
// the provider and base URL. The ORCHESTRATION_MODEL variable must be set.
// REASONING_EFFORT is optional; when set, its value is forwarded verbatim
// as the reasoning effort (expected values: none, minimal, low, medium,
// high, xhigh). REASONING_REPLAY, when set to a truthy value ("1",
// "true", "yes", or "on"; case- and surrounding-whitespace-insensitive),
// enables reasoning replay for tool-calling continuity.
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
		provider:         provider,
		model:            model,
		raw:              openai.NewClient(opts...),
		reasoningEffort:  ReasoningEffort(os.Getenv("REASONING_EFFORT")),
		replayReasoning:  parseBoolEnv(os.Getenv("REASONING_REPLAY")),
		streamCategories: resolveStreamCategories(0),
	}, nil
}

// parseBoolEnv reports whether s names a truthy boolean environment
// value. Empty and unknown strings are treated as false.
func parseBoolEnv(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
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
