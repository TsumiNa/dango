package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
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
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

func loadLightweightTestSkill(t *testing.T) *llm.Skill {
	t.Helper()
	dir := t.TempDir()
	content := "---\nname: t\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, llm.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	sk, err := llm.NewSkill(dir, llm.DefaultSkillConfig())
	if err != nil {
		t.Fatalf("llm.New: %v", err)
	}
	return sk
}

func TestNewExecutor_BindsSkillAndPlanner(t *testing.T) {
	sk := loadLightweightTestSkill(t)
	client := &llm.Client{}
	planner := &ExecutionPlanner{}
	exec, err := NewExecutor(sk, planner, llm.DefaultConversationConfig(), WithExecutorClient(client))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if exec.Skill() != sk {
		t.Errorf("Skill() = %p, want %p", exec.Skill(), sk)
	}
	if exec.Planner() != planner {
		t.Errorf("Planner() = %p, want %p", exec.Planner(), planner)
	}
	if exec.LLMClient() != client {
		t.Fatalf("LLMClient() = %p, want %p", exec.LLMClient(), client)
	}
}

func TestExecutorExposesBoundSkillRuntimeEventStream(t *testing.T) {
	cfg := llm.DefaultConversationConfig()
	cfg.StreamEvents = true
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{id: "node-1"}, cfg, WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if exec.EventStream() != nil {
		t.Fatal("EventStream() before binding is non-nil")
	}
	if _, err := exec.BindForRunner(nil, runnerpkg.ExecutorRuntimePaths{}); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}
	stream := exec.EventStream()
	if stream == nil {
		t.Fatal("EventStream() after binding is nil")
	}
	if got := exec.runtime.EventStream(); got != stream {
		t.Fatalf("runtime skill stream = %p, want executor-exposed stream %p", got, stream)
	}
	if got := exec.runtime.Conversation().EventStream(); got != stream {
		t.Fatalf("runtime conversation stream = %p, want skill stream %p", got, stream)
	}
}

func TestNewExecutor_RejectsNilSkill(t *testing.T) {
	if _, err := NewExecutor(nil, &ExecutionPlanner{}, llm.DefaultConversationConfig()); err == nil {
		t.Fatal("expected error for nil skill")
	}
}

func TestNewExecutor_RejectsNilPlanner(t *testing.T) {
	if _, err := NewExecutor(loadLightweightTestSkill(t), nil, llm.DefaultConversationConfig()); err == nil {
		t.Fatal("expected error for nil planner")
	}
}

func TestExchangeDocMarkdownTreatsBodyAsOpaqueMarkdown(t *testing.T) {
	handoff, err := (runnerpkg.HandoffDoc{
		ChannelHeader: streampkg.ChannelHeader{
			RunnerID:  "runner-1",
			CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		},
		FromNode: "node-1",
		ToNodes:  []string{"downstream"},
		Intent:   "continue",
		Body:     "handoff body",
	}).Markdown()
	if err != nil {
		t.Fatalf("Handoff Markdown: %v", err)
	}

	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{id: "node-1"}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	raw, err := exec.exchangeDocMarkdown("runner-1", "node-1", "skill-1", "execute", handoff)
	if err != nil {
		t.Fatalf("exchangeDocMarkdown: %v", err)
	}

	parsed, err := runnerpkg.ParseExchangeDocMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseExchangeDocMarkdown: %v", err)
	}
	if parsed.Body != strings.TrimSpace(handoff) {
		t.Fatalf("body = %q, want original markdown body %q", parsed.Body, strings.TrimSpace(handoff))
	}
	if !strings.Contains(parsed.Body, "kind: handoff") {
		t.Fatalf("exchange body lost nested handoff markdown:\n%s", parsed.Body)
	}
}

