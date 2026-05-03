package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	raw := openai.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(baseURL+"/"),
	)
	c, err := NewClient(ProviderOpenAI, "test-model", raw, DefaultClientConfig())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

type runSkillConfig struct {
	Dir          string
	Client       *Client
	Tools        []Tool
	MaxSteps     int
	AutoTrim     *AutoShrinkConfig
	Summarizer   Summarizer
	SessionStore SessionStore
	SessionID    string
}

// newRunSkill builds a minimal Skill backed by a temp SKILL.md and binds it
// with the given runtime configuration. The frontmatter's prompt body is used
// as the conversation's system instructions.
func newRunSkill(t *testing.T, cfg runSkillConfig) *Skill {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = writeSkillDir(t, "---\nname: run-test\ndescription: d\n---\nsystem\n")
	}
	sk, err := NewSkill(cfg.Dir, DefaultSkillConfig(), WithTools(cfg.Tools...))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	convCfg := DefaultConversationConfig()
	if cfg.MaxSteps > 0 || cfg.AutoTrim != nil || cfg.Summarizer != nil {
		convCfg.MaxSteps = cfg.MaxSteps
		convCfg.AutoShrink = cfg.AutoTrim
		convCfg.Summarizer = cfg.Summarizer
	}
	var bindOpts []BindOption
	if cfg.SessionID != "" {
		if cfg.SessionStore != nil {
			bindOpts = append(bindOpts, WithExistingSession(cfg.SessionID, cfg.SessionStore))
		} else {
			bindOpts = append(bindOpts, WithExistingSession(cfg.SessionID))
		}
	} else if cfg.SessionStore != nil {
		bindOpts = append(bindOpts, WithNewSession(cfg.SessionStore))
	}
	bound, err := sk.Bind(cfg.Client, convCfg, bindOpts...)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return bound
}

func TestSkillRejectsDuplicateToolNames(t *testing.T) {
	a := NewFuncTool("x", "", map[string]any{}, func(context.Context, string) (string, error) { return "", nil })
	b := NewFuncTool("x", "", map[string]any{}, func(context.Context, string) (string, error) { return "", nil })
	dir := writeSkillDir(t, "---\nname: x\ndescription: d\n---\n")
	if _, err := NewSkill(dir, DefaultSkillConfig(), WithTools(a, b)); err == nil {
		t.Fatal("expected error for duplicate tool names")
	}
}

