package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	builtinpromptspkg "github.com/tsumina/dango/internal/engine/builtin/prompts"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
)

const defaultExecutionEffort llm.ReasoningEffort = llm.ReasoningEffortMedium

var (
	executorPromptRendererOnce sync.Once
	executorPromptRendererInst *builtinpromptspkg.Renderer
	executorPromptRendererErr  error
)

func executorPromptRenderer() (*builtinpromptspkg.Renderer, error) {
	executorPromptRendererOnce.Do(func() {
		executorPromptRendererInst, executorPromptRendererErr = builtinpromptspkg.NewRenderer()
	})
	return executorPromptRendererInst, executorPromptRendererErr
}

func (e *Executor) polishExchange(ctx context.Context) (string, error) {
	defaultBody := strings.TrimSpace(fmt.Sprintf("Task description:\n\n%s\n\nPlanner version: %d\n\nReason:\n%s\n\nSolution:\n%s",
		e.planner.TaskDescription,
		e.planner.Version,
		e.planner.Reason,
		e.planner.Solution,
	))
	runtime, ok, err := e.runnableRuntimeSkill()
	if err != nil {
		return "", err
	}
	body := defaultBody
	if ok {
		raw, runErr := runtime.Run(normalizeContext(ctx), e.polishPrompt(), defaultExecutionEffort)
		if runErr != nil {
			return "", runErr
		}
		body = strings.TrimSpace(raw)
		if body == "" {
			body = defaultBody
		}
	}
	return e.renderStageOutputs("polish", "review", []string{"orchestrator"}, body, runtime)
}

func (e *Executor) executeExchange(ctx context.Context, parentOutputs map[string]any) (string, error) {
	fallback := fallbackExecutionHandoff(e.planner.TaskDescription, parentOutputs)
	runtime, ok, err := e.runnableRuntimeSkill()
	if err != nil {
		return "", err
	}
	body := fallback
	if ok {
		raw, runErr := runtime.Run(normalizeContext(ctx), e.executionPrompt(parentOutputs), defaultExecutionEffort)
		if runErr != nil {
			return "", runErr
		}
		body = strings.TrimSpace(raw)
		if body == "" {
			body = fallback
		}
	}
	return e.renderStageOutputs("execute", "continue", []string{"downstream"}, body, runtime)
}

func (e *Executor) reportExchange(ctx context.Context, output any) (string, error) {
	defaultBody := strings.TrimSpace(formatAny(output))
	runtime, ok, err := e.runnableRuntimeSkill()
	if err != nil {
		return "", err
	}
	body := defaultBody
	if ok {
		raw, runErr := runtime.Run(normalizeContext(ctx), e.reportPrompt(output), defaultExecutionEffort)
		if runErr != nil {
			return "", runErr
		}
		body = strings.TrimSpace(raw)
		if body == "" {
			body = defaultBody
		}
	}
	return e.renderStageOutputs("report", "summarize", []string{"orchestrator"}, body, runtime)
}