func TestRenderStageOutputsRecreatesMissingExchangeDir(t *testing.T) {
	workspace, err := runnerpkg.ProvisionWorkspace(t.TempDir(), "runner-1", []string{"node-1"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	if _, ok := workspace.Skill("node-1"); !ok {
		t.Fatal("workspace.Skill(node-1) = false")
	}
	accessible, err := workspace.AccessibleDirs("node-1")
	if err != nil {
		t.Fatalf("AccessibleDirs: %v", err)
	}
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		id:              "node-1",
		TaskDescription: "Render stage outputs.",
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	runtimePaths, err := workspace.ExecutorRuntimePaths("node-1", exec.Skill().Name, accessible)
	if err != nil {
		t.Fatalf("ExecutorRuntimePaths: %v", err)
	}
	if _, err := exec.BindForRunner(nil, runtimePaths); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}
	if err := os.RemoveAll(workspace.ExchangeDir()); err != nil {
		t.Fatalf("RemoveAll(exchange dir): %v", err)
	}

	if _, err := exec.renderStageOutputs("execute", "continue", []string{"downstream"}, "stage body"); err != nil {
		t.Fatalf("renderStageOutputs: %v", err)
	}

	entries, err := os.ReadDir(workspace.ExchangeDir())
	if err != nil {
		t.Fatalf("ReadDir(exchange): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("exchange entry count = %d, want 1", len(entries))
	}
}

func TestFormatParentHandoffsWithPlainMarkdown(t *testing.T) {
	workspace, err := runnerpkg.ProvisionWorkspace(t.TempDir(), "runner-1", []string{"node-1"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	skillWS, ok := workspace.Skill("node-1")
	if !ok {
		t.Fatal("workspace.Skill(node-1) = false")
	}
	parentDir := filepath.Join(skillWS.UpstreamDir, "parent-1")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(parentDir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentDir, "handoff.md"), []byte("plain handoff body"), 0o644); err != nil {
		t.Fatalf("WriteFile(handoff.md): %v", err)
	}
	accessible, err := workspace.AccessibleDirs("node-1")
	if err != nil {
		t.Fatalf("AccessibleDirs: %v", err)
	}
	runtimePaths, err := workspace.ExecutorRuntimePaths("node-1", "skill-1", accessible)
	if err != nil {
		t.Fatalf("ExecutorRuntimePaths: %v", err)
	}

	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{id: "node-1"}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.BindForRunner(nil, runtimePaths); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}

	got := exec.formatParentHandoffs(map[string]any{"parent-1": "fallback"})
	if !strings.Contains(got, "### parent-1\n\nplain handoff body") {
		t.Fatalf("formatParentHandoffs = %q, want upstream plain markdown", got)
	}
}

func TestPolishPlan_BumpsVersionAndFillsPlan(t *testing.T) {
	planner := &ExecutionPlanner{Version: 1}
	exec, err := NewExecutor(loadLightweightTestSkill(t), planner, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if err := exec.PolishPlan(); err != nil {
		t.Fatalf("PolishPlan: %v", err)
	}
	if planner.Version != 2 {
		t.Errorf("Version = %d, want 2", planner.Version)
	}
	if planner.Reason == "" || planner.Solution == "" {
		t.Errorf("expected planner Reason and Solution to be set, got %+v", planner)
	}
}

func TestExecute_UsesRunEWhenSet(t *testing.T) {
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	called := false
	exec.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
		called = true
		return "ok", nil, nil
	}
	out, nodes, err := exec.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !called {
		t.Error("expected RunE to be invoked")
	}
	if out != "ok" || nodes != nil {
		t.Errorf("Execute returned (%v, %v), want (\"ok\", nil)", out, nodes)
	}
}

func TestExecute_NoRunEReturnsMarkdownFallback(t *testing.T) {
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.BindForRunner(nil, runnerpkg.ExecutorRuntimePaths{}); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}
	out, nodes, err := exec.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if nodes != nil {
		t.Fatalf("nodes = %v, want nil", nodes)
	}
	outStr, ok := out.(string)
	if !ok {
		t.Fatalf("Execute output type = %T, want string; value = %v", out, out)
	}
	doc, err := runnerpkg.ParseHandoffMarkdown(outStr)
	if err != nil {
		t.Fatalf("ParseHandoffMarkdown: %v", err)
	}
	if doc.Intent != "continue" {
		t.Fatalf("Intent = %q, want continue", doc.Intent)
	}
	if strings.TrimSpace(doc.Body) == "" {
		t.Fatal("expected fallback handoff to be populated")
	}
}

func TestExecute_NoRunERequiresRunnerBinding(t *testing.T) {
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, _, err := exec.Execute(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "has not been bound") {
		t.Fatalf("Execute error = %v, want unbound runtime skill error", err)
	}
}

func TestExecutionPromptDoesNotExposeArtifactsRoot(t *testing.T) {
	artifactsDir := t.TempDir()
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		TaskDescription: "Write durable outputs.",
		ArtifactsDir:    artifactsDir,
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	prompt := exec.executionPrompt(nil)
	if strings.Contains(prompt, artifactsDir) || strings.Contains(prompt, "Artifacts root:") {
		t.Fatalf("execution prompt exposed artifacts root %q:\n%s", artifactsDir, prompt)
	}
}

func TestExecutionPromptIncludesSourceInputForRootTask(t *testing.T) {
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		TaskDescription: "Normalize groundwater records.",
		SourceInput:     `{"records":[{"site":"Aomori-plain-01"}]}`,
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	prompt := exec.executionPrompt(nil)
	if !strings.Contains(prompt, "Original root request input") || !strings.Contains(prompt, "Aomori-plain-01") {
		t.Fatalf("execution prompt missing source input:\n%s", prompt)
	}
	withParent := exec.executionPrompt(map[string]any{"upstream": "exchange"})
	if strings.Contains(withParent, "Original root request input") {
		t.Fatalf("execution prompt leaked source input when parent exchange exists:\n%s", withParent)
	}
}

func TestExecutionPromptListsRuntimeReferencesWithoutInliningBodies(t *testing.T) {
	workspace, err := runnerpkg.ProvisionWorkspace(t.TempDir(), "runner-1", []string{"parent-1", "node-1"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	exchangeMarkdown, err := (runnerpkg.ExchangeDoc{
		ChannelHeader: streampkg.ChannelHeader{
			RunnerID:  "runner-1",
			CreatedAt: time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
		},
		NodeID:    "parent-1",
		SkillName: "upstream-skill",
		Title:     "Upstream summary",
		Body:      "exchange body should be inspected only by tool",
	}).Markdown()
	if err != nil {
		t.Fatalf("ExchangeDoc Markdown: %v", err)
	}
	exchangePath := filepath.Join(workspace.ExchangeDir(), "parent-1-execute.md")
	if err := os.WriteFile(exchangePath, []byte(exchangeMarkdown), 0o644); err != nil {
		t.Fatalf("WriteFile(exchange): %v", err)
	}
	handoffMarkdown, err := (runnerpkg.HandoffDoc{
		ChannelHeader: streampkg.ChannelHeader{
			RunnerID:  "runner-1",
			CreatedAt: time.Date(2026, 5, 11, 12, 30, 0, 0, time.UTC),
		},
		FromNode: "parent-1",
		ToNodes:  []string{"node-1"},
		Intent:   "continue",
		Artifacts: []runnerpkg.HandoffArtifact{{
			Path:        "results.csv",
			Type:        "file",
			Description: "results",
		}},
		Body: "handoff body should be inspected only by tool",
	}).Markdown()
	if err != nil {
		t.Fatalf("HandoffDoc Markdown: %v", err)
	}
	childWS, ok := workspace.Skill("node-1")
	if !ok {
		t.Fatal("workspace.Skill(node-1) = false")
	}
	parentDir := filepath.Join(childWS.UpstreamDir, "parent-1")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(parentDir): %v", err)
	}
	handoffPath := filepath.Join(parentDir, "handoff.md")
	if err := os.WriteFile(handoffPath, []byte(handoffMarkdown), 0o644); err != nil {
		t.Fatalf("WriteFile(handoff): %v", err)
	}
	accessible, err := workspace.AccessibleDirs("node-1")
	if err != nil {
		t.Fatalf("AccessibleDirs: %v", err)
	}
	runtimePaths, err := workspace.ExecutorRuntimePaths("node-1", "skill-1", accessible)
	if err != nil {
		t.Fatalf("ExecutorRuntimePaths: %v", err)
	}
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		id:              "node-1",
		TaskDescription: "Use upstream references.",
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	exec.runtimePaths = runtimePaths

	prompt := exec.executionPrompt(nil)
	for _, want := range []string{
		"Use tools to inspect exchange and upstream handoff references",
		exchangePath,
		"Upstream summary",
		handoffPath,
		"results.csv",
		"`memo/`",
		"`downstream/artifacts/`",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("execution prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"exchange body should be inspected only by tool",
		"handoff body should be inspected only by tool",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("execution prompt inlined %q:\n%s", forbidden, prompt)
		}
	}
}

func TestExecute_RespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, _, err := exec.Execute(ctx, nil); err == nil {
		t.Fatal("expected Execute to return ctx.Err for canceled context")
	}
	if exec.Status != StatusFailed {
		t.Fatalf("Status = %v, want StatusFailed", exec.Status)
	}
}

func TestLLMClient_ReturnsConfiguredClientBeforeRunnerBind(t *testing.T) {
	client := &llm.Client{}
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(client))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if got := exec.LLMClient(); got != client {
		t.Fatalf("LLMClient() = %p, want %p", got, client)
	}
}