// TestSkillRunToolLoop drives the loop through a fake Responses API: the
// first response requests a function_call; the second returns the final
// message.
func TestSkillRunToolLoop(t *testing.T) {
	var requests [][]byte
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		if responded == 0 {
			responded++
			_, _ = w.Write([]byte(`{
				"id":"resp_1","object":"response","created_at":0,"model":"test-model","status":"completed",
				"output":[{
					"id":"fc_1","type":"function_call","status":"completed",
					"call_id":"call_1","name":"echo","arguments":"{\"msg\":\"hello\"}"
				}],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"resp_2","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{
				"id":"msg_1","type":"message","role":"assistant","status":"completed",
				"content":[{"type":"output_text","text":"done: hello","annotations":[]}]
			}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	var echoed string
	echo := NewFuncTool("echo", "echo msg", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"msg": map[string]any{"type": "string"},
		},
		"required": []string{"msg"},
	}, func(_ context.Context, arguments string) (string, error) {
		var a struct {
			Msg string `json:"msg"`
		}
		_ = json.Unmarshal([]byte(arguments), &a)
		echoed = a.Msg
		return a.Msg, nil
	})

	sk := newRunSkill(t, runSkillConfig{
		Client: newTestClient(t, srv.URL),
		Tools:  []Tool{echo},
	})
	out, err := sk.Run(context.Background(), "please echo hello", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done: hello" {
		t.Errorf("Run output = %q, want %q", out, "done: hello")
	}
	if echoed != "hello" {
		t.Errorf("tool received %q, want %q", echoed, "hello")
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	// Second request must carry the function_call_output back to the model.
	if !strings.Contains(string(requests[1]), `"function_call_output"`) {
		t.Errorf("second request missing function_call_output: %s", requests[1])
	}
	if !strings.Contains(string(requests[1]), `"call_1"`) {
		t.Errorf("second request missing call_id: %s", requests[1])
	}
}

func TestSkillRunUnknownToolReportsError(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if responded == 0 {
			responded++
			_, _ = w.Write([]byte(`{
				"id":"r","object":"response","created_at":0,"model":"test-model","status":"completed",
				"output":[{"id":"fc","type":"function_call","status":"completed","call_id":"c","name":"nope","arguments":"{}"}],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	sk := newRunSkill(t, runSkillConfig{Client: newTestClient(t, srv.URL)})
	out, err := sk.Run(context.Background(), "go", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "ok" {
		t.Errorf("Run output = %q, want %q", out, "ok")
	}
}

func TestSkillRunMaxStepsExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"fc","type":"function_call","status":"completed","call_id":"c","name":"loop","arguments":"{}"}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	loop := NewFuncTool("loop", "", map[string]any{"type": "object"},
		func(context.Context, string) (string, error) { return "", nil })

	sk := newRunSkill(t, runSkillConfig{
		Client:   newTestClient(t, srv.URL),
		Tools:    []Tool{loop},
		MaxSteps: 2,
	})
	if _, err := sk.Run(context.Background(), "go", ""); err == nil {
		t.Fatal("expected error when max steps exceeded")
	}
}

// TestSkillWithSummarizerAndAutoTrim verifies that AutoTrim and Summarizer are
// applied to the conversation built inside Bind by
// observing that the registered summarizer is invoked once the second
// response reports input tokens above the threshold.
func TestSkillWithSummarizerAndAutoTrim(t *testing.T) {
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// First response: a function_call so the loop continues to a
		// second request whose Usage will trigger auto-shrink.
		if responded == 0 {
			responded++
			_, _ = w.Write([]byte(`{
				"id":"r","object":"response","created_at":0,"model":"test-model","status":"completed",
				"output":[{"id":"fc","type":"function_call","status":"completed","call_id":"c","name":"echo","arguments":"{}"}],
				"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
			}`))
			return
		}
		// Second response: final text reply with usage above threshold.
		_, _ = w.Write([]byte(`{
			"id":"r2","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"done","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[],
			"usage":{
				"input_tokens":900,"input_tokens_details":{"cached_tokens":0},
				"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},
				"total_tokens":901
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	echo := NewFuncTool("echo", "", map[string]any{"type": "object"},
		func(context.Context, string) (string, error) { return "out", nil })

	called := 0
	sum := SummarizerFunc(func(_ context.Context, _ []Turn) (string, error) {
		called++
		return "compact", nil
	})

	sk := newRunSkill(t, runSkillConfig{
		Client: newTestClient(t, srv.URL),
		Tools:  []Tool{echo},
		AutoTrim: &AutoShrinkConfig{
			ContextWindow:     1000,
			Threshold:         0.5,
			KeepToolExchanges: 1,
			KeepTurns:         1,
		},
		Summarizer: sum,
	})
	if _, err := sk.Run(context.Background(), "go", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 1 {
		t.Errorf("summarizer called %d times, want 1", called)
	}
}

// TestSkillWithSession verifies that a persisted session is loaded on a
// second Run so the saved conversation is reused and extended.
func TestSkillWithSession(t *testing.T) {
	var requests [][]byte
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		responded++
		_, _ = w.Write([]byte(`{
			"id":"r","object":"response","created_at":0,"model":"test-model","status":"completed",
			"output":[{"id":"m","type":"message","role":"assistant","status":"completed",
			 "content":[{"type":"output_text","text":"reply","annotations":[]}]}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[]
		}`))
	}))
	t.Cleanup(srv.Close)

	store, err := NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	sk := newRunSkill(t, runSkillConfig{
		Client:       newTestClient(t, srv.URL),
		SessionStore: store,
	})
	if _, err := sk.Run(context.Background(), "first", ""); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	sessionID := sk.Conversation().SessionID()
	if sessionID == "" {
		t.Fatal("SessionID is empty after Bind with a store")
	}

	sess, err := store.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Load after run 1: %v", err)
	}
	if turns := countTurnEvents(sess); turns != 2 {
		t.Fatalf("after run 1: turn events = %d, want 2", turns)
	}

	// A new skill bound to the same session should ship the prior turns in
	// the request body alongside the new user input.
	restored := newRunSkill(t, runSkillConfig{
		Client:       newTestClient(t, srv.URL),
		SessionStore: store,
		SessionID:    sessionID,
	})
	if _, err := restored.Run(context.Background(), "second", ""); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	second := string(requests[1])
	if !strings.Contains(second, `"first"`) || !strings.Contains(second, `"reply"`) || !strings.Contains(second, `"second"`) {
		t.Errorf("second request missing resumed history: %s", second)
	}

	sess2, err := store.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Load after run 2: %v", err)
	}
	if turns := countTurnEvents(sess2); turns != 4 {
		t.Errorf("after run 2: turn events = %d, want 4", turns)
	}
}

// countTurnEvents returns the number of events in the session log that
// represent appended turns (user/assistant/reasoning/tool_call/tool_output).
func countTurnEvents(events []Event) int {
	n := 0
	for _, ev := range events {
		switch ev.Kind {
		case EventAppendUser, EventAppendAssistant,
			EventAppendReasoning, EventAppendToolCall, EventAppendToolOutput:
			n++
		}
	}
	return n
}