func (e *Executor) renderStageOutputs(stage string, intent string, toNodes []string, body string, runtime *llm.Skill) (string, error) {
	paths := e.workspacePaths()
	runnerID := paths.runnerID
	if runnerID == "" {
		runnerID = "runner"
	}
	nodeID := ""
	skillName := ""
	if e.planner != nil {
		nodeID = e.planner.id
	}
	if e.skill != nil {
		skillName = e.skill.Name
	}
	if nodeID == "" {
		nodeID = "node"
	}
	doc := runnerpkg.HandoffDoc{
		RunnerID:  runnerID,
		FromNode:  nodeID,
		ToNodes:   append([]string(nil), toNodes...),
		Intent:    intent,
		CreatedAt: time.Now(),
		Body:      strings.TrimSpace(body),
	}
	handoffMarkdown, err := doc.Markdown()
	if err != nil {
		return "", err
	}
	if paths.outboxDir != "" {
		if err := os.MkdirAll(paths.outboxDir, 0o755); err != nil {
			return "", fmt.Errorf("orchestrate: create outbox dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(paths.outboxDir, "handoff.md"), []byte(handoffMarkdown), 0o644); err != nil {
			return "", fmt.Errorf("orchestrate: write handoff markdown: %w", err)
		}
	}
	if paths.exchangeDir != "" {
		fileName := fmt.Sprintf("%s-%s-%d.md", stage, nodeID, time.Now().UnixNano())
		exchangeMarkdown, exchangeErr := e.exchangeDocMarkdown(runnerID, nodeID, skillName, stage, body)
		if exchangeErr != nil {
			return "", exchangeErr
		}
		if err := os.WriteFile(filepath.Join(paths.exchangeDir, fileName), []byte(exchangeMarkdown), 0o644); err != nil {
			return "", fmt.Errorf("orchestrate: write exchange markdown: %w", err)
		}
		if e.planner != nil && e.planner.ArtifactsDir != "" {
			exchangesDir := filepath.Join(e.planner.ArtifactsDir, "exchanges")
			if err := os.MkdirAll(exchangesDir, 0o755); err != nil {
				return "", fmt.Errorf("orchestrate: create artifact exchanges dir: %w", err)
			}
			if err := os.WriteFile(filepath.Join(exchangesDir, fileName), []byte(exchangeMarkdown), 0o644); err != nil {
				return "", fmt.Errorf("orchestrate: write artifact exchange markdown: %w", err)
			}
		}
	}
	if err := e.snapshotMemos(stage, paths, nodeID, skillName, runnerID); err != nil {
		return "", err
	}
	reasoning := ""
	if runtime != nil {
		reasoning = latestReasoning(runtime)
	}
	return e.legacyExchangeMarkdown(stage, runnerID, nodeID, skillName, toNodes, body, reasoning)
}

func (e *Executor) exchangeDocMarkdown(runnerID string, nodeID string, skillName string, stage string, body string) (string, error) {
	exchange := runnerpkg.ExchangeDoc{
		RunnerID:  runnerID,
		NodeID:    nodeID,
		SkillName: skillName,
		Title:     stage,
		CreatedAt: time.Now(),
		Body:      strings.TrimSpace(body),
	}
	return exchange.Markdown()
}

func (e *Executor) legacyExchangeMarkdown(stage string, runnerID string, nodeID string, skillName string, toNodes []string, body string, reasoning string) (string, error) {
	taskDescription := ""
	if e.planner != nil {
		taskDescription = e.planner.TaskDescription
	}
	doc := runnerpkg.ExchangeDocument{
		RunnerID:        runnerID,
		NodeID:          nodeID,
		SkillName:       skillName,
		TaskDescription: taskDescription,
		Memo:            "Handoff emitted through workspace outbox.",
		Reasoning:       strings.TrimSpace(reasoning),
		Handoff:         strings.TrimSpace(body),
	}
	switch stage {
	case "polish":
		doc.Stage = runnerpkg.ExchangeStagePolish
	case "report":
		doc.Stage = runnerpkg.ExchangeStageReport
	default:
		doc.Stage = runnerpkg.ExchangeStageExecute
	}
	for _, to := range toNodes {
		handoff := runnerpkg.ExchangeHandoff{
			To:      runnerpkg.ExchangeRecipientDownstream,
			Intent:  runnerpkg.ExchangeIntentContinue,
			Summary: "Use this output for the next execution stage.",
		}
		if to == "orchestrator" {
			handoff.To = runnerpkg.ExchangeRecipientOrchestrator
			if stage == "polish" {
				handoff.Intent = runnerpkg.ExchangeIntentReview
				handoff.Summary = "Review the polished plan handoff."
			} else {
				handoff.Intent = runnerpkg.ExchangeIntentSummarize
				handoff.Summary = "Summarize this report handoff."
			}
		}
		doc.Handoffs = append(doc.Handoffs, handoff)
	}
	return doc.Markdown()
}

func (e *Executor) snapshotMemos(stage string, paths executorWorkspacePaths, nodeID string, skillName string, runnerID string) error {
	if paths.memoDir == "" || paths.archiveMemoDir == "" {
		return nil
	}
	stageRoot := filepath.Join(paths.archiveMemoDir, stage)
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return fmt.Errorf("orchestrate: create memo snapshot dir: %w", err)
	}
	return filepath.WalkDir(paths.memoDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(paths.memoDir, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc := runnerpkg.MemoDocument{
			RunnerID:  runnerID,
			NodeID:    nodeID,
			SkillName: skillName,
			Path:      filepath.ToSlash(filepath.Join("memo", rel)),
			CreatedAt: time.Now(),
			Body:      string(body),
		}
		raw, err := doc.Markdown()
		if err != nil {
			return err
		}
		dst := filepath.Join(stageRoot, rel+".md")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, []byte(raw), 0o644)
	})
}

type executorWorkspacePaths struct {
	memoDir        string
	inboxDir       string
	outboxDir      string
	exchangeDir    string
	archiveMemoDir string
	runnerID       string
}

func (e *Executor) workspacePaths() executorWorkspacePaths {
	var paths executorWorkspacePaths
	for _, dir := range e.accessibleDirs {
		switch filepath.Base(dir) {
		case "memo":
			paths.memoDir = dir
		case "inbox":
			paths.inboxDir = dir
		case "outbox":
			paths.outboxDir = dir
		case "exchange":
			paths.exchangeDir = dir
		}
	}
	if paths.memoDir != "" && e.planner != nil {
		skillRoot := filepath.Dir(paths.memoDir)
		skillsRoot := filepath.Dir(skillRoot)
		runnerRoot := filepath.Dir(skillsRoot)
		paths.archiveMemoDir = filepath.Join(runnerRoot, "archive", "memo", e.planner.id)
		base := filepath.Base(runnerRoot)
		if strings.HasPrefix(base, "task_") {
			paths.runnerID = strings.TrimPrefix(base, "task_")
		}
	}
	return paths
}

