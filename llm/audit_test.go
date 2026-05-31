package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	streampkg "github.com/tsumina/dango/stream"
)

// TestToolCallStartedEventCarriesAuditCategory exercises the audit-tag
// metadata stamp from subtask 60a. The runtime stream consumer (and the
// trace analyzer in tools/analyze-tool-traces) filters on
// `metadata.category == "audit"`, so a regression here would silently
// disconnect the audit pipeline.
func TestToolCallStartedEventCarriesAuditCategory(t *testing.T) {
	events := runOneToolCall(t, "echo", `{"msg":"hi"}`, "tool says ok")

	got := findAuditEvent(t, events, streampkg.EventLLMToolCallStarted)
	if got.Metadata["category"] != "audit" {
		t.Fatalf("started: category metadata = %v, want \"audit\"", got.Metadata["category"])
	}
}

func TestToolCallCompletedEventCarriesAuditCategory(t *testing.T) {
	events := runOneToolCall(t, "echo", `{"msg":"hi"}`, "tool says ok")

	got := findAuditEvent(t, events, streampkg.EventLLMToolCallCompleted)
	if got.Metadata["category"] != "audit" {
		t.Fatalf("completed: category metadata = %v, want \"audit\"", got.Metadata["category"])
	}
}

func TestToolResultDeltaCarriesAuditCategory(t *testing.T) {
	events := runOneToolCall(t, "echo", `{"msg":"hi"}`, "tool says ok")

	got := findAuditEvent(t, events, streampkg.EventLLMToolResultDelta)
	if got.Metadata["category"] != "audit" {
		t.Fatalf("result.delta: category metadata = %v, want \"audit\"", got.Metadata["category"])
	}
}

// TestToolCallEventTruncatesLargeArguments confirms the documented
// 4096-byte cap on the `arguments` field still fires, so the audit
// payload never bloats past the schema's contract.
func TestToolCallEventTruncatesLargeArguments(t *testing.T) {
	big := strings.Repeat("x", 10_000)
	args := `{"msg":"` + big + `"}`
	events := runOneToolCall(t, "echo", args, "ok")

	got := findAuditEvent(t, events, streampkg.EventLLMToolCallStarted)
	var delta map[string]any
	if err := json.Unmarshal(got.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	arguments, _ := delta["arguments"].(string)
	if len(arguments) > conversationStreamTextLimit {
		t.Fatalf("arguments not truncated: len=%d cap=%d", len(arguments), conversationStreamTextLimit)
	}
	if delta["arguments_truncated"] != true {
		t.Fatalf("expected arguments_truncated=true when arguments exceed the cap, got %v", delta["arguments_truncated"])
	}
}

// runOneToolCall stands up an SSE-backed conversation that dispatches a
// single function call to the named tool with the given arguments string,
// then returns every emitted stream event. The fake LLM responds with a
// tool call on the first turn and a final text on the second.
func runOneToolCall(t *testing.T, tool, args, toolOutput string) []streampkg.Event {
	t.Helper()
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if responded == 0 {
			responded++
			sseResponse(w, completedEvent("", tool, args))
			return
		}
		sseResponse(w, textDeltaEvent("final"), completedEvent("final", "", ""))
	}))
	t.Cleanup(srv.Close)

	handler := NewFuncTool(tool, "", map[string]any{"type": "object"},
		func(_ context.Context, _ string) (string, error) {
			return toolOutput, nil
		},
	)
	conv := mustNewConversation(t, testClient(srv.URL), "sys", []Tool{handler}, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "audit_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_audit", NodeID: "node_audit"},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := conv.Run(t.Context(), "go", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	eventStream.Close()
	return collectStreamEvents(t, sub)
}

// findAuditEvent returns the first event of the given type. It exists to
// give the audit-tag tests a clear failure mode when an emission is
// missing entirely.
func findAuditEvent(t *testing.T, events []streampkg.Event, eventType string) streampkg.Event {
	t.Helper()
	for _, ev := range events {
		if ev.EventType == eventType {
			return ev
		}
	}
	t.Fatalf("event %q not found in stream", eventType)
	return streampkg.Event{}
}
