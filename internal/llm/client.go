package llm

import (
	"context"
	"encoding/json"
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
// [Client.Stream] forwards to its consumer. See
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

// Response holds the parsed result of a [Client.Send] call.
type Response struct {
	// Text is the concatenated output_text produced by the model. Empty
	// when the model only emitted tool calls.
	Text string
	// ToolCalls lists the function_call items the model emitted in this
	// turn, preserving their original order.
	ToolCalls []ToolCall
	// Usage is the token usage reported by the provider for this request.
	Usage TokenUsage
	// Raw is the underlying SDK response, exposed for provider-specific
	// fields that do not have a typed accessor on [Response].
	Raw *responses.Response
}

// NewConversation is a convenience wrapper that constructs a
// [Conversation] anchored on instructions and tools. It is equivalent to
// [NewConversation] and is provided on [Client] so callers can discover
// the paired API.
func (c *Client) NewConversation(instructions string, tools []ToolSpec) *Conversation {
	return NewConversation(instructions, tools)
}

// Send issues one turn against the Responses API using conv's current
// state. The request's prefix (instructions, tool schema, and already
// recorded turns) is serialized in a stable order so the provider's prompt
// cache can hit across iterations. On success the model's reply is
// appended to conv - assistant text via [Conversation.AppendAssistantText]
// and each function call via [Conversation.AppendToolCall] - token usage
// is recorded, and the parsed [Response] is returned. Tool execution is
// the caller's responsibility; supply the outputs via
// [Conversation.AppendToolOutput] before the next Send.
func (c *Client) Send(ctx context.Context, conv *Conversation) (*Response, error) {
	if conv == nil {
		return nil, fmt.Errorf("llm: Send requires a non-nil conversation")
	}
	params := c.buildRequestParams(conv)

	resp, err := c.raw.Responses.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return c.applyResponseOutput(ctx, conv, resp), nil
}

// applyResponseOutput appends the model's output items from resp to
// conv and records token usage, returning a [Response] view suitable
// for callers of [Client.Send]. It is shared with the streaming
// commit path so the post-request conversation state is identical
// regardless of how the response was delivered.
func (c *Client) applyResponseOutput(ctx context.Context, conv *Conversation, resp *responses.Response) *Response {
	out := &Response{
		Usage: usageFromResponse(resp.Usage),
		Raw:   resp,
	}
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			msg := item.AsMessage()
			var text string
			for _, part := range msg.Content {
				if part.Type == "output_text" {
					text += part.Text
				}
			}
			if text != "" {
				out.Text += text
				conv.AppendAssistantText(text)
			}
		case "function_call":
			call := item.AsFunctionCall()
			tc := ToolCall{
				CallID:    call.CallID,
				Name:      call.Name,
				Arguments: call.Arguments,
			}
			out.ToolCalls = append(out.ToolCalls, tc)
			conv.AppendToolCall(tc)
		case "reasoning":
			// Capture the model's chain-of-thought for observability.
			// Summary is the redacted public summary; Content is the
			// full reasoning_text when the provider emits it. When
			// ReplayReasoning is enabled the full item (including
			// encrypted_content when the provider returned it) is
			// stored on the turn so buildResponseInput can replay it.
			r := item.AsReasoning()
			var buf []string
			for _, s := range r.Summary {
				if s.Text != "" {
					buf = append(buf, s.Text)
				}
			}
			for _, part := range r.Content {
				if part.Text != "" {
					buf = append(buf, part.Text)
				}
			}
			text := strings.Join(buf, "\n")
			var raw json.RawMessage
			if c.replayReasoning {
				// Round-trip through ResponseReasoningItemParam so the
				// bytes stored on the turn are known to decode cleanly
				// on replay. Any failure here means the SDK's output
				// and input shapes diverged for this item; drop raw so
				// the turn stays observability-only rather than
				// silently disabling replay later in buildResponseInput.
				if b, err := json.Marshal(r); err == nil {
					var probe responses.ResponseReasoningItemParam
					if json.Unmarshal(b, &probe) == nil {
						raw = b
					}
				}
			}
			if text != "" || len(raw) > 0 {
				conv.AppendReasoning(text, raw)
			}
		}
	}
	// Auto-shrink is best-effort: when a registered Summarizer fails,
	// recordUsage has already fallen back to Trim so the next Send still
	// fits in context.
	_ = conv.recordUsage(ctx, out.Usage)
	return out
}

