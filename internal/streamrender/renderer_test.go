package streamrender

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func TestRendererFormatsRunnerFailureContext(t *testing.T) {
	renderer := New(nil, Config{})
	line := renderer.FormatEvent(streampkg.Event{
		EventType: streampkg.EventRunnerNodeFailed,
		From:      streampkg.Source{Layer: "runner", ID: "runner-1"},
		Status:    streampkg.StatusFailed,
		Scope:     streampkg.Scope{RunnerID: "runner-1", NodeID: "train_model"},
		Delta:     json.RawMessage(`{"event":"NodeFailed","node_id":"train_model","error":"skill execution loop did not produce final markdown"}`),
		Metadata:  map[string]any{"skill_name": "train_gp_model"},
	})
	for _, want := range []string{
		"status=failed",
		"error=\"skill execution loop did not produce final markdown\"",
		"event=node.failed",
		"node=train_model",
		"skill=train_gp_model",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
}

func TestRendererFiltersAndDedupe(t *testing.T) {
	var out bytes.Buffer
	renderer := New(&out, Config{
		Filter:         streampkg.Filter{Prefixes: []string{"runner."}},
		DedupeRepeated: true,
	})
	toolEvent := streampkg.Event{
		EventType: streampkg.EventToolExecutionStarted,
		From:      streampkg.Source{Layer: "skill", ID: "skill"},
		Status:    streampkg.StatusRunning,
		Delta:     json.RawMessage(`{"call_id":"c","name":"bash"}`),
	}
	phaseEvent := streampkg.Event{
		EventType: streampkg.EventRunnerPhaseChanged,
		From:      streampkg.Source{Layer: "runner", ID: "runner"},
		Status:    streampkg.StatusCompleted,
		Scope:     streampkg.Scope{RunnerID: "runner"},
		Delta:     json.RawMessage(`{"phase":"settled","status":"idle"}`),
	}
	if err := renderer.RenderEvent(toolEvent); err != nil {
		t.Fatalf("RenderEvent(tool): %v", err)
	}
	if err := renderer.RenderEvent(phaseEvent); err != nil {
		t.Fatalf("RenderEvent(first phase): %v", err)
	}
	if err := renderer.RenderEvent(phaseEvent); err != nil {
		t.Fatalf("RenderEvent(second phase): %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("rendered lines = %d, want 1:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], "phase=settled") {
		t.Fatalf("phase line = %q", lines[0])
	}
}

func TestRendererCompressesRunningOutputWithFrame(t *testing.T) {
	renderer := New(nil, Config{MaxText: 12, ProgressFrames: []string{"*"}})
	event := streampkg.Event{
		EventType: streampkg.EventLLMOutputDelta,
		From:      streampkg.Source{Layer: "skill", ID: "train"},
		Status:    streampkg.StatusRunning,
		Delta:     mustJSONString(t, "this is a very long model output chunk"),
	}
	line := renderer.FormatEvent(event)
	for _, want := range []string{"Skill[train]", "output", "streaming", "*"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "this is a") || strings.Contains(line, "truncated=true") {
		t.Fatalf("running output leaked raw text: %q", line)
	}
	if repeated := renderer.FormatEvent(event); repeated != "" {
		t.Fatalf("repeated running output line = %q, want suppressed", repeated)
	}
}

func TestRendererBatchesRunningReasoning(t *testing.T) {
	renderer := New(nil, Config{MaxText: 200, ProgressFrames: []string{"*"}})
	event := streampkg.Event{
		EventType: streampkg.EventLLMReasoningDelta,
		From:      streampkg.Source{Layer: "skill", ID: "train"},
		Status:    streampkg.StatusRunning,
		Delta:     mustJSONString(t, "small "),
	}
	if line := renderer.FormatEvent(event); line != "" {
		t.Fatalf("short reasoning line = %q, want buffered", line)
	}
	event.Delta = mustJSONString(t, strings.Repeat("reasoning ", 32))
	line := renderer.FormatEvent(event)
	if !strings.Contains(line, "Skill[train]") || !strings.Contains(line, "reasoning") || !strings.Contains(line, "small reasoning") {
		t.Fatalf("batched reasoning line = %q", line)
	}
}

func TestRendererLinksArtifactPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plot.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	renderer := New(nil, Config{})
	line := renderer.FormatEvent(streampkg.Event{
		EventType: streampkg.EventArtifactCreated,
		From:      streampkg.Source{Layer: "executor", ID: "node"},
		Status:    streampkg.StatusCompleted,
		Delta:     json.RawMessage(`{"path":` + string(mustJSONString(t, path)) + `,"resource_type":"file","stage":"execute"}`),
	})
	for _, want := range []string{"Executor[node]", "artifact=file://", "plot.png", "type=file", "stage=execute"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q:\n%s", want, line)
		}
	}
}

