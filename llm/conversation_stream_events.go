package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	streampkg "github.com/tsumina/dango/stream"
)

const conversationStreamTextLimit = 4096

// auditCategoryMetadata is the metadata stamp that marks an event as part of
// the tool-call audit pipeline (see docs/tool-call-audit-schema.md). It is
// applied to llm.tool_call.started, llm.tool_call.completed,
// llm.tool_result.delta, and mcp.tool.call.completed so downstream consumers
// (the trace analyzer; future post-alpha audit storage) can filter on a
// single stable field instead of an event-type allowlist.
func auditCategoryMetadata() map[string]any {
	return map[string]any{"category": "audit"}
}

// EventStream returns the progress stream this conversation writes to, or nil
// when the conversation was created without [ConversationConfig.StreamEvents].
// The stream may be owned by the conversation or supplied by its caller through
// [ConversationConfig.EventStream].
func (c *Conversation) EventStream() *streampkg.Stream {
	if c == nil {
		return nil
	}
	return c.eventStream
}

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

func (c *Conversation) emitResponseCompleted(ctx context.Context, response *Response) {
	if response == nil {
		return
	}
	delta := map[string]any{
		"has_text":        response.Text != "",
		"tool_call_count": len(response.ToolCalls),
		"usage":           tokenUsageDelta(response.Usage),
	}
	if response.Raw != nil {
		if response.Raw.ID != "" {
			delta["response_id"] = response.Raw.ID
		}
		if response.Raw.Model != "" {
			delta["model"] = response.Raw.Model
		}
	}
	c.emitStreamEvent(ctx, streampkg.EventStatusCompleted, streampkg.StatusCompleted, delta, map[string]any{"stage": "llm_response"})
}

func (c *Conversation) emitLLMFailure(ctx context.Context, err error, stage string) {
	if err == nil {
		return
	}
	metadata := map[string]any(nil)
	if stage != "" {
		metadata = map[string]any{"stage": "llm_" + stage}
	}
	c.emitStreamEvent(ctx, streampkg.EventStatusFailed, streampkg.StatusFailed, map[string]any{
		"error": compactErrorText(err.Error()),
	}, metadata)
}

func tokenUsageDelta(usage TokenUsage) map[string]any {
	return map[string]any{
		"input_tokens":     usage.Input,
		"cached_tokens":    usage.Cached,
		"output_tokens":    usage.Output,
		"reasoning_tokens": usage.Reasoning,
		"total_tokens":     usage.Total,
	}
}

func (c *Conversation) emitTextDelta(ctx context.Context, eventType string, status string, text string) {
	if text == "" {
		return
	}
	// Truncate only live streaming deltas (StatusRunning). Final completed
	// outputs must be preserved in full so downstream consumers (e.g. the
	// renderer writing exchange files) receive the complete text.
	var delta string
	var truncated bool
	if status == streampkg.StatusRunning {
		delta, truncated = compactConversationText(text)
	} else {
		delta = text
	}
	metadata := map[string]any(nil)
	if truncated {
		metadata = map[string]any{"truncated": true}
	}
	c.emitStreamEvent(ctx, eventType, status, delta, metadata)
}

func (c *Conversation) emitToolCallStarted(ctx context.Context, call ToolCall) {
	c.emitStreamEvent(ctx, streampkg.EventLLMToolCallStarted, streampkg.StatusRunning, toolCallDelta(call), auditCategoryMetadata())
}

func (c *Conversation) emitToolCallCompleted(ctx context.Context, call ToolCall) {
	c.emitStreamEvent(ctx, streampkg.EventLLMToolCallCompleted, streampkg.StatusCompleted, toolCallDelta(call), auditCategoryMetadata())
}

func (c *Conversation) emitToolExecutionStarted(ctx context.Context, call ToolCall) {
	c.emitStreamEvent(ctx, streampkg.EventToolExecutionStarted, streampkg.StatusRunning, toolExecutionDelta(call, nil, Decision{}), nil)
}

func (c *Conversation) emitToolExecutionFinished(ctx context.Context, call ToolCall, execErr error, decision Decision) {
	eventType := streampkg.EventToolExecutionCompleted
	if execErr != nil {
		eventType = streampkg.EventToolExecutionFailed
	}
	c.emitStreamEvent(ctx, eventType, streamStatusForError(execErr), toolExecutionDelta(call, execErr, decision), nil)
}

func (c *Conversation) emitToolApprovalRequested(ctx context.Context, req ApprovalRequest) {
	c.emitStreamEvent(ctx, streampkg.EventToolApprovalRequested, streampkg.StatusPending, approvalRequestDelta(req), nil)
}

func (c *Conversation) emitToolApprovalResolved(ctx context.Context, req ApprovalRequest, resp ApprovalResponse, err error) {
	metadata := map[string]any(nil)
	if err != nil {
		metadata = map[string]any{"error": compactErrorText(err.Error())}
	}
	c.emitStreamEvent(ctx, streampkg.EventToolApprovalResolved, streamStatusForError(err), approvalResolvedDelta(req, resp), metadata)
}

