package engine

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	builtinpromptspkg "github.com/tsumina/dango/internal/engine/builtin/prompts"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
)

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
		if strings.HasPrefix(body, "---\n") || strings.HasPrefix(body, "---\r\n") {
			if parsed, parseErr := runnerpkg.ParseHandoffMarkdown(body); parseErr == nil && parsed != nil {
				body = strings.TrimSpace(parsed.Body)
			}
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