func TestRendererWritesExchangeMarkdownReferences(t *testing.T) {
	dir := t.TempDir()
	renderer := New(nil, Config{ExchangeDir: dir})
	line := renderer.FormatEvent(streampkg.Event{
		EventType:      streampkg.EventLLMOutputDelta,
		From:           streampkg.Source{Layer: "skill", ID: "writer"},
		Status:         streampkg.StatusCompleted,
		SequenceNumber: 7,
		Delta: mustJSONString(t, `---
kind: dango.exchange
version: 1
stage: execute
---

## Memo

done`),
	})
	if !strings.Contains(line, "exchange=file://") || !strings.Contains(line, "exchange-000000000007.md") {
		t.Fatalf("exchange line = %q", line)
	}
	if _, err := os.Stat(filepath.Join(dir, "exchange-000000000007.md")); err != nil {
		t.Fatalf("exchange file not written: %v", err)
	}
}

func TestRendererWritesDraftExchangeMarkdownReferences(t *testing.T) {
	dir := t.TempDir()
	renderer := New(nil, Config{ExchangeDir: dir})
	line := renderer.FormatEvent(streampkg.Event{
		EventType:      streampkg.EventLLMOutputDelta,
		From:           streampkg.Source{Layer: "skill", ID: "writer"},
		Status:         streampkg.StatusCompleted,
		SequenceNumber: 9,
		Delta: mustJSONString(t, `---
kind: dango_exchange
status: ready
---

# Memo
done

# Handoff
payload`),
	})
	if !strings.Contains(line, "exchange=file://") || !strings.Contains(line, "exchange-000000000009.md") {
		t.Fatalf("exchange line = %q", line)
	}
	written, err := os.ReadFile(filepath.Join(dir, "exchange-000000000009.md"))
	if err != nil {
		t.Fatalf("exchange file not written: %v", err)
	}
	if !strings.Contains(string(written), "kind: dango_exchange") || !strings.Contains(string(written), "# Handoff") {
		t.Fatalf("draft exchange file was not preserved raw:\n%s", string(written))
	}
}

func TestRendererSummarizesOrchestratorPlanningOutput(t *testing.T) {
	renderer := New(nil, Config{})
	line := renderer.FormatEvent(streampkg.Event{
		EventType: streampkg.EventLLMOutputDelta,
		From:      streampkg.Source{Layer: "orchestrator", ID: "orchestrator"},
		Status:    streampkg.StatusCompleted,
		Delta:     mustJSONString(t, `{"plan":{"nodes":[{"id":"a"}]}}`),
		Metadata:  map[string]any{"stage": "planning"},
	})
	if !strings.Contains(line, "planning output captured") || strings.Contains(line, `"nodes"`) {
		t.Fatalf("planning line = %q", line)
	}
}

func TestRendererHidesOrchestratorReviewJSONOutput(t *testing.T) {
	renderer := New(nil, Config{})
	line := renderer.FormatEvent(streampkg.Event{
		EventType: streampkg.EventLLMOutputDelta,
		From:      streampkg.Source{Layer: "orchestrator", ID: "orchestrator"},
		Status:    streampkg.StatusCompleted,
		Delta:     mustJSONString(t, `{"approved":true}`),
	})
	if line != "" {
		t.Fatalf("review output line = %q, want hidden", line)
	}
}

func TestRendererHidesSkillTokenCompletion(t *testing.T) {
	renderer := New(nil, Config{})
	line := renderer.FormatEvent(streampkg.Event{
		EventType: streampkg.EventStatusCompleted,
		From:      streampkg.Source{Layer: "skill", ID: "skill"},
		Status:    streampkg.StatusCompleted,
		Delta:     json.RawMessage(`{"usage":{"total_tokens":9510}}`),
	})
	if line != "" {
		t.Fatalf("skill token completion line = %q, want hidden", line)
	}
}

func TestRendererOmitsTokenUsageFromStatusLines(t *testing.T) {
	renderer := New(nil, Config{})
	line := renderer.FormatEvent(streampkg.Event{
		EventType: streampkg.EventStatusCompleted,
		From:      streampkg.Source{Layer: "orchestrator", ID: "orchestrator"},
		Status:    streampkg.StatusCompleted,
		Delta:     json.RawMessage(`{"message":"done","usage":{"total_tokens":42}}`),
	})
	if strings.Contains(line, "total_tokens=") {
		t.Fatalf("status line leaked token usage: %q", line)
	}
	if !strings.Contains(line, "done") || !strings.Contains(line, "event=status.completed") {
		t.Fatalf("status line missing core fields: %q", line)
	}
}

func TestRendererDrainsSubscription(t *testing.T) {
	s := streampkg.New(streampkg.Scope{RequestID: "req"})
	sub, err := s.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(4))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := s.Emit(context.Background(), streampkg.Event{
		EventType: streampkg.EventRunnerPhaseChanged,
		From:      streampkg.Source{Layer: "runner", ID: "runner"},
		Status:    streampkg.StatusCompleted,
		Delta:     json.RawMessage(`{"phase":"settled","status":"idle"}`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	s.Close()

	var out bytes.Buffer
	renderer := New(&out, Config{})
	if err := renderer.RenderSubscription(context.Background(), sub); err != nil {
		t.Fatalf("RenderSubscription: %v", err)
	}
	if !strings.Contains(out.String(), "phase=settled") {
		t.Fatalf("rendered output = %q", out.String())
	}
}

func mustJSONString(t *testing.T, text string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return b
}
