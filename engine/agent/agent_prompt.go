package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	instructionspkg "github.com/tsumina/dango/engine/internal/instructions"
	runnerpkg "github.com/tsumina/dango/engine/runner"
)

func (e *Agent) executionPrompt(parentOutputs map[string]any) string {
	task := ""
	sourceInput := ""
	if e.planner != nil {
		task = e.planner.TaskDescription
		sourceInput = e.planner.SourceInput
	}
	upstreamRefs := e.upstreamHandoffReferences()
	prompt := e.stagePromptWithUpstreamReferences("execute", task, upstreamRefs)
	if sourceInput != "" && len(parentOutputs) == 0 && len(upstreamRefs) == 0 {
		prompt = appendMarkdownSection(prompt, "Original root request input", sourceInput)
	}
	return prompt
}

func (e *Agent) polishPrompt() string {
	var task, reason, solution string
	var version uint32
	if e.planner != nil {
		task = e.planner.TaskDescription
		reason = e.planner.Reason
		solution = e.planner.Solution
		version = e.planner.Version
	}
	prompt := e.stagePrompt("polish", task)
	var draft strings.Builder
	if version != 0 {
		fmt.Fprintf(&draft, "- Version: %d\n", version)
	}
	if reason != "" {
		fmt.Fprintf(&draft, "- Reason: %s\n", reason)
	}
	if solution != "" {
		fmt.Fprintf(&draft, "- Solution: %s\n", solution)
	}
	if strings.TrimSpace(draft.String()) != "" {
		prompt = appendMarkdownSection(prompt, "Current planner draft", draft.String())
	}
	return prompt
}

func (e *Agent) reportPrompt(output any) string {
	return appendMarkdownSection(e.stagePrompt("report", "Summarize this agent output."), "Agent output", formatAny(output))
}

func (e *Agent) stagePrompt(stage string, task string) string {
	return e.stagePromptWithUpstreamReferences(stage, task, e.upstreamHandoffReferences())
}

func (e *Agent) stagePromptWithUpstreamReferences(stage string, task string, upstreamRefs []string) string {
	note, err := instructionspkg.StageNote(stage)
	if err != nil {
		e.logger.Warn("failed to load agent stage note", "stage", stage, "error", err)
		note = "# " + stage + " stage"
	}
	var b strings.Builder
	b.WriteString(note)
	b.WriteString("\n\n")
	b.WriteString(e.runtimeContextMarkdown(stage))
	b.WriteString("\n\n")
	b.WriteString("## Stage task\n\n")
	if strings.TrimSpace(task) == "" {
		b.WriteString("No task description was provided.")
	} else {
		b.WriteString(strings.TrimSpace(task))
	}
	b.WriteString("\n\n")
	b.WriteString(e.upstreamHandoffReferencesMarkdown(stage, upstreamRefs))
	return strings.TrimSpace(b.String())
}

