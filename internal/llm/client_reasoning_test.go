package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// reasoningReplayResponseBody is a Responses API payload that emits a
// reasoning item (with encrypted_content) followed by a function_call
// so tests can exercise the capture+replay path.
const reasoningReplayResponseBody = `{
	"id":"r1","object":"response","created_at":0,"model":"test-model","status":"completed",
	"output":[
		{"id":"rs_abc","type":"reasoning","status":"completed",
		 "summary":[{"type":"summary_text","text":"plan"}],
		 "content":[{"type":"reasoning_text","text":"call echo"}],
		 "encrypted_content":"ENC_BLOB"},
		{"id":"fc1","type":"function_call","status":"completed",
		 "call_id":"call_1","name":"echo","arguments":"{}"}
	],
	"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
}`

// testClientWithReplay returns a Client wired to baseURL with
// ReplayReasoning enabled so the reasoning capture/replay branches run.
func testClientWithReplay(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{
		Provider: ProviderOpenAI,
		Model:    "test-model",
		Raw: openai.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(baseURL+"/"),
		),
		ReplayReasoning: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// TestClient_ReplayReasoning_CapturesRaw verifies that Send stores the
// full reasoning item (including encrypted_content) on the resulting
// Turn.Raw when ReplayReasoning is enabled.
func TestClient_ReplayReasoning_CapturesRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reasoningReplayResponseBody))
	}))
	t.Cleanup(srv.Close)

	c := testClientWithReplay(t, srv.URL)
	conv := c.NewConversation("sys", []ToolSpec{{Name: "echo", Parameters: map[string]any{"type": "object"}}})
	conv.AppendUser("hi")
	if _, err := c.Send(t.Context(), conv); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var reasoning *Turn
	for i, tr := range conv.Turns() {
		if tr.Role == RoleReasoning {
			reasoning = &conv.Turns()[i]
			break
		}
	}
	if reasoning == nil {
		t.Fatalf("no reasoning turn recorded")
	}
	if len(reasoning.Raw) == 0 {
		t.Fatalf("reasoning.Raw is empty; want full reasoning item JSON")
	}
	var got map[string]any
	if err := json.Unmarshal(reasoning.Raw, &got); err != nil {
		t.Fatalf("Raw is not valid JSON: %v (%s)", err, reasoning.Raw)
	}
	if got["id"] != "rs_abc" {
		t.Errorf("Raw.id = %v, want rs_abc", got["id"])
	}
	if got["encrypted_content"] != "ENC_BLOB" {
		t.Errorf("Raw.encrypted_content = %v, want ENC_BLOB", got["encrypted_content"])
	}
	// The capture path round-trips raw through
	// ResponseReasoningItemParam so buildResponseInput is guaranteed
	// to decode it on replay. Assert that invariant here so future
	// SDK shape drift between the output and input types surfaces as
	// a test failure at capture time instead of a silent skip at
	// replay time.
	var probe responses.ResponseReasoningItemParam
	if err := json.Unmarshal(reasoning.Raw, &probe); err != nil {
		t.Fatalf("Raw does not decode into ResponseReasoningItemParam: %v", err)
	}
}

// TestClient_ReplayReasoning_IncludesAndReplays verifies that enabling
// ReplayReasoning both asks the provider for encrypted_content on every
// request and replays the captured reasoning item on the next Send so
// tool-calling continuity is preserved.
func TestClient_ReplayReasoning_IncludesAndReplays(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(reasoningReplayResponseBody))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m2","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"done","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	c := testClientWithReplay(t, srv.URL)
	conv := c.NewConversation("sys", []ToolSpec{{Name: "echo", Parameters: map[string]any{"type": "object"}}})
	conv.AppendUser("please echo")

	resp, err := c.Send(t.Context(), conv)
	if err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %+v", resp.ToolCalls)
	}
	// Caller runs the tool and feeds the output back.
	conv.AppendToolOutput(resp.ToolCalls[0].CallID, "ok", nil)

	if _, err := c.Send(t.Context(), conv); err != nil {
		t.Fatalf("Send 2: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("want 2 requests, got %d", len(bodies))
	}

	// Both requests must ask for encrypted_content via include.
	for i, b := range bodies {
		var req map[string]any
		if err := json.Unmarshal([]byte(b), &req); err != nil {
			t.Fatalf("request %d not JSON: %v", i, err)
		}
		include, _ := req["include"].([]any)
		if !containsString(include, "reasoning.encrypted_content") {
			t.Errorf("request %d missing reasoning.encrypted_content in include: %v", i, include)
		}
	}

	// Second request's input must contain the replayed reasoning item
	// (by id) immediately before the function_call/output pair.
	var req2 struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &req2); err != nil {
		t.Fatalf("unmarshal request 2: %v", err)
	}
	var seenReasoning, seenCall, seenOutput bool
	var reasoningIdx, callIdx, outputIdx int
	for i, item := range req2.Input {
		switch item["type"] {
		case "reasoning":
			seenReasoning = true
			reasoningIdx = i
			if item["id"] != "rs_abc" {
				t.Errorf("replayed reasoning id = %v, want rs_abc", item["id"])
			}
			if item["encrypted_content"] != "ENC_BLOB" {
				t.Errorf("replayed encrypted_content = %v, want ENC_BLOB", item["encrypted_content"])
			}
		case "function_call":
			seenCall = true
			callIdx = i
		case "function_call_output":
			seenOutput = true
			outputIdx = i
		}
	}
	if !seenReasoning || !seenCall || !seenOutput {
		t.Fatalf("missing items: reasoning=%v call=%v output=%v; input=%+v",
			seenReasoning, seenCall, seenOutput, req2.Input)
	}
	if !(reasoningIdx < callIdx && callIdx < outputIdx) {
		t.Errorf("order wrong: reasoning=%d call=%d output=%d", reasoningIdx, callIdx, outputIdx)
	}
}