func TestRuntimeSkill_RequiresRunnerBinding(t *testing.T) {
	lightweight := loadLightweightTestSkill(t)
	exec, err := NewExecutor(lightweight, &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.runtimeSkill(); err == nil {
		t.Fatal("expected runtimeSkill to fail before runner binding")
	}
}

func TestBindForRunner_BindsLightweightSkillAndAllocatesSession(t *testing.T) {
	client := &llm.Client{}
	store, err := llm.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	lightweight := loadLightweightTestSkill(t)
	exec, err := NewExecutor(lightweight, &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(client))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	sessionID, err := exec.BindForRunner(nil, runnerpkg.ExecutorRuntimePaths{}, store)
	if err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}
	if sessionID == "" {
		t.Fatal("BindForRunner returned an empty session id")
	}
	runtimeSkill, err := exec.runtimeSkill()
	if err != nil {
		t.Fatalf("runtimeSkill: %v", err)
	}
	if runtimeSkill == lightweight {
		t.Fatal("runtimeSkill returned the original lightweight skill, want bound copy")
	}
	if runtimeSkill.Client() != client {
		t.Fatalf("runtimeSkill client = %p, want %p", runtimeSkill.Client(), client)
	}
	if conv := runtimeSkill.Conversation(); conv == nil || conv.SessionID() != sessionID {
		t.Fatalf("conversation/session = %v, want session %q", conv, sessionID)
	}
}

