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
	input := buildResponseInput(conv.Turns())
	params := responses.ResponseNewParams{
		Model: c.model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Tools: buildToolParams(conv.Tools()),
	}
	if instr := conv.Instructions(); instr != "" {
		params.Instructions = openai.String(instr)
	}

	resp, err := c.raw.Responses.New(ctx, params)
	if err != nil {
		return nil, err
	}

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
		}
	}
	conv.recordUsage(out.Usage)
	return out, nil
}

// buildResponseInput translates recorded [Turn]s into the Responses API
// input item list. Ordering is preserved verbatim so prompt-cache-sensitive
// prefixes stay stable across consecutive Send calls.
func buildResponseInput(turns []Turn) responses.ResponseInputParam {
	input := make(responses.ResponseInputParam, 0, len(turns))
	for _, t := range turns {
		switch t.Role {
		case RoleUser:
			input = append(input, responses.ResponseInputItemParamOfMessage(
				t.Text, responses.EasyInputMessageRoleUser))
		case RoleAssistant:
			input = append(input, responses.ResponseInputItemParamOfMessage(
				t.Text, responses.EasyInputMessageRoleAssistant))
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

// NewClient wraps an already-constructed openai SDK client.
//
// NewClient is useful when callers need to supply a pre-configured SDK client
// (for example, to route requests through a custom HTTP transport or a test
// server). provider and model are stored on the returned Client and returned
// by [Client.Provider] and [Client.Model].
func NewClient(provider Provider, model string, raw openai.Client) *Client {
	return &Client{provider: provider, model: model, raw: raw}
}

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