// TestClient_ReplayReasoning_DroppedAfterNewUserTurn verifies that once
// a new user turn starts a fresh conversation cycle, any earlier
// reasoning items are not replayed (they belong to a closed cycle and
// replaying them would bloat the request prefix and break the cache).
func TestClient_ReplayReasoning_DroppedAfterNewUserTurn(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(reasoningReplayResponseBody))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m2","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"done","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	c := testClientWithReplay(t, srv.URL)
	conv := c.NewConversation("sys", []ToolSpec{{Name: "echo", Parameters: map[string]any{"type": "object"}}})
	conv.AppendUser("hi")
	if _, err := c.Send(t.Context(), conv); err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	// Simulate tool execution completing then the user starting a fresh turn.
	conv.AppendToolOutput("call_1", "ok", nil)
	conv.AppendUser("follow-up")
	if _, err := c.Send(t.Context(), conv); err != nil {
		t.Fatalf("Send 2: %v", err)
	}

	if strings.Contains(bodies[1], `"rs_abc"`) || strings.Contains(bodies[1], "ENC_BLOB") {
		t.Errorf("reasoning from closed cycle replayed: %s", bodies[1])
	}
}

// TestClient_ReplayReasoning_DisabledKeepsPhase1Behavior verifies that
// without ReplayReasoning the Phase 1 observability-only semantics
// hold: no Raw is stored, no include is requested, and the next Send
// does not carry any reasoning item.
func TestClient_ReplayReasoning_DisabledKeepsPhase1Behavior(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(reasoningReplayResponseBody))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m2","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"done","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL) // ReplayReasoning defaults to false.
	conv := c.NewConversation("sys", []ToolSpec{{Name: "echo", Parameters: map[string]any{"type": "object"}}})
	conv.AppendUser("hi")
	if _, err := c.Send(t.Context(), conv); err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	conv.AppendToolOutput("call_1", "ok", nil)
	if _, err := c.Send(t.Context(), conv); err != nil {
		t.Fatalf("Send 2: %v", err)
	}

	// No Raw stored on any turn.
	for _, tr := range conv.Turns() {
		if tr.Role == RoleReasoning && len(tr.Raw) != 0 {
			t.Errorf("reasoning.Raw populated with ReplayReasoning=false: %s", tr.Raw)
		}
	}
	// No include requested and no reasoning id in second body.
	var req map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &req); err != nil {
		t.Fatalf("unmarshal body 2: %v", err)
	}
	if _, ok := req["include"]; ok {
		t.Errorf("include should be absent when ReplayReasoning=false: %v", req["include"])
	}
	if strings.Contains(bodies[1], `"rs_abc"`) {
		t.Errorf("reasoning leaked into body 2: %s", bodies[1])
	}
}

// TestNewClientFromEnv_ReasoningReplay verifies the REASONING_REPLAY
// environment variable drives the ReplayReasoning config flag.
func TestNewClientFromEnv_ReasoningReplay(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{" yes ", true},
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			clearProviderEnv(t)
			t.Setenv("OPENAI_API_KEY", "oai")
			t.Setenv("ORCHESTRATION_MODEL", "m")
			t.Setenv("REASONING_REPLAY", tc.val)
			c, err := NewClientFromEnv()
			if err != nil {
				t.Fatalf("NewClientFromEnv: %v", err)
			}
			if c.ReplayReasoning() != tc.want {
				t.Errorf("ReplayReasoning() = %v, want %v", c.ReplayReasoning(), tc.want)
			}
		})
	}
}

// containsString reports whether items contains the given string. It
// accepts []any to work against json.Unmarshal's generic map output.
func containsString(items []any, want string) bool {
	for _, it := range items {
		if s, ok := it.(string); ok && s == want {
			return true
		}
	}
	return false
}