// buildRequestParams assembles the Responses API request body from
// conv's current state and the client's configuration. It is shared
// by the non-streaming and streaming request paths so both endpoints
// see exactly the same prefix and include list.
func (c *Client) buildRequestParams(conv *Conversation) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: c.model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: buildResponseInput(conv.Turns())},
		Tools: buildToolParams(conv.Tools()),
	}
	if instr := conv.Instructions(); instr != "" {
		params.Instructions = openai.String(instr)
	}
	c.applyReasoning(&params)
	if c.replayReasoning {
		params.Include = append(params.Include,
			responses.ResponseIncludableReasoningEncryptedContent)
	}
	return params
}

// buildResponseInput translates recorded [Turn]s into the Responses API
// input item list. Ordering is preserved verbatim so prompt-cache-sensitive
// prefixes stay stable across consecutive Send calls.
//
// Reasoning turns are replayed when they carry a provider-opaque Raw
// payload and sit after the most recent user turn (i.e., inside the
// currently open tool-calling chain). Reasoning items produced before
// the latest user turn belong to a closed conversation cycle and are
// skipped to avoid bloating the request prefix and breaking the
// prompt cache.
func buildResponseInput(turns []Turn) responses.ResponseInputParam {
	lastUser := -1
	for i, t := range turns {
		if t.Role == RoleUser {
			lastUser = i
		}
	}
	input := make(responses.ResponseInputParam, 0, len(turns))
	for i, t := range turns {
		switch t.Role {
		case RoleUser:
			input = append(input, responses.ResponseInputItemParamOfMessage(
				t.Text, responses.EasyInputMessageRoleUser))
		case RoleAssistant:
			input = append(input, responses.ResponseInputItemParamOfMessage(
				t.Text, responses.EasyInputMessageRoleAssistant))
		case RoleReasoning:
			if len(t.Raw) == 0 || i <= lastUser {
				continue
			}
			var item responses.ResponseReasoningItemParam
			if err := json.Unmarshal(t.Raw, &item); err != nil {
				continue
			}
			input = append(input, responses.ResponseInputItemUnionParam{
				OfReasoning: &item,
			})
		case RoleToolCall:
			if t.Tool == nil {
				continue
			}
			callParam := responses.ResponseFunctionToolCallParam{
				CallID:    t.Tool.CallID,
				Name:      t.Tool.Name,
				Arguments: t.Tool.Arguments,
			}
			input = append(input, responses.ResponseInputItemUnionParam{OfFunctionCall: &callParam})
		case RoleToolOutput:
			if t.Tool == nil {
				continue
			}
			input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(
				t.Tool.CallID, t.Tool.Output))
		}
	}
	return input
}

// buildToolParams translates [ToolSpec]s into the Responses API tool union.
func buildToolParams(tools []ToolSpec) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		ft := &responses.FunctionToolParam{
			Name:       t.Name,
			Parameters: t.Parameters,
		}
		if t.Description != "" {
			ft.Description = openai.String(t.Description)
		}
		out = append(out, responses.ToolUnionParam{OfFunction: ft})
	}
	return out
}

// usageFromResponse copies the SDK's ResponseUsage into the local
// [TokenUsage] type.
func usageFromResponse(u responses.ResponseUsage) TokenUsage {
	return TokenUsage{
		Input:     int(u.InputTokens),
		Cached:    int(u.InputTokensDetails.CachedTokens),
		Output:    int(u.OutputTokens),
		Reasoning: int(u.OutputTokensDetails.ReasoningTokens),
		Total:     int(u.TotalTokens),
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
	// [Client.Send] requests reasoning.encrypted_content from the
	// provider, stores the full reasoning item on each [Turn], and
	// replays those items on subsequent requests that still belong to
	// the same user turn. Defaults to false so non-reasoning models
	// and debug-only setups keep the Phase 1 observability-only
	// behavior.
	ReplayReasoning bool
	// StreamCategories selects which kinds of incremental fragments
	// [Client.Stream] forwards to its consumer. The zero value means
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