func TestBindForRunner_ReusesExistingSession(t *testing.T) {
	client := &llm.Client{}
	store, err := llm.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(client))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	firstSessionID, err := exec.BindForRunner(nil, runnerpkg.ExecutorRuntimePaths{}, store)
	if err != nil {
		t.Fatalf("BindForRunner(first): %v", err)
	}
	secondSessionID, err := exec.BindForRunner(&firstSessionID, runnerpkg.ExecutorRuntimePaths{}, store)
	if err != nil {
		t.Fatalf("BindForRunner(second): %v", err)
	}
	if secondSessionID != firstSessionID {
		t.Fatalf("session id = %q, want %q", secondSessionID, firstSessionID)
	}
}

func TestBindForRunnerStoresRuntimePathsAndConfiguresAccessibleDirs(t *testing.T) {
	resourceDir := t.TempDir()
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	runtimePaths := runnerpkg.ExecutorRuntimePaths{
		RunnerID:       "runner-1",
		NodeID:         "node-1",
		SkillName:      exec.Skill().Name,
		AccessibleDirs: []string{resourceDir},
	}
	if _, err := exec.BindForRunner(nil, runtimePaths); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}
	if exec.runtime == nil {
		t.Fatal("runtime skill is nil")
	}
	if got := exec.runtimePaths; got.RunnerID != "runner-1" || got.NodeID != "node-1" || got.SkillName != exec.Skill().Name {
		t.Fatalf("executor runtime paths = %+v", got)
	}
	if got := exec.runtimePaths.AccessibleDirs; len(got) != 1 || got[0] != resourceDir {
		t.Fatalf("executor runtime AccessibleDirs = %v, want [%s]", got, resourceDir)
	}
	if got := exec.runtime.AccessibleDirs(); len(got) != 1 {
		t.Fatalf("runtime AccessibleDirs() = %v, want one dir", got)
	}
	if instructions := exec.runtime.Conversation().Instructions(); !strings.Contains(instructions, resourceDir) {
		t.Fatalf("runtime instructions missing resource dir %q:\n%s", resourceDir, instructions)
	}
}

