package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestProviderBaseURL(t *testing.T) {
	cases := map[Provider]string{
		ProviderOpenAI:     "",
		ProviderOpenRouter: "https://openrouter.ai/api/v1/",
		ProviderGemini:     "https://generativelanguage.googleapis.com/v1beta/openai/",
	}
	for p, want := range cases {
		if got := p.baseURL(); got != want {
			t.Errorf("%s.baseURL() = %q, want %q", p, got, want)
		}
	}
}

func TestDetectProviderPriority(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantProvider Provider
		wantKey      string
		wantOK       bool
	}{
		{
			name:         "none",
			env:          nil,
			wantProvider: "",
			wantOK:       false,
		},
		{
			name:         "openai_only",
			env:          map[string]string{"OPENAI_API_KEY": "oai"},
			wantProvider: ProviderOpenAI,
			wantKey:      "oai",
			wantOK:       true,
		},
		{
			name:         "openai_wins_over_others",
			env:          map[string]string{"OPENAI_API_KEY": "oai", "OPENROUTER_API_KEY": "or", "GEMINI_API_KEY": "gm"},
			wantProvider: ProviderOpenAI,
			wantKey:      "oai",
			wantOK:       true,
		},
		{
			name:         "openrouter_over_gemini",
			env:          map[string]string{"OPENROUTER_API_KEY": "or", "GEMINI_API_KEY": "gm"},
			wantProvider: ProviderOpenRouter,
			wantKey:      "or",
			wantOK:       true,
		},
		{
			name:         "gemini_only",
			env:          map[string]string{"GEMINI_API_KEY": "gm"},
			wantProvider: ProviderGemini,
			wantKey:      "gm",
			wantOK:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			p, key, ok := detectProvider()
			if ok != tt.wantOK || p != tt.wantProvider || key != tt.wantKey {
				t.Fatalf("detectProvider() = (%q, %q, %v), want (%q, %q, %v)",
					p, key, ok, tt.wantProvider, tt.wantKey, tt.wantOK)
			}
		})
	}
}

