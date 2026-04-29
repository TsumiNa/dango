package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/tsumina/dango/internal/llm"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

type testExecutor struct {
	run    func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error)
	polish func(ctx context.Context) (any, error)
	report func(ctx context.Context, output any) (any, error)
}

func (e *testExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	if e.run == nil {
		return nil, nil, nil
	}
	return e.run(ctx, parentOutputs)
}

func (e *testExecutor) Polish(ctx context.Context) (any, error) {
	if e.polish == nil {
		return nil, nil
	}
	return e.polish(ctx)
}

func (e *testExecutor) Report(ctx context.Context, output any) (any, error) {
	if e.report == nil {
		return nil, nil
	}
	return e.report(ctx, output)
}

func mustNewRunnerStore(t *testing.T, dir string) *JSONRunnerStore {
	t.Helper()
	store, err := NewJSONRunnerStore(dir)
	if err != nil {
		t.Fatalf("NewJSONRunnerStore: %v", err)
	}
	return store
}

func waitForRunnerEvent(t *testing.T, ch <-chan RunnerEvent, want EventType, nodeID string) RunnerEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for event %s/%s", want.String(), nodeID)
		case ev := <-ch:
			if ev.Type == want && ev.NodeID == nodeID {
				return ev
			}
		}
	}
}

func hasStoredEvent(records []RunnerRecord, eventType string, nodeID string) bool {
	for _, rec := range records {
		if rec.Kind != RunnerRecordEvent || rec.Event == nil {
			continue
		}
		if rec.Event.Type == eventType && rec.Event.NodeID == nodeID {
			return true
		}
	}
	return false
}

func lastStatus(records []RunnerRecord) RunnerRecord {
	var last RunnerRecord
	for _, rec := range records {
		if rec.Kind == RunnerRecordStatus {
			last = rec
		}
	}
	return last
}

func assertCanceledStart(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}
}

func assertFailureContains(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("Start err = %v, want %q", err, text)
	}
}

func bindTestPlannerSkill(t *testing.T, outputs ...string) *llm.Skill {
	t.Helper()
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		text := outputs[len(outputs)-1]
		if responded < len(outputs) {
			text = outputs[responded]
		}
		responded++
		payload, err := json.Marshal(map[string]any{
			"id":         "r1",
			"object":     "response",
			"created_at": 0,
			"model":      "test-model",
			"status":     "completed",
			"output": []map[string]any{{
				"id":     "m1",
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        text,
					"annotations": []any{},
				}},
			}},
			"parallel_tool_calls": false,
			"tool_choice":         "auto",
			"tools":               []any{},
		})
		if err != nil {
			t.Fatalf("marshal planner response: %v", err)
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	content := "---\nname: planner\ndescription: Test planner.\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, llm.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	sk, err := llm.NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("llm.New: %v", err)
	}
	raw := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(srv.URL+"/"))
	client, err := llm.NewClient(llm.ClientConfig{Provider: llm.ProviderOpenAI, Model: "test-model", Raw: raw})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	bound, err := sk.Bind(client, nil, nil)
	if err != nil {
		t.Fatalf("Bind(planner): %v", err)
	}
	return bound
}

func mustReviewJSON(t *testing.T, approved bool, reason string) string {
	t.Helper()
	buf, err := json.Marshal(map[string]any{"approved": approved, "reason": reason})
	if err != nil {
		t.Fatalf("marshal review json: %v", err)
	}
	return string(buf)
}
