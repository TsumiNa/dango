package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Response holds the parsed result of a [Conversation.Send] call.
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

// ErrNoClient is returned by [Conversation.Send] and
// [Conversation.Stream] when the conversation was constructed without a
// [Client]. Pure-history conversations (constructed with a nil client)
// support local mutations and JSON round-trips but cannot issue LLM
// requests.
var ErrNoClient = fmt.Errorf("llm: conversation has no client")

// Send issues one turn against the Responses API using the
// conversation's current state. The request's prefix (instructions, tool
// schema, and already recorded turns) is serialized in a stable order so
// the provider's prompt cache can hit across iterations. On success the
// model's reply is appended to the conversation - assistant text via
// [Conversation.AppendAssistantText] and each function call via
// [Conversation.AppendToolCall] - token usage is recorded, and the
// parsed [Response] is returned. Tool execution is the caller's
// responsibility; supply the outputs via
// [Conversation.AppendToolOutput] before the next Send.
//
// effort overrides the reasoning-effort level for this request only.
// Pass an empty string to fall back to the level configured on the
// bound [Client]; any non-empty value is forwarded verbatim on the
// request body and leaves the client's default untouched.
func (c *Conversation) Send(ctx context.Context, effort ReasoningEffort) (*Response, error) {
	if c.client == nil {
		return nil, ErrNoClient
	}
	params := c.buildRequestParams(effort)
	resp, err := c.client.raw.Responses.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return c.applyResponseOutput(ctx, resp), nil
}

// applyResponseOutput appends the model's output items from resp to
// the conversation and records token usage, returning a [Response]
// view. It is shared with the streaming commit path so the
// post-request conversation state is identical regardless of how the
// response was delivered.
func (c *Conversation) applyResponseOutput(ctx context.Context, resp *responses.Response) *Response {
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
				c.AppendAssistantText(text)
			}
		case "function_call":
			call := item.AsFunctionCall()
			tc := ToolCall{
				CallID:    call.CallID,
				Name:      call.Name,
				Arguments: call.Arguments,
			}
			out.ToolCalls = append(out.ToolCalls, tc)
			c.AppendToolCall(tc)
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
			if c.client.replayReasoning {
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
				c.AppendReasoning(text, raw)
			}
		}
	}
	// Auto-shrink is best-effort: when a registered Summarizer fails,
	// recordUsage has already fallen back to Trim so the next request
	// still fits in context.
	_ = c.recordUsage(ctx, out.Usage)
	return out
}

// buildRequestParams assembles the Responses API request body from the
// conversation's current state and the bound client's configuration. It
// is shared by the non-streaming and streaming request paths so both
// endpoints see exactly the same prefix and include list.
//
// effort, when non-empty, overrides the client's default reasoning
// effort on the resulting params; an empty effort falls back to
// [Client.ReasoningEffort].
func (c *Conversation) buildRequestParams(effort ReasoningEffort) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: c.client.model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: buildResponseInput(c.turns)},
		Tools: buildToolParams(c.toolSpecs),
	}
	if c.instructions != "" {
		params.Instructions = openai.String(c.instructions)
	}
	resolvedEffort := effort
	if resolvedEffort == "" {
		resolvedEffort = c.client.reasoningEffort
	}
	if resolvedEffort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort: shared.ReasoningEffort(resolvedEffort),
		}
	}
	if c.client.replayReasoning {
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