func TestPolish_ReturnsHandoffMarkdown(t *testing.T) {
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		id:              "node-1",
		TaskDescription: "Plan the work.",
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.BindForRunner(nil, runnerpkg.ExecutorRuntimePaths{}); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}

	fragment, err := exec.Polish(context.Background())
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	doc, err := runnerpkg.ParseHandoffMarkdown(fragment.(string))
	if err != nil {
		t.Fatalf("ParseHandoffMarkdown: %v", err)
	}
	if doc.Intent != "review" || doc.FromNode != "node-1" {
		t.Fatalf("doc metadata = %+v, want review/node-1", doc)
	}
	if len(doc.ToNodes) != 1 || doc.ToNodes[0] != "orchestrator" {
		t.Fatalf("to_nodes = %+v, want orchestrator", doc.ToNodes)
	}
	if strings.TrimSpace(doc.Body) == "" {
		t.Fatalf("doc handoff not populated: %+v", doc)
	}
}

func TestPolish_UsesRuntimeSkillWhenBound(t *testing.T) {
	clearLLMEnv(t)
	polishDoc := "Use the GP package environment after elevation enrichment."

	var requestBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"id":         "polish-response",
			"object":     "response",
			"created_at": 0,
			"model":      "test-model",
			"status":     "completed",
			"output": []map[string]any{{
				"id":     "polish-message",
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        polishDoc,
					"annotations": []any{},
				}},
			}},
			"parallel_tool_calls": false,
			"tool_choice":         "auto",
			"tools":               []any{},
		}); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	raw := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(srv.URL+"/"))
	client, err := llm.NewClient(llm.ProviderOpenAI, "test-model", raw, llm.DefaultClientConfig())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		id:              "node-1",
		TaskDescription: "Train a GP-style groundwater model.",
	}, llm.DefaultConversationConfig(), WithExecutorClient(client))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.BindForRunner(nil, runnerpkg.ExecutorRuntimePaths{}); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}

	fragment, err := exec.Polish(context.Background())
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	doc, err := runnerpkg.ParseHandoffMarkdown(fragment.(string))
	if err != nil {
		t.Fatalf("ParseHandoffMarkdown: %v", err)
	}
	if doc.Body != "Use the GP package environment after elevation enrichment." {
		t.Fatalf("handoff = %q, want skill polish output", doc.Body)
	}
	if !strings.Contains(requestBody, "Polish the assigned task plan before execution") {
		t.Fatalf("polish request missing polish prompt: %s", requestBody)
	}
}

func TestReport_ReturnsHandoffMarkdownFallback(t *testing.T) {
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		id:              "node-1",
		TaskDescription: "Report the work.",
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.BindForRunner(nil, runnerpkg.ExecutorRuntimePaths{}); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}

	summary, err := exec.Report(context.Background(), map[string]string{"result": "ok"})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	doc, err := runnerpkg.ParseHandoffMarkdown(summary.(string))
	if err != nil {
		t.Fatalf("ParseHandoffMarkdown: %v", err)
	}
	if doc.Intent != "summarize" {
		t.Fatalf("Intent = %q, want summarize", doc.Intent)
	}
	if strings.TrimSpace(doc.Body) == "" {
		t.Fatal("expected report handoff to include output")
	}
}