func (e *Agent) runtimeContextMarkdown(stage string) string {
	paths := e.currentRuntimePaths()
	var b strings.Builder
	b.WriteString("## Runtime context\n\n")
	fmt.Fprintf(&b, "- Runner ID: %s\n", paths.RunnerID)
	fmt.Fprintf(&b, "- Node ID: %s\n", paths.NodeID)
	if paths.SkillName != "" {
		fmt.Fprintf(&b, "- Skill name: %s\n", paths.SkillName)
	}
	b.WriteString("\n### Workspace channel paths\n\n")
	writePath := func(label string, path string) {
		if path != "" {
			fmt.Fprintf(&b, "- `%s`: `%s`\n", label, path)
		}
	}
	writePath("memo/", paths.MemoDir)
	writePath("upstream/", paths.UpstreamDir)
	writePath("downstream/", paths.DownstreamDir)
	if paths.DownstreamDir != "" {
		writePath("downstream/artifacts/", filepath.Join(paths.DownstreamDir, "artifacts"))
	}
	writePath("scratch/", paths.ScratchDir)
	writePath("exchange/", paths.ExchangeDir)
	if len(paths.AccessibleDirs) > 0 {
		b.WriteString("\n### Accessible roots\n\n")
		for _, dir := range paths.AccessibleDirs {
			if dir != "" {
				fmt.Fprintf(&b, "- `%s`\n", dir)
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(e.exchangeReferencesMarkdown(stage))
	return strings.TrimSpace(b.String())
}

func (e *Agent) exchangeReferencesMarkdown(stage string) string {
	paths := e.currentRuntimePaths()
	intro := exchangeReferenceInstruction(stage)
	if paths.ExchangeDir == "" {
		return "### Exchange references\n\n" + intro + "\n\nNo exchange directory is available."
	}
	entries, err := os.ReadDir(paths.ExchangeDir)
	if err != nil {
		return fmt.Sprintf("### Exchange references\n\n%s\n\nExchange directory: `%s`\n\nNo exchange references are currently readable.", intro, paths.ExchangeDir)
	}
	type reference struct {
		name string
		line string
	}
	refs := make([]reference, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(paths.ExchangeDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if doc, parseErr := runnerpkg.ParseExchangeDocMarkdown(string(raw)); parseErr == nil {
			line := fmt.Sprintf("- `%s` — node `%s`", path, doc.NodeID)
			if doc.SkillName != "" {
				line += fmt.Sprintf(", skill `%s`", doc.SkillName)
			}
			if doc.Title != "" {
				line += fmt.Sprintf(", title %q", doc.Title)
			}
			if !doc.CreatedAt.IsZero() {
				line += fmt.Sprintf(", created `%s`", doc.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
			}
			refs = append(refs, reference{name: entry.Name(), line: line})
			continue
		}
		refs = append(refs, reference{name: entry.Name(), line: fmt.Sprintf("- `%s` — markdown file with unavailable exchange metadata", path)})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	var b strings.Builder
	b.WriteString("### Exchange references\n\n")
	b.WriteString(intro)
	b.WriteString("\n\n")
	if len(refs) == 0 {
		b.WriteString("No exchange references are currently available.")
		return b.String()
	}
	for _, ref := range refs {
		b.WriteString(ref.line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func (e *Agent) upstreamHandoffReferencesMarkdown(stage string, refs []string) string {
	paths := e.currentRuntimePaths()
	intro := upstreamHandoffReferenceInstruction(stage)
	if paths.UpstreamDir == "" {
		return "## Directed upstream handoff references\n\n" + intro + "\n\nNo directed upstream handoff directory is available."
	}
	var b strings.Builder
	b.WriteString("## Directed upstream handoff references\n\n")
	b.WriteString(intro)
	b.WriteString("\n\n")
	if len(refs) == 0 {
		b.WriteString("No directed upstream handoffs are currently available.")
		return b.String()
	}
	for _, ref := range refs {
		b.WriteString(ref)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func exchangeReferenceInstruction(stage string) string {
	if stage == "execute" {
		return "Inspect these shared public exchange documents with tools only when the stage task needs their contents."
	}
	return "This stage does not allow tool use, so do not inspect exchange documents during this stage."
}

func upstreamHandoffReferenceInstruction(stage string) string {
	if stage == "execute" {
		return "Inspect these directed upstream handoffs with tools when the task depends on parent output."
	}
	return "This stage does not allow tool use, so do not inspect directed upstream handoffs during this stage."
}

func (e *Agent) upstreamHandoffReferences() []string {
	paths := e.currentRuntimePaths()
	if paths.UpstreamDir == "" {
		return nil
	}
	entries, err := os.ReadDir(paths.UpstreamDir)
	if err != nil {
		return nil
	}
	refs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		handoffPath := filepath.Join(paths.UpstreamDir, entry.Name(), "handoff.md")
		raw, err := os.ReadFile(handoffPath)
		if err != nil {
			continue
		}
		line := fmt.Sprintf("- From `%s`: `%s`", entry.Name(), handoffPath)
		if doc, parseErr := runnerpkg.ParseHandoffMarkdown(string(raw)); parseErr == nil {
			line = fmt.Sprintf("- From `%s`: `%s`", doc.FromNode, handoffPath)
			if doc.Intent != "" {
				line += fmt.Sprintf(", intent `%s`", doc.Intent)
			}
			if len(doc.ToNodes) > 0 {
				line += fmt.Sprintf(", to `%s`", strings.Join(doc.ToNodes, "`, `"))
			}
			if !doc.CreatedAt.IsZero() {
				line += fmt.Sprintf(", created `%s`", doc.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
			}
			if len(doc.Artifacts) > 0 {
				line += ", artifacts:"
				for _, artifact := range doc.Artifacts {
					line += fmt.Sprintf(" `%s`", artifact.Path)
				}
			}
		}
		refs = append(refs, line)
	}
	sort.Strings(refs)
	return refs
}

func appendMarkdownSection(base string, heading string, body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return strings.TrimSpace(base)
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(base))
	b.WriteString("\n\n## ")
	b.WriteString(heading)
	b.WriteString("\n\n")
	b.WriteString(trimmed)
	return strings.TrimSpace(b.String())
}