func (e *Executor) runnableRuntimeSkill() (*llm.Skill, bool, error) {
	runtime, err := e.runtimeSkill()
	if err != nil {
		return nil, false, err
	}
	client := runtime.Client()
	if client == nil || client.Model() == "" {
		return runtime, false, nil
	}
	return runtime, true, nil
}

func (e *Executor) executionPrompt(parentOutputs map[string]any) string {
	parentHandoffs := e.formatParentHandoffs(parentOutputs)
	sourceInput := ""
	if e.planner != nil {
		sourceInput = e.planner.SourceInput
	}
	renderer, err := executorPromptRenderer()
	if err != nil {
		return "Execute the assigned task."
	}
	task := ""
	artifactsDir := ""
	if e.planner != nil {
		task = e.planner.TaskDescription
		artifactsDir = e.planner.ArtifactsDir
	}
	out, renderErr := renderer.RenderExecute(builtinpromptspkg.ExecuteData{
		TaskDescription: task,
		SourceInput:     sourceInput,
		ParentHandoffs:  parentHandoffs,
		ArtifactsDir:    artifactsDir,
		AccessibleDirs:  append([]string(nil), e.accessibleDirs...),
	})
	if renderErr != nil {
		return "Execute the assigned task."
	}
	return out
}

func (e *Executor) polishPrompt() string {
	renderer, err := executorPromptRenderer()
	if err != nil {
		return "Polish the assigned task plan before execution."
	}
	var task, reason, solution string
	var version uint32
	if e.planner != nil {
		task = e.planner.TaskDescription
		reason = e.planner.Reason
		solution = e.planner.Solution
		version = e.planner.Version
	}
	out, renderErr := renderer.RenderPolish(builtinpromptspkg.PolishData{
		TaskDescription: task,
		Reason:          reason,
		Solution:        solution,
		Version:         version,
	})
	if renderErr != nil {
		return "Polish the assigned task plan before execution."
	}
	return out
}

func (e *Executor) reportPrompt(output any) string {
	renderer, err := executorPromptRenderer()
	if err != nil {
		return "Summarize this executor output for final orchestration."
	}
	out, renderErr := renderer.RenderReport(builtinpromptspkg.ReportData{
		Output: formatAny(output),
	})
	if renderErr != nil {
		return "Summarize this executor output for final orchestration."
	}
	return out
}

func (e *Executor) formatParentHandoffs(parentOutputs map[string]any) string {
	paths := e.workspacePaths()
	if paths.inboxDir != "" {
		raw, err := readParentHandoffsFromInbox(paths.inboxDir)
		if err == nil && strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	return formatParentOutputs(parentOutputs)
}

func readParentHandoffsFromInbox(inboxDir string) (string, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		return "", err
	}
	type handoffEntry struct {
		from string
		body string
	}
	list := make([]handoffEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		handoffPath := filepath.Join(inboxDir, entry.Name(), "handoff.md")
		raw, readErr := os.ReadFile(handoffPath)
		if readErr != nil {
			continue
		}
		body := strings.TrimSpace(string(raw))
		if parsed, parseErr := runnerpkg.ParseHandoffMarkdown(body); parseErr == nil && parsed != nil {
			body = strings.TrimSpace(parsed.Body)
		}
		if body == "" {
			continue
		}
		list = append(list, handoffEntry{from: entry.Name(), body: body})
	}
	if len(list) == 0 {
		return "No parent handoffs.", nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].from < list[j].from })
	var b strings.Builder
	for _, item := range list {
		b.WriteString("### ")
		b.WriteString(item.from)
		b.WriteString("\n\n")
		b.WriteString(item.body)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func latestReasoning(sk *llm.Skill) string {
	if sk == nil || sk.Conversation() == nil {
		return ""
	}
	turns := sk.Conversation().Turns()
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == llm.RoleReasoning && turns[i].Text != "" {
			return turns[i].Text
		}
	}
	return ""
}

func fallbackExecutionHandoff(task string, parentOutputs map[string]any) string {
	var b strings.Builder
	b.WriteString("Task accepted for execution without a runnable LLM client.\n\n")
	if task != "" {
		b.WriteString("Task:\n")
		b.WriteString(task)
		b.WriteString("\n\n")
	}
	if len(parentOutputs) > 0 {
		b.WriteString("Parent outputs:\n")
		b.WriteString(formatParentOutputs(parentOutputs))
	}
	return strings.TrimSpace(b.String())
}

func formatParentOutputs(parentOutputs map[string]any) string {
	if len(parentOutputs) == 0 {
		return "No parent handoffs."
	}
	keys := make([]string, 0, len(parentOutputs))
	for key := range parentOutputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString("### ")
		b.WriteString(key)
		b.WriteString("\n\n")
		b.WriteString(formatAny(parentOutputs[key]))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func formatAny(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		buf, err := json.MarshalIndent(value, "", "  ")
		if err == nil {
			return string(buf)
		}
		return fmt.Sprintf("%v", value)
	}
}