func TestRenderStageOutputsWritesSingleHandoffExchangeEnvelopeAndMemoSnapshot(t *testing.T) {
	workspace, err := runnerpkg.ProvisionWorkspace(t.TempDir(), "runner-1", []string{"node-1"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	skillWS, ok := workspace.Skill("node-1")
	if !ok {
		t.Fatal("workspace.Skill(node-1) = false")
	}
	if err := os.WriteFile(filepath.Join(skillWS.MemoDir, "plan.md"), []byte("memo body"), 0o644); err != nil {
		t.Fatalf("WriteFile(memo): %v", err)
	}
	externalMemo := filepath.Join(t.TempDir(), "external.md")
	if err := os.WriteFile(externalMemo, []byte("external"), 0o644); err != nil {
		t.Fatalf("WriteFile(external memo): %v", err)
	}
	symlinkMemo := filepath.Join(skillWS.MemoDir, "external_link.md")
	if err := os.Symlink(externalMemo, symlinkMemo); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			t.Skipf("symlink not permitted: %v", err)
		}
		t.Fatalf("Symlink(external memo): %v", err)
	}
	accessible, err := workspace.AccessibleDirs("node-1")
	if err != nil {
		t.Fatalf("AccessibleDirs: %v", err)
	}
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		id:              "node-1",
		TaskDescription: "Plan and execute.",
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	runtimePaths, err := workspace.ExecutorRuntimePaths("node-1", exec.Skill().Name, accessible)
	if err != nil {
		t.Fatalf("ExecutorRuntimePaths: %v", err)
	}
	if _, err := exec.BindForRunner(nil, runtimePaths); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}

	const body = "stage body"
	handoffMarkdown, err := exec.renderStageOutputs("execute", "continue", []string{"downstream"}, body)
	if err != nil {
		t.Fatalf("renderStageOutputs: %v", err)
	}
	if got := strings.Count(handoffMarkdown, "---\n"); got != 2 {
		t.Fatalf("returned handoff fence count = %d, want 2:\n%s", got, handoffMarkdown)
	}
	if strings.Contains(handoffMarkdown, "kind: exchange") {
		t.Fatalf("returned handoff markdown contains nested exchange front matter:\n%s", handoffMarkdown)
	}
	handoffRaw, err := os.ReadFile(filepath.Join(skillWS.DownstreamDir, "handoff.md"))
	if err != nil {
		t.Fatalf("ReadFile(downstream handoff): %v", err)
	}
	if got := strings.Count(string(handoffRaw), "---\n"); got != 2 {
		t.Fatalf("handoff fence count = %d, want 2:\n%s", got, string(handoffRaw))
	}
	if strings.Contains(string(handoffRaw), "kind: exchange") {
		t.Fatalf("handoff markdown contains nested exchange front matter:\n%s", string(handoffRaw))
	}
	handoff, err := runnerpkg.ParseHandoffMarkdown(string(handoffRaw))
	if err != nil {
		t.Fatalf("ParseHandoffMarkdown(downstream): %v", err)
	}
	if handoff.FromNode != "node-1" {
		t.Fatalf("handoff.FromNode = %q, want node-1", handoff.FromNode)
	}
	if handoff.Body != body {
		t.Fatalf("handoff.Body = %q, want %q", handoff.Body, body)
	}
	exchangeEntries, err := os.ReadDir(workspace.ExchangeDir())
	if err != nil {
		t.Fatalf("ReadDir(exchange): %v", err)
	}
	if len(exchangeEntries) != 1 {
		t.Fatalf("exchange entry count = %d, want 1", len(exchangeEntries))
	}
	exchangeRaw, err := os.ReadFile(filepath.Join(workspace.ExchangeDir(), exchangeEntries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile(exchange): %v", err)
	}
	if got := strings.Count(string(exchangeRaw), "---\n"); got != 2 {
		t.Fatalf("exchange fence count = %d, want 2:\n%s", got, string(exchangeRaw))
	}
	if strings.Contains(string(exchangeRaw), "kind: handoff") {
		t.Fatalf("exchange markdown contains nested handoff front matter:\n%s", string(exchangeRaw))
	}
	exchange, err := runnerpkg.ParseExchangeDocMarkdown(string(exchangeRaw))
	if err != nil {
		t.Fatalf("ParseExchangeDocMarkdown(exchange): %v", err)
	}
	if exchange.Body != body {
		t.Fatalf("exchange.Body = %q, want %q", exchange.Body, body)
	}
	archiveMemo := filepath.Join(workspace.ArchiveDir(), "memo", "node-1", "execute", "plan.md.memo.md")
	if _, err := os.Stat(archiveMemo); err != nil {
		t.Fatalf("memo snapshot stat(%s): %v", archiveMemo, err)
	}
	archiveLinkedMemo := filepath.Join(workspace.ArchiveDir(), "memo", "node-1", "execute", "external_link.md.memo.md")
	if _, err := os.Stat(archiveLinkedMemo); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink memo should be skipped, stat err = %v", err)
	}
}
