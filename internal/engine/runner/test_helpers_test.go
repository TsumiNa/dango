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
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

func newTestRunner() *Runner {
	return New(WithLogger(testLogger))
}

func newTestRunnerForPlan(plan *CoarsePlan, nodes map[string]*Node) *Runner {
	return New(WithLogger(testLogger), WithInitialPlan(plan, nodes))
}

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

func waitForRunnerEvent(t *testing.T, r *Runner, want EventType, nodeID string) streampkg.Event {
	t.Helper()
	eventType, _, ok := runnerEventStreamType(want, r.State().Status)
	if !ok {
		t.Fatalf("runner event %s has no stream event type", want.String())
	}
	filter := streampkg.Filter{
		EventTypes: []string{eventType},
		Scope:      streampkg.Scope{RunnerID: r.ID()},
	}
	if nodeID != "" {
		filter.Scope.NodeID = nodeID
	}
	sub, err := r.SubscribeStream(filter, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	defer sub.Cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		event, ok, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("stream error waiting for event %s/%s: %v", want.String(), nodeID, err)
		}
		if !ok {
			t.Fatalf("stream closed waiting for event %s/%s", want.String(), nodeID)
		}
		if !runnerStreamEventMatches(event, want, nodeID) {
			continue
		}
		return event
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
	sk, err := llm.NewSkill(dir, llm.DefaultSkillConfig())
	if err != nil {
		t.Fatalf("llm.New: %v", err)
	}
	raw := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(srv.URL+"/"))
	client, err := llm.NewClient(llm.ProviderOpenAI, "test-model", raw, llm.DefaultClientConfig())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	bound, err := sk.Bind(client, llm.DefaultConversationConfig())
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