func TestNewClientFromEnv_NoAPIKey(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("MODEL", "gpt-test")
	if _, err := NewClientFromEnv(); err != ErrNoAPIKey {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestNewClientFromEnv_NoModel(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai")
	t.Setenv("MODEL", "")
	if _, err := NewClientFromEnv(); err != ErrNoModel {
		t.Fatalf("err = %v, want ErrNoModel", err)
	}
}

func TestNewClientFromEnv_Success(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("MODEL", "some-model")

	c, err := NewClientFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Provider() != ProviderOpenRouter {
		t.Errorf("Provider() = %s, want %s", c.Provider(), ProviderOpenRouter)
	}
	if c.Model() != "some-model" {
		t.Errorf("Model() = %s, want some-model", c.Model())
	}
	if !strings.Contains(c.String(), "openrouter") || !strings.Contains(c.String(), "some-model") {
		t.Errorf("String() = %q, missing provider/model", c.String())
	}
	if c.Raw() == nil {
		t.Error("Raw() returned nil")
	}
}

func TestNewClientFromEnv_LoadsSpecifiedFile(t *testing.T) {
	unsetProviderEnv(t)
	envFile := filepath.Join(t.TempDir(), "custom.env")
	if err := os.WriteFile(envFile, []byte("OPENROUTER_API_KEY=or-file\nMODEL=file-model\nREASONING_EFFORT=low\nREASONING_REPLAY=yes\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	c, err := NewClientFromEnv(envFile)
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}
	if c.Provider() != ProviderOpenRouter {
		t.Errorf("Provider() = %s, want %s", c.Provider(), ProviderOpenRouter)
	}
	if c.Model() != "file-model" {
		t.Errorf("Model() = %s, want file-model", c.Model())
	}
	if c.ReasoningEffort() != ReasoningEffortLow {
		t.Errorf("ReasoningEffort() = %q, want low", c.ReasoningEffort())
	}
	if !c.ReplayReasoning() {
		t.Error("ReplayReasoning() = false, want true")
	}
	for _, key := range []string{"OPENROUTER_API_KEY", "MODEL", "REASONING_EFFORT", "REASONING_REPLAY"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Fatalf("NewClientFromEnv should not mutate process env, but %s is now set", key)
		}
	}
}

func TestNewClientFromEnv_ProcessEnvOverridesFile(t *testing.T) {
	unsetProviderEnv(t)
	envFile := filepath.Join(t.TempDir(), "custom.env")
	if err := os.WriteFile(envFile, []byte("OPENROUTER_API_KEY=or-file\nMODEL=file-model\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "env-openai")
	t.Setenv("MODEL", "env-model")

	c, err := NewClientFromEnv(envFile)
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}
	if c.Provider() != ProviderOpenAI {
		t.Fatalf("Provider() = %s, want %s", c.Provider(), ProviderOpenAI)
	}
	if c.Model() != "env-model" {
		t.Fatalf("Model() = %s, want env-model", c.Model())
	}
}

func TestClient_Respond(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		// Minimal Response payload with one output_text item.
		_, _ = w.Write([]byte(`{
			"id": "resp_1",
			"object": "response",
			"created_at": 0,
			"model": "test-model",
			"status": "completed",
			"output": [
				{
					"id": "msg_1",
					"type": "message",
					"role": "assistant",
					"status": "completed",
					"content": [
						{"type": "output_text", "text": "hello world", "annotations": []}
					]
				}
			],
			"parallel_tool_calls": false,
			"tool_choice": "auto",
			"tools": []
		}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		provider: ProviderOpenAI,
		model:    "test-model",
		raw: openai.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(srv.URL+"/"),
		),
	}

	out, err := c.Respond(t.Context(), "hi")
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if out != "hello world" {
		t.Errorf("Respond() = %q, want %q", out, "hello world")
	}
	if !strings.HasSuffix(gotPath, "/responses") {
		t.Errorf("request path = %q, want suffix /responses", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization header = %q", gotAuth)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, gotBody)
	}
	if req["model"] != "test-model" {
		t.Errorf("request model = %v, want test-model", req["model"])
	}
	if req["input"] != "hi" {
		t.Errorf("request input = %v, want \"hi\"", req["input"])
	}
}

// clearProviderEnv wipes the environment variables that NewClientFromEnv and
// detectProvider consult, isolating tests from the developer's real shell
// environment (including any .env file loaded via godotenv.Load).
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "GEMINI_API_KEY", "MODEL", "REASONING_EFFORT", "REASONING_REPLAY"} {
		t.Setenv(k, "")
	}
}

func unsetProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "GEMINI_API_KEY", "MODEL", "REASONING_EFFORT", "REASONING_REPLAY"} {
		key := k
		old, ok := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(key, old)
				return
			}
			_ = os.Unsetenv(key)
		})
	}
}

func TestClient_SendAppendsAssistantAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[
				{"id":"m1","type":"message","role":"assistant","status":"completed",
				 "content":[{"type":"output_text","text":"ok","annotations":[]}]}
			],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[],
			"usage":{
				"input_tokens":50,"input_tokens_details":{"cached_tokens":10},
				"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},
				"total_tokens":55
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")

	resp, err := conv.Send(t.Context(), "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("Response.Text = %q, want %q", resp.Text, "ok")
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("unexpected tool calls: %+v", resp.ToolCalls)
	}
	if resp.Usage.Input != 50 || resp.Usage.Cached != 10 || resp.Usage.Output != 5 || resp.Usage.Reasoning != 2 || resp.Usage.Total != 55 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
	if conv.Usage() != resp.Usage {
		t.Errorf("conversation.Usage() not updated: %+v", conv.Usage())
	}
	turns := conv.Turns()
	if len(turns) != 2 || turns[1].Role != RoleAssistant || turns[1].Text != "ok" {
		t.Errorf("assistant turn not appended: %+v", turns)
	}
}

func TestClient_SendRecordsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[
				{"id":"fc1","type":"function_call","status":"completed",
				 "call_id":"call_1","name":"echo","arguments":"{\"msg\":\"hi\"}"}
			],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "", []Tool{NewFuncTool("echo", "", map[string]any{"type": "object"}, nil)})
	conv.AppendUser("please echo")

	resp, err := conv.Send(t.Context(), "")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].CallID != "call_1" || resp.ToolCalls[0].Name != "echo" {
		t.Fatalf("unexpected ToolCalls: %+v", resp.ToolCalls)
	}
	turns := conv.Turns()
	if len(turns) != 2 || turns[1].Role != RoleToolCall {
		t.Errorf("tool_call turn not appended: %+v", turns)
	}
	if turns[1].Tool == nil || turns[1].Tool.CallID != "call_1" {
		t.Errorf("tool_call CallID not preserved: %+v", turns[1].Tool)
	}
}

// TestClient_SendPrefixStable verifies that the cache-critical request
// prefix (instructions, tools, plus all previously recorded turns) is
// byte-for-byte identical between two consecutive Send calls.
func TestClient_SendPrefixStable(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		requests = append(requests, m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[{"id":"m1","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "system prompt", []Tool{NewFuncTool("echo", "e", map[string]any{"type": "object"}, nil)})
	conv.AppendUser("hello")

	if _, err := conv.Send(t.Context(), ""); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	// Caller adds another user turn; the prior prefix must not shift.
	conv.AppendUser("follow-up")
	if _, err := conv.Send(t.Context(), ""); err != nil {
		t.Fatalf("second Send: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	// instructions and tools must match exactly.
	if requests[0]["instructions"] != requests[1]["instructions"] {
		t.Errorf("instructions changed: %v vs %v", requests[0]["instructions"], requests[1]["instructions"])
	}
	toolsA, _ := json.Marshal(requests[0]["tools"])
	toolsB, _ := json.Marshal(requests[1]["tools"])
	if string(toolsA) != string(toolsB) {
		t.Errorf("tools schema changed: %s vs %s", toolsA, toolsB)
	}
	inputA, _ := requests[0]["input"].([]any)
	inputB, _ := requests[1]["input"].([]any)
	if len(inputB) <= len(inputA) {
		t.Fatalf("second input length %d not > first %d", len(inputB), len(inputA))
	}
	for i := 0; i < len(inputA); i++ {
		a, _ := json.Marshal(inputA[i])
		b, _ := json.Marshal(inputB[i])
		if string(a) != string(b) {
			t.Errorf("prefix diverged at input[%d]:\n first:  %s\n second: %s", i, a, b)
		}
	}
}

func TestConversation_SendRejectsNilClient(t *testing.T) {
	conv := mustNewConversation(t, nil, "", nil)
	conv.AppendUser("hi")
	if _, err := conv.Send(t.Context(), ""); err != ErrNoClient {
		t.Errorf("err = %v, want ErrNoClient", err)
	}
}

func testClient(baseURL string) *Client {
	return &Client{
		provider: ProviderOpenAI,
		model:    "test-model",
		raw: openai.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(baseURL+"/"),
		),
	}
}

func TestNewClient_Config(t *testing.T) {
	raw := openai.NewClient(option.WithAPIKey("k"))
	cases := []struct {
		name    string
		cfg     ClientConfig
		wantErr bool
	}{
		{"missing provider", ClientConfig{Model: "m", Raw: raw}, true},
		{"missing model", ClientConfig{Provider: ProviderOpenAI, Raw: raw}, true},
		{"missing raw", ClientConfig{Provider: ProviderOpenAI, Model: "m"}, true},
		{"ok", ClientConfig{Provider: ProviderOpenAI, Model: "m", Raw: raw, ReasoningEffort: ReasoningEffortHigh}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.Provider() != tc.cfg.Provider || c.Model() != tc.cfg.Model {
				t.Errorf("provider/model = %s/%s", c.Provider(), c.Model())
			}
			if c.ReasoningEffort() != tc.cfg.ReasoningEffort {
				t.Errorf("ReasoningEffort() = %q, want %q", c.ReasoningEffort(), tc.cfg.ReasoningEffort)
			}
		})
	}
}

// TestClient_ReasoningEffortInRequest verifies that a configured
// ReasoningEffort is forwarded verbatim on the request body for both
// Respond and Send, and that the empty value omits the field entirely
// so non-reasoning models see an unchanged payload.
func TestClient_ReasoningEffortInRequest(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	mk := func(effort ReasoningEffort) *Client {
		c, err := NewClient(ClientConfig{
			Provider: ProviderOpenAI,
			Model:    "test-model",
			Raw: openai.NewClient(
				option.WithAPIKey("test-key"),
				option.WithBaseURL(srv.URL+"/"),
			),
			ReasoningEffort: effort,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		return c
	}

	assertEffort := func(label string, want string) {
		t.Helper()
		var req map[string]any
		if err := json.Unmarshal(lastBody, &req); err != nil {
			t.Fatalf("%s: body not JSON: %v (%s)", label, err, lastBody)
		}
		reasoning, ok := req["reasoning"]
		if want == "" {
			if ok {
				t.Errorf("%s: reasoning should be absent, got %v", label, reasoning)
			}
			return
		}
		m, isMap := reasoning.(map[string]any)
		if !ok || !isMap {
			t.Fatalf("%s: reasoning missing or not object: %v", label, reasoning)
		}
		if got := m["effort"]; got != want {
			t.Errorf("%s: reasoning.effort = %v, want %q", label, got, want)
		}
	}

	// Configured effort appears in the request.
	c := mk(ReasoningEffortHigh)
	if _, err := c.Respond(t.Context(), "hi"); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	assertEffort("Respond(high)", "high")

	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hello")
	if _, err := conv.Send(t.Context(), ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	assertEffort("Send(high)", "high")

	// Empty effort is omitted from the payload on both paths.
	c2 := mk("")
	if _, err := c2.Respond(t.Context(), "hi"); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	assertEffort("Respond(empty)", "")
	conv2 := mustNewConversation(t, c2, "sys", nil)
	conv2.AppendUser("hello")
	if _, err := conv2.Send(t.Context(), ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	assertEffort("Send(empty)", "")
}

// TestConversation_SendEffortOverride verifies that a per-call effort
// passed to Send overrides the client's configured default and that an
// empty effort falls back to the client's default, including the case
// where the client itself has no effort configured.
func TestConversation_SendEffortOverride(t *testing.T) {
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	mk := func(effort ReasoningEffort) *Client {
		c, err := NewClient(ClientConfig{
			Provider: ProviderOpenAI,
			Model:    "test-model",
			Raw: openai.NewClient(
				option.WithAPIKey("test-key"),
				option.WithBaseURL(srv.URL+"/"),
			),
			ReasoningEffort: effort,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		return c
	}

	readEffort := func() any {
		var req map[string]any
		if err := json.Unmarshal(lastBody, &req); err != nil {
			t.Fatalf("body not JSON: %v (%s)", err, lastBody)
		}
		r, ok := req["reasoning"]
		if !ok {
			return nil
		}
		m, _ := r.(map[string]any)
		return m["effort"]
	}

	// Client default is high; per-call "low" must override it.
	c := mk(ReasoningEffortHigh)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hello")
	if _, err := conv.Send(t.Context(), ReasoningEffortLow); err != nil {
		t.Fatalf("Send override: %v", err)
	}
	if got := readEffort(); got != "low" {
		t.Errorf("override: reasoning.effort = %v, want low", got)
	}

	// Empty effort falls back to the client's configured default.
	if _, err := conv.Send(t.Context(), ""); err != nil {
		t.Fatalf("Send fallback: %v", err)
	}
	if got := readEffort(); got != "high" {
		t.Errorf("fallback: reasoning.effort = %v, want high", got)
	}

	// Client has no default and no per-call effort: field is omitted.
	c2 := mk("")
	conv2 := mustNewConversation(t, c2, "sys", nil)
	conv2.AppendUser("hello")
	if _, err := conv2.Send(t.Context(), ""); err != nil {
		t.Fatalf("Send empty+empty: %v", err)
	}
	if got := readEffort(); got != nil {
		t.Errorf("empty+empty: reasoning should be absent, got %v", got)
	}

	// Client has no default but a per-call effort is supplied.
	if _, err := conv2.Send(t.Context(), ReasoningEffortMedium); err != nil {
		t.Fatalf("Send empty+medium: %v", err)
	}
	if got := readEffort(); got != "medium" {
		t.Errorf("empty+medium: reasoning.effort = %v, want medium", got)
	}
}

func TestNewClientFromEnv_ReasoningEffort(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai")
	t.Setenv("MODEL", "m")
	t.Setenv("REASONING_EFFORT", "low")

	c, err := NewClientFromEnv()
	if err != nil {
		t.Fatalf("NewClientFromEnv: %v", err)
	}
	if c.ReasoningEffort() != ReasoningEffortLow {
		t.Errorf("ReasoningEffort() = %q, want low", c.ReasoningEffort())
	}
}

// TestClient_SendCapturesReasoning verifies that reasoning items emitted
// by the model are recorded as RoleReasoning turns on the conversation
// (for debugging / traceability) but are omitted from the request body
// on the next Send so they do not round-trip to the provider.
func TestClient_SendCapturesReasoning(t *testing.T) {
	var lastBody []byte
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		responded++
		if responded == 1 {
			// First response mixes a reasoning item and an assistant message.
			_, _ = w.Write([]byte(`{
				"id":"r1","object":"response","created_at":0,"model":"test-model","status":"completed",
				"output":[
					{"id":"rs1","type":"reasoning","status":"completed",
					 "summary":[{"type":"summary_text","text":"checked arithmetic"}],
					 "content":[{"type":"reasoning_text","text":"12^2+3^2=153; sqrt=12.37"}]},
					{"id":"m1","type":"message","role":"assistant","status":"completed",
					 "content":[{"type":"output_text","text":"12.37","annotations":[]}]}
				],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m2","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")
	if _, err := conv.Send(t.Context(), ""); err != nil {
		t.Fatalf("Send 1: %v", err)
	}

	turns := conv.Turns()
	var reasoning *Turn
	for i := range turns {
		if turns[i].Role == RoleReasoning {
			reasoning = &turns[i]
			break
		}
	}
	if reasoning == nil {
		t.Fatalf("no RoleReasoning turn recorded; turns=%+v", turns)
	}
	if !strings.Contains(reasoning.Text, "checked arithmetic") ||
		!strings.Contains(reasoning.Text, "12.37") {
		t.Errorf("reasoning text = %q, want summary+content", reasoning.Text)
	}
	if reasoning.Tier != TierToolIO {
		t.Errorf("reasoning tier = %v, want TierToolIO", reasoning.Tier)
	}

	// Round-trip through JSON to confirm persistence works for sessions.
	data, err := json.Marshal(conv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored Conversation
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found bool
	for _, t := range restored.Turns() {
		if t.Role == RoleReasoning {
			found = true
			break
		}
	}
	if !found {
		t.Error("reasoning turn lost after JSON round-trip")
	}

	// Second Send: reasoning must not appear in the outbound request body.
	conv.AppendUser("follow-up")
	if _, err := conv.Send(t.Context(), ""); err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	if strings.Contains(string(lastBody), "checked arithmetic") ||
		strings.Contains(string(lastBody), "reasoning_text") {
		t.Errorf("reasoning leaked into next request body: %s", lastBody)
	}
}
