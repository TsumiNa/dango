package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	conv := NewConversation(testClient(srv.URL), "sys", []Tool{echo})
	out, err := conv.Run(t.Context(), "do it")
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

// TestConversationRun_MaxStepsExceeded confirms Run bails out with an
// error when the model keeps requesting tool calls past the iteration
// bound rather than looping forever.
func TestConversationRun_MaxStepsExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[{"id":"fc","type":"function_call","status":"completed","call_id":"c","name":"loop","arguments":"{}"}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	loop := NewFuncTool("loop", "", map[string]any{"type": "object"},
		func(_ context.Context, _ string) (string, error) { return "again", nil },
	)
	conv := NewConversation(testClient(srv.URL), "sys", []Tool{loop})
	conv.SetMaxSteps(2)
	if _, err := conv.Run(t.Context(), "go"); err == nil {
		t.Fatal("expected max-steps error")
	} else if !strings.Contains(err.Error(), "exceeded max steps") {
		t.Errorf("error = %v, want max-steps message", err)
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

	conv := NewConversation(testClient(srv.URL), "sys", nil)
	out, err := conv.Run(t.Context(), "go")
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

// TestConversationRun_NilClientReturnsError confirms Run rejects
// conversations that are not bound to a transport.
func TestConversationRun_NilClientReturnsError(t *testing.T) {
	conv := NewConversation(nil, "sys", nil)
	if _, err := conv.Run(t.Context(), "hi"); err == nil {
		t.Fatal("expected ErrNoClient")
	}
}
