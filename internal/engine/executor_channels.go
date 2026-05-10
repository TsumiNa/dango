package engine

import (
	"context"
	"encoding/json"
	"fmt"
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

func (e *Executor) promptRenderer() (*builtinpromptspkg.Renderer, error) {
	if len(e.promptTemplateOverrides) == 0 {
		return executorPromptRenderer()
	}
	return builtinpromptspkg.NewRenderer(builtinpromptspkg.WithTemplateOverrides(e.promptTemplateOverrides))
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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
	paths := e.currentRuntimePaths()
	runnerID := paths.RunnerID
	nodeID := paths.NodeID
	skillName := paths.SkillName
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
	if paths.DownstreamDir != "" {
		if err := os.MkdirAll(paths.DownstreamDir, 0o755); err != nil {
			return "", fmt.Errorf("orchestrate: create downstream dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(paths.DownstreamDir, "handoff.md"), []byte(handoffMarkdown), 0o644); err != nil {
			return "", fmt.Errorf("orchestrate: write handoff markdown: %w", err)
		}
	}
	if paths.ExchangeDir != "" {
		fileName := fmt.Sprintf("%s-%s-%d.md", stage, nodeID, time.Now().UnixNano())
		exchangeMarkdown, exchangeErr := e.exchangeDocMarkdown(runnerID, nodeID, skillName, stage, body)
		if exchangeErr != nil {
			return "", exchangeErr
		}
		if err := os.WriteFile(filepath.Join(paths.ExchangeDir, fileName), []byte(exchangeMarkdown), 0o644); err != nil {
			return "", fmt.Errorf("orchestrate: write exchange markdown: %w", err)
		}
	}
	if err := e.snapshotMemos(stage, paths); err != nil {
		return "", err
	}
	return handoffMarkdown, nil
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
	renderer, err := e.promptRenderer()
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
		AccessibleDirs:  append([]string(nil), e.currentRuntimePaths().AccessibleDirs...),
	})
	if renderErr != nil {
		return "Execute the assigned task."
	}
	return out
}

func (e *Executor) polishPrompt() string {
	renderer, err := e.promptRenderer()
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
	renderer, err := e.promptRenderer()
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
	paths := e.currentRuntimePaths()
	if paths.UpstreamDir != "" {
		raw, err := readParentHandoffsFromUpstream(paths.UpstreamDir)
		if err == nil && strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	return formatParentOutputs(parentOutputs)
}

func readParentHandoffsFromUpstream(upstreamDir string) (string, error) {
	entries, err := os.ReadDir(upstreamDir)
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
		handoffPath := filepath.Join(upstreamDir, entry.Name(), "handoff.md")
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
