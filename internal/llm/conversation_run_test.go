package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

// TestConversationRun_HappyPath drives one tool call then a final
// message through Conversation.Run to confirm the loop dispatches the
// tool, feeds the output back, and returns the model's final text.
func TestConversationRun_HappyPath(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if responded == 0 {
			responded++
			_, _ = w.Write([]byte(`{
				"id":"r1","object":"response","created_at":0,"model":"m","status":"completed",
				"output":[{
					"id":"fc","type":"function_call","status":"completed",
					"call_id":"c1","name":"echo","arguments":"{\"msg\":\"hi\"}"
				}],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[{
				"id":"m1","type":"message","role":"assistant","status":"completed",
				"content":[{"type":"output_text","text":"final","annotations":[]}]
			}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	var executed bool
	echo := NewFuncTool("echo", "e", map[string]any{"type": "object"},
		func(_ context.Context, arguments string) (string, error) {
			executed = true
			return "ok", nil
		},
	)
	conv := mustNewConversation(t, testClient(srv.URL), "sys", []Tool{echo})
	out, err := conv.Run(t.Context(), "do it", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "final" {
		t.Errorf("Run output = %q, want %q", out, "final")
	}
	if !executed {
		t.Error("tool not executed")
	}
	// Conversation must carry user + tool_call + tool_output + assistant turns.
	roles := []Role{}
	for _, tn := range conv.Turns() {
		roles = append(roles, tn.Role)
	}
	want := []Role{RoleUser, RoleToolCall, RoleToolOutput, RoleAssistant}
	if len(roles) != len(want) {
		t.Fatalf("turns = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Errorf("turn[%d] role = %q, want %q", i, roles[i], want[i])
		}
	}
}

func TestConversationRun_EmitsStreamEvents(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if responded == 0 {
			responded++
			sseResponse(w, completedEvent("", "echo", `{"msg":"hi"}`))
			return
		}
		sseResponse(w, textDeltaEvent("final"), completedEvent("final", "", ""))
	}))
	t.Cleanup(srv.Close)

	echo := NewFuncTool("echo", "e", map[string]any{"type": "object"},
		func(_ context.Context, arguments string) (string, error) {
			return "tool says ok", nil
		},
	)
	conv := mustNewConversation(t, testClient(srv.URL), "sys", []Tool{echo}, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "echo_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_1", NodeID: "node_1"},
		StreamMetadata: map[string]any{
			"skill_name": "echo_skill",
		},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := conv.Run(t.Context(), "do it", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	eventStream.Close()

	events := collectStreamEvents(t, sub)
	if !hasStreamEvent(events, streampkg.EventLLMToolCallCompleted, "skill", "node_1", func(delta map[string]any) bool {
		args, _ := delta["arguments"].(string)
		return delta["name"] == "echo" && strings.Contains(args, `"msg":"hi"`)
	}) {
		t.Fatalf("missing tool call completed event: %+v", events)
	}
	if !hasStreamEvent(events, streampkg.EventLLMToolCallStarted, "skill", "node_1", func(delta map[string]any) bool {
		return delta["name"] == "echo"
	}) {
		t.Fatalf("missing tool call started event: %+v", events)
	}
	if !hasStreamEvent(events, streampkg.EventLLMToolResultDelta, "skill", "node_1", func(delta map[string]any) bool {
		return delta["name"] == "echo" && delta["output"] == "tool says ok"
	}) {
		t.Fatalf("missing tool result event: %+v", events)
	}
	if !hasStreamEvent(events, streampkg.EventToolExecutionStarted, "skill", "node_1", func(delta map[string]any) bool {
		return delta["name"] == "echo"
	}) {
		t.Fatalf("missing tool execution started event: %+v", events)
	}
	if !hasStreamEvent(events, streampkg.EventToolExecutionCompleted, "skill", "node_1", func(delta map[string]any) bool {
		return delta["name"] == "echo"
	}) {
		t.Fatalf("missing tool execution completed event: %+v", events)
	}
	var sawOutput bool
	for _, event := range events {
		if event.EventType != streampkg.EventLLMOutputDelta {
			continue
		}
		var text string
		if err := json.Unmarshal(event.Delta, &text); err != nil {
			t.Fatalf("unmarshal output delta: %v", err)
		}
		if text == "final" && event.Scope.RequestID == "req_1" && event.Scope.NodeID == "node_1" {
			sawOutput = true
		}
	}
	if !sawOutput {
		t.Fatalf("missing final output event: %+v", events)
	}
	if !streamEventOrder(events, streampkg.EventLLMToolCallStarted, streampkg.EventLLMToolCallCompleted, streampkg.EventToolExecutionStarted, streampkg.EventToolExecutionCompleted, streampkg.EventLLMToolResultDelta) {
		t.Fatalf("tool stream event order is wrong: %+v", events)
	}
}

func TestConversationRun_StreamsReasoningAndOutputDeltas(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		sseResponse(w,
			reasoningDeltaEvent("response.reasoning_summary_text.delta", "thinking live"),
			textDeltaEvent("partial "),
			textDeltaEvent("answer"),
			completedEvent("partial answer", "", ""),
		)
	}))
	t.Cleanup(srv.Close)

	conv := mustNewConversation(t, testClient(srv.URL), "sys", nil, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "stream_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_streaming_run", NodeID: "node_stream"},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	out, err := conv.Run(t.Context(), "do it", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "partial answer" {
		t.Fatalf("Run output = %q, want partial answer", out)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, gotBody)
	}
	if stream, _ := req["stream"].(bool); !stream {
		t.Fatalf("StreamEvents=true request stream = %v, want true; body=%s", req["stream"], gotBody)
	}
	eventStream.Close()

	events := collectStreamEvents(t, sub)
	if !hasStringStreamEvent(events, streampkg.EventLLMReasoningDelta, streampkg.StatusRunning, "thinking live") {
		t.Fatalf("missing running reasoning delta: %+v", events)
	}
	if !hasStringStreamEvent(events, streampkg.EventLLMOutputDelta, streampkg.StatusRunning, "partial ") {
		t.Fatalf("missing running output delta: %+v", events)
	}
	if !hasStringStreamEvent(events, streampkg.EventLLMOutputDelta, streampkg.StatusCompleted, "partial answer") {
		t.Fatalf("missing completed final output delta: %+v", events)
	}
}

func TestConversationRun_EmitsToolExecutionFailedEvent(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if responded == 0 {
			responded++
			sseResponse(w, completedEvent("", "fail", `{}`))
			return
		}
		sseResponse(w, textDeltaEvent("recovered"), completedEvent("recovered", "", ""))
	}))
	t.Cleanup(srv.Close)

	failTool := NewFuncTool("fail", "f", map[string]any{"type": "object"},
		func(_ context.Context, _ string) (string, error) {
			return "partial output", errors.New("tool exploded")
		},
	)
	conv := mustNewConversation(t, testClient(srv.URL), "sys", []Tool{failTool}, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "fail_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_fail", NodeID: "node_fail"},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := conv.Run(t.Context(), "do it", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	eventStream.Close()

	events := collectStreamEvents(t, sub)
	if !hasStreamEvent(events, streampkg.EventToolExecutionFailed, "skill", "node_fail", func(delta map[string]any) bool {
		errText, _ := delta["error"].(string)
		return delta["name"] == "fail" && strings.Contains(errText, "tool exploded")
	}) {
		t.Fatalf("missing tool execution failed event: %+v", events)
	}
	if !hasStreamEvent(events, streampkg.EventLLMToolResultDelta, "skill", "node_fail", func(delta map[string]any) bool {
		output, _ := delta["output"].(string)
		return delta["name"] == "fail" && delta["error"] == "tool exploded" && strings.Contains(output, "partial output")
	}) {
		t.Fatalf("missing failed tool result event: %+v", events)
	}
	if !streamEventOrder(events, streampkg.EventToolExecutionStarted, streampkg.EventToolExecutionFailed, streampkg.EventLLMToolResultDelta) {
		t.Fatalf("failed tool stream event order is wrong: %+v", events)
	}
}

// TestConversationRun_MaxStepsExceeded confirms Run bails out with an
// error when the model keeps requesting tool calls past the iteration
// bound rather than looping forever.
func TestConversationRun_MaxStepsExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseResponse(w, completedEvent("", "loop", `{}`))
	}))
	t.Cleanup(srv.Close)

	loop := NewFuncTool("loop", "", map[string]any{"type": "object"},
		func(_ context.Context, _ string) (string, error) { return "again", nil },
	)
	conv := mustNewConversation(t, testClient(srv.URL), "sys", []Tool{loop}, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "loop_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_loop", NodeID: "node_loop"},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventStatusFailed}}, streampkg.WithSubscriberBuffer(4))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	conv.SetMaxSteps(2)
	if _, err := conv.Run(t.Context(), "go", ""); err == nil {
		t.Fatal("expected max-steps error")
	} else if !strings.Contains(err.Error(), "exceeded max steps") {
		t.Errorf("error = %v, want max-steps message", err)
	}
	eventStream.Close()
	if !hasStreamEvent(collectStreamEvents(t, sub), streampkg.EventStatusFailed, "skill", "node_loop", func(delta map[string]any) bool {
		errorText, _ := delta["error"].(string)
		return strings.Contains(errorText, "exceeded max steps")
	}) {
		t.Fatalf("missing max-steps failure event")
	}
}

// TestConversationRun_UnknownToolRecovers confirms that an unknown tool
// call is surfaced back to the model as function_call_output and the
// loop continues rather than aborting.
func TestConversationRun_UnknownToolRecovers(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if responded == 0 {
			responded++
			_, _ = w.Write([]byte(`{
				"id":"r1","object":"response","created_at":0,"model":"m","status":"completed",
				"output":[{"id":"fc","type":"function_call","status":"completed","call_id":"c","name":"nope","arguments":"{}"}],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	conv := mustNewConversation(t, testClient(srv.URL), "sys", nil)
	out, err := conv.Run(t.Context(), "go", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "ok" {
		t.Errorf("Run output = %q, want %q", out, "ok")
	}
	// The unknown-tool error must have been surfaced to the model as
	// a function_call_output turn, so the conversation should contain
	// one tool_output turn recording the error.
	var outputTurns int
	for _, tn := range conv.Turns() {
		if tn.Role == RoleToolOutput {
			outputTurns++
			if tn.Tool == nil || !strings.Contains(tn.Tool.Error, "unknown tool") {
				t.Errorf("tool_output turn = %+v, want unknown-tool error", tn.Tool)
			}
		}
	}
	if outputTurns != 1 {
		t.Errorf("got %d tool_output turns, want 1", outputTurns)
	}
}

func TestPolicyPassbyRunsImmediately(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if responded == 0 {
			responded++
			_, _ = w.Write([]byte(`{
				"id":"r1","object":"response","created_at":0,"model":"m","status":"completed",
				"output":[{"id":"fc","type":"function_call","status":"completed","call_id":"c","name":"echo","arguments":"{}"}],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	var executed bool
	echo := NewFuncTool("echo", "echo", map[string]any{"type": "object"}, func(_ context.Context, _ string) (string, error) {
		executed = true
		return "done", nil
	})
	tools := wrapToolsWithPolicySet([]Tool{echo}, ToolSetConfig{
		Policies: map[CapabilityRef]ExecPolicy{
			ToolCapability("echo"): ExecPolicyPassby,
		},
	})
	conv := mustNewConversation(t, testClient(srv.URL), "sys", tools)
	if out, err := conv.Run(t.Context(), "go", ""); err != nil {
		t.Fatalf("Run: %v", err)
	} else if out != "ok" {
		t.Fatalf("Run output = %q, want ok", out)
	}
	if !executed {
		t.Fatal("passby tool did not execute")
	}
}

func TestPolicyOffRejects(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if responded == 0 {
			responded++
			_, _ = w.Write([]byte(`{
				"id":"r1","object":"response","created_at":0,"model":"m","status":"completed",
				"output":[{"id":"fc","type":"function_call","status":"completed","call_id":"c","name":"echo","arguments":"{}"}],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"recovered","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	var executed bool
	echo := NewFuncTool("echo", "echo", map[string]any{"type": "object"}, func(_ context.Context, _ string) (string, error) {
		executed = true
		return "done", nil
	})
	tools := wrapToolsWithPolicySet([]Tool{echo}, ToolSetConfig{
		Policies: map[CapabilityRef]ExecPolicy{
			ToolCapability("echo"): ExecPolicyOff,
		},
	})
	conv := mustNewConversation(t, testClient(srv.URL), "sys", tools)
	if out, err := conv.Run(t.Context(), "go", ""); err != nil {
		t.Fatalf("Run: %v", err)
	} else if out != "recovered" {
		t.Fatalf("Run output = %q, want recovered", out)
	}
	if executed {
		t.Fatal("off policy still executed tool")
	}
	var sawDisabled bool
	for _, turn := range conv.Turns() {
		if turn.Role == RoleToolOutput && turn.Tool != nil && strings.Contains(turn.Tool.Error, "disabled") {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Fatal("tool output did not record disabled error")
	}
}

func TestPolicyNeedApproveRunsInInterim(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if responded == 0 {
			responded++
			sseResponse(w, completedEvent("", "echo", `{}`))
			return
		}
		sseResponse(w, textDeltaEvent("ok"), completedEvent("ok", "", ""))
	}))
	t.Cleanup(srv.Close)

	var executed bool
	echo := NewFuncTool("echo", "echo", map[string]any{"type": "object"}, func(_ context.Context, _ string) (string, error) {
		executed = true
		return "done", nil
	})
	tools := wrapToolsWithPolicySet([]Tool{echo}, ToolSetConfig{
		Policies: map[CapabilityRef]ExecPolicy{
			ToolCapability("echo"): ExecPolicyNeedApprove,
		},
	})
	conv := mustNewConversation(t, testClient(srv.URL), "sys", tools, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "policy_skill"},
		StreamScope:  streampkg.Scope{NodeID: "node_policy"},
	})
	sub, err := conv.EventStream().Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if out, err := conv.Run(t.Context(), "go", ""); err != nil {
		t.Fatalf("Run: %v", err)
	} else if out != "ok" {
		t.Fatalf("Run output = %q, want ok", out)
	}
	conv.EventStream().Close()
	if !executed {
		t.Fatal("need_approve policy should still execute in 12a")
	}
	events := collectStreamEvents(t, sub)
	if !hasStreamEvent(events, streampkg.EventToolExecutionCompleted, "skill", "node_policy", func(delta map[string]any) bool {
		return delta["name"] == "echo" && delta["policy"] == "need_approve"
	}) {
		t.Fatalf("missing need_approve recording in tool execution event: %+v", events)
	}
}

// TestConversationRun_NilClientReturnsError confirms Run rejects
// conversations that are not bound to a transport.
func TestConversationRun_NilClientReturnsError(t *testing.T) {
	conv := mustNewConversation(t, nil, "sys", nil)
	if _, err := conv.Run(t.Context(), "hi", ""); err == nil {
		t.Fatal("expected ErrNoClient")
	}
}

func collectStreamEvents(t *testing.T, sub *streampkg.Subscription) []streampkg.Event {
	t.Helper()
	var events []streampkg.Event
	for {
		event, ok, err := sub.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			return events
		}
		events = append(events, event)
	}
}

func hasStreamEvent(events []streampkg.Event, eventType string, layer string, nodeID string, match func(map[string]any) bool) bool {
	for _, event := range events {
		if event.EventType != eventType || event.From.Layer != layer || event.Scope.NodeID != nodeID {
			continue
		}
		var delta map[string]any
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			continue
		}
		if match(delta) {
			return true
		}
	}
	return false
}

func hasStringStreamEvent(events []streampkg.Event, eventType string, status string, want string) bool {
	for _, event := range events {
		if event.EventType != eventType || event.Status != status {
			continue
		}
		var delta string
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			continue
		}
		if delta == want {
			return true
		}
	}
	return false
}

func streamEventOrder(events []streampkg.Event, eventTypes ...string) bool {
	next := 0
	for _, event := range events {
		if next < len(eventTypes) && event.EventType == eventTypes[next] {
			next++
		}
	}
	return next == len(eventTypes)
}
