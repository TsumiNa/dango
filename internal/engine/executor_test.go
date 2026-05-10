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
	if _, err := exec.BindForRunner(nil, nil); err != nil {
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

func TestExchangeDocMarkdownStripsNestedHandoffFrontMatter(t *testing.T) {
	handoff, err := (runnerpkg.HandoffDoc{
		RunnerID:  "runner-1",
		FromNode:  "node-1",
		ToNodes:   []string{"downstream"},
		Intent:    "continue",
		CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		Body:      "handoff body",
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

	if got := strings.Count(raw, "---\n"); got != 2 {
		t.Fatalf("front matter fence count = %d, want 2 in one envelope:\n%s", got, raw)
	}
	if strings.Contains(raw, "kind: dango.handoff_doc") {
		t.Fatalf("exchange doc preserved nested handoff front matter:\n%s", raw)
	}
	parsed, err := runnerpkg.ParseExchangeDocMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseExchangeDocMarkdown: %v", err)
	}
	if parsed.Body != "handoff body" {
		t.Fatalf("body = %q, want stripped handoff body", parsed.Body)
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
	if _, err := exec.BindForRunner(nil, nil); err != nil {
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

func TestExecutionPromptUsesAdvancedTemplateOverride(t *testing.T) {
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{
		TaskDescription: "Override task.",
	}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	exec.SetPromptTemplateOverrides(map[string]string{
		"execute.tmpl": "advanced override: {{.TaskDescription}}",
	})

	prompt := exec.executionPrompt(nil)
	if prompt != "advanced override: Override task." {
		t.Fatalf("executionPrompt = %q", prompt)
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
	if !strings.Contains(prompt, "Original request input for this root task:") || !strings.Contains(prompt, "Aomori-plain-01") {
		t.Fatalf("execution prompt missing source input:\n%s", prompt)
	}
	withParent := exec.executionPrompt(map[string]any{"upstream": "exchange"})
	if strings.Contains(withParent, "Original request input for this root task:") {
		t.Fatalf("execution prompt leaked source input when parent exchange exists:\n%s", withParent)
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
	sessionID, err := exec.BindForRunner(nil, nil, store)
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
	firstSessionID, err := exec.BindForRunner(nil, nil, store)
	if err != nil {
		t.Fatalf("BindForRunner(first): %v", err)
	}
	secondSessionID, err := exec.BindForRunner(&firstSessionID, nil, store)
	if err != nil {
		t.Fatalf("BindForRunner(second): %v", err)
	}
	if secondSessionID != firstSessionID {
		t.Fatalf("session id = %q, want %q", secondSessionID, firstSessionID)
	}
}

func TestBindForRunnerConfiguresRuntimeSkillAccessibleDirs(t *testing.T) {
	resourceDir := t.TempDir()
	exec, err := NewExecutor(loadLightweightTestSkill(t), &ExecutionPlanner{}, llm.DefaultConversationConfig(), WithExecutorClient(&llm.Client{}))
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.BindForRunner(nil, []string{resourceDir}); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}
	if exec.runtime == nil {
		t.Fatal("runtime skill is nil")
	}
	if got := exec.accessibleDirs; len(got) != 1 || got[0] != resourceDir {
		t.Fatalf("executor accessibleDirs = %v, want [%s]", got, resourceDir)
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
	if _, err := exec.BindForRunner(nil, nil); err != nil {
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
	if _, err := exec.BindForRunner(nil, nil); err != nil {
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
	if _, err := exec.BindForRunner(nil, nil); err != nil {
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

func TestExecutorStagesWriteWorkspaceHandoffAndMemoSnapshot(t *testing.T) {
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
	if _, err := exec.BindForRunner(nil, accessible); err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}

	if _, err := exec.Polish(context.Background()); err != nil {
		t.Fatalf("Polish: %v", err)
	}
	handoffRaw, err := os.ReadFile(filepath.Join(skillWS.OutboxDir, "handoff.md"))
	if err != nil {
		t.Fatalf("ReadFile(outbox handoff): %v", err)
	}
	handoff, err := runnerpkg.ParseHandoffMarkdown(string(handoffRaw))
	if err != nil {
		t.Fatalf("ParseHandoffMarkdown(outbox): %v", err)
	}
	if handoff.FromNode != "node-1" {
		t.Fatalf("handoff.FromNode = %q, want node-1", handoff.FromNode)
	}
	archiveMemo := filepath.Join(workspace.ArchiveDir(), "memo", "node-1", "polish", "plan.md.memo.md")
	if _, err := os.Stat(archiveMemo); err != nil {
		t.Fatalf("memo snapshot stat(%s): %v", archiveMemo, err)
	}
	archiveLinkedMemo := filepath.Join(workspace.ArchiveDir(), "memo", "node-1", "polish", "external_link.md.memo.md")
	if _, err := os.Stat(archiveLinkedMemo); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink memo should be skipped, stat err = %v", err)
	}
}
