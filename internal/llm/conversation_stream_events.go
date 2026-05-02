package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

const conversationStreamTextLimit = 4096

func (c *Conversation) emitStreamEvent(ctx context.Context, eventType string, status string, delta any, metadata map[string]any) {
	if c == nil || c.eventStream == nil {
		return
	}
	raw, err := json.Marshal(delta)
	if err != nil {
		raw, _ = json.Marshal(fmt.Sprint(delta))
	}
	_ = c.eventStream.Emit(ctx, streampkg.Event{
		EventType: eventType,
		From:      c.eventSource,
		Status:    status,
		Delta:     json.RawMessage(raw),
		Scope:     c.eventScope,
		Metadata:  c.streamMetadata(metadata),
	})
}

func (c *Conversation) emitTextDelta(ctx context.Context, eventType string, status string, text string) {
	if text == "" {
		return
	}
	delta, truncated := compactConversationText(text)
	metadata := map[string]any(nil)
	if truncated {
		metadata = map[string]any{"truncated": true}
	}
	c.emitStreamEvent(ctx, eventType, status, delta, metadata)
}

func (c *Conversation) emitToolCallStarted(ctx context.Context, call ToolCall) {
	c.emitStreamEvent(ctx, streampkg.EventLLMToolCallStarted, streampkg.StatusRunning, toolCallDelta(call), nil)
}

func (c *Conversation) emitToolCallCompleted(ctx context.Context, call ToolCall) {
	c.emitStreamEvent(ctx, streampkg.EventLLMToolCallCompleted, streampkg.StatusCompleted, toolCallDelta(call), nil)
}

func (c *Conversation) emitToolExecutionStarted(ctx context.Context, call ToolCall) {
	c.emitStreamEvent(ctx, streampkg.EventToolExecutionStarted, streampkg.StatusRunning, toolExecutionDelta(call, nil), nil)
}

func (c *Conversation) emitToolExecutionFinished(ctx context.Context, call ToolCall, execErr error) {
	eventType := streampkg.EventToolExecutionCompleted
	if execErr != nil {
		eventType = streampkg.EventToolExecutionFailed
	}
	c.emitStreamEvent(ctx, eventType, streamStatusForError(execErr), toolExecutionDelta(call, execErr), nil)
}

func (c *Conversation) emitToolResult(ctx context.Context, callID string, output string, execErr error) {
	delta, truncated := compactConversationText(output)
	payload := map[string]any{
		"call_id": callID,
		"output":  delta,
	}
	if name := c.toolNameForCallID(callID); name != "" {
		payload["name"] = name
	}
	if truncated {
		payload["truncated"] = true
	}
	if execErr != nil {
		payload["error"] = compactErrorText(execErr.Error())
	}
	c.emitStreamEvent(ctx, streampkg.EventLLMToolResultDelta, streamStatusForError(execErr), payload, nil)
}

func toolCallDelta(call ToolCall) map[string]any {
	args, truncated := compactJSONText(call.Arguments)
	delta := map[string]any{
		"call_id":   call.CallID,
		"name":      call.Name,
		"arguments": args,
	}
	if truncated {
		delta["arguments_truncated"] = true
	}
	return delta
}

func toolExecutionDelta(call ToolCall, execErr error) map[string]any {
	delta := map[string]any{
		"call_id": call.CallID,
		"name":    call.Name,
	}
	if execErr != nil {
		delta["error"] = compactErrorText(execErr.Error())
	}
	return delta
}

func (c *Conversation) toolNameForCallID(callID string) string {
	for i := len(c.turns) - 1; i >= 0; i-- {
		turn := c.turns[i]
		if turn.Role == RoleToolCall && turn.Tool != nil && turn.Tool.CallID == callID {
			return turn.Tool.Name
		}
	}
	return ""
}

func streamStatusForError(err error) string {
	if err != nil {
		return streampkg.StatusFailed
	}
	return streampkg.StatusCompleted
}

func compactConversationText(text string) (string, bool) {
	if len(text) <= conversationStreamTextLimit {
		return text, false
	}
	return text[:conversationStreamTextLimit], true
}

func compactErrorText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}

func compactJSONText(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return compactConversationText(text)
	}
	sanitized, truncated := sanitizeJSONValue(v)
	b, err := json.Marshal(v)
	if truncated {
		b, err = json.Marshal(sanitized)
	}
	if err != nil {
		return compactConversationText(text)
	}
	out, textTruncated := compactConversationText(string(b))
	return out, truncated || textTruncated
}

func sanitizeJSONValue(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		out, truncated := compactConversationText(v)
		return out, truncated
	case []any:
		var truncated bool
		out := make([]any, len(v))
		for i, item := range v {
			var itemTruncated bool
			out[i], itemTruncated = sanitizeJSONValue(item)
			truncated = truncated || itemTruncated
		}
		return out, truncated
	case map[string]any:
		var truncated bool
		out := make(map[string]any, len(v))
		for k, item := range v {
			var itemTruncated bool
			out[k], itemTruncated = sanitizeJSONValue(item)
			truncated = truncated || itemTruncated
		}
		return out, truncated
	default:
		return value, false
	}
}

func (c *Conversation) streamMetadata(metadata map[string]any) map[string]any {
	if len(c.eventMetadata) == 0 && len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(c.eventMetadata)+len(metadata))
	for k, v := range c.eventMetadata {
		out[k] = v
	}
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

func cloneConversationStreamMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}