func (c *Conversation) emitToolResult(ctx context.Context, callID string, output string, execErr error) {
	name := c.toolNameForCallID(callID)
	if server, mcpName := c.mcpDescriptorFor(name); server != "" {
		// Per docs/mcp-support-plan.md §6, MCP tool results stay in the
		// exchange/memo/handoff documents and are not written to the runtime
		// stream. We emit only the compact MCP call event so a top-level
		// caller still sees that a call happened.
		c.emitMCPToolCall(ctx, callID, name, server, mcpName, execErr)
		return
	}
	delta, truncated := compactConversationText(output)
	payload := map[string]any{
		"call_id": callID,
		"output":  delta,
	}
	if name != "" {
		payload["name"] = name
	}
	if truncated {
		payload["truncated"] = true
	}
	if execErr != nil {
		payload["error"] = compactErrorText(execErr.Error())
	}
	c.emitStreamEvent(ctx, streampkg.EventLLMToolResultDelta, streamStatusForError(execErr), payload, auditCategoryMetadata())
}

// mcpDescriptorFor returns the MCP server and bare tool name when the tool
// registered under name is an MCP-backed adapter, or empty strings when it
// is not.
func (c *Conversation) mcpDescriptorFor(name string) (string, string) {
	if c == nil || name == "" {
		return "", ""
	}
	tool, ok := c.toolByName[name]
	if !ok {
		return "", ""
	}
	m, ok := tool.(mcpToolMarker)
	if !ok {
		return "", ""
	}
	return m.mcpServerName(), m.mcpToolName()
}

// emitMCPToolCall publishes the compact MCP call event with the metadata the
// design plan calls for (server, tool, namespaced name, call id, compact
// argument summary, outcome, optional error) and never the result body.
func (c *Conversation) emitMCPToolCall(ctx context.Context, callID, namespaced, server, tool string, execErr error) {
	argsSummary, _ := compactJSONText(c.toolArgumentsForCallID(callID))
	outcome := "ok"
	if execErr != nil {
		outcome = "error"
	}
	payload := map[string]any{
		"server":            server,
		"tool":              tool,
		"namespaced_name":   namespaced,
		"call_id":           callID,
		"arguments_summary": argsSummary,
		"outcome":           outcome,
	}
	if execErr != nil {
		payload["error"] = compactErrorText(execErr.Error())
	}
	c.emitStreamEvent(ctx, streampkg.EventMCPToolCallCompleted, streamStatusForError(execErr), payload, auditCategoryMetadata())
}

func (c *Conversation) toolArgumentsForCallID(callID string) string {
	if c == nil || callID == "" {
		return ""
	}
	for i := len(c.turns) - 1; i >= 0; i-- {
		turn := c.turns[i]
		if turn.Role == RoleToolCall && turn.Tool != nil && turn.Tool.CallID == callID {
			return turn.Tool.Arguments
		}
	}
	return ""
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

func toolExecutionDelta(call ToolCall, execErr error, decision Decision) map[string]any {
	delta := map[string]any{
		"call_id": call.CallID,
		"name":    call.Name,
	}
	if execErr != nil {
		delta["error"] = compactErrorText(execErr.Error())
	}
	if decision.Policy != "" {
		delta["policy"] = string(decision.Policy)
	}
	if decision.Capability.Kind != "" {
		delta["capability_kind"] = string(decision.Capability.Kind)
	}
	if decision.Capability.Name != "" {
		delta["capability_name"] = decision.Capability.Name
	}
	if decision.Reason != "" {
		delta["policy_reason"] = decision.Reason
	}
	if decision.ApprovalOutcome != "" {
		delta["approval_outcome"] = string(decision.ApprovalOutcome)
	}
	if decision.ApprovalReason != "" {
		delta["approval_reason"] = decision.ApprovalReason
	}
	return delta
}

func approvalRequestDelta(req ApprovalRequest) map[string]any {
	delta := map[string]any{
		"policy": string(req.Policy),
	}
	if req.CallID != "" {
		delta["call_id"] = req.CallID
	}
	if req.ToolName != "" {
		delta["name"] = req.ToolName
	}
	if req.ArgumentsSummary != "" {
		delta["arguments_summary"] = req.ArgumentsSummary
	}
	if req.Capability.Kind != "" {
		delta["capability_kind"] = string(req.Capability.Kind)
	}
	if req.Capability.Name != "" {
		delta["capability_name"] = req.Capability.Name
	}
	if req.Reason != "" {
		delta["policy_reason"] = req.Reason
	}
	return delta
}

func approvalResolvedDelta(req ApprovalRequest, resp ApprovalResponse) map[string]any {
	delta := approvalRequestDelta(req)
	if resp.Outcome != "" {
		delta["approval_outcome"] = string(resp.Outcome)
	}
	if resp.Reason != "" {
		delta["approval_reason"] = resp.Reason
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
