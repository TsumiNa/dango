package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	builtininstructions "github.com/tsumina/dango/internal/engine/builtin/instructions"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
)

func (e *Executor) executionPrompt(parentOutputs map[string]any) string {
	task := ""
	sourceInput := ""
	if e.planner != nil {
		task = e.planner.TaskDescription
		sourceInput = e.planner.SourceInput
	}
	prompt := e.stagePrompt("execute", task)
	if sourceInput != "" && len(parentOutputs) == 0 && !e.hasUpstreamHandoffReferences() {
		prompt = appendMarkdownSection(prompt, "Original root request input", sourceInput)
	}
	return prompt
}

func (e *Executor) polishPrompt() string {
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

func (e *Executor) reportPrompt(output any) string {
	return appendMarkdownSection(e.stagePrompt("report", "Summarize this executor output."), "Executor output", formatAny(output))
}

func (e *Executor) stagePrompt(stage string, task string) string {
	note, err := builtininstructions.StageNote(stage)
	if err != nil {
		note = "# " + stage + " stage"
	}
	var b strings.Builder
	b.WriteString(note)
	b.WriteString("\n\n")
	b.WriteString(e.runtimeContextMarkdown())
	b.WriteString("\n\n")
	b.WriteString("## Stage task\n\n")
	if strings.TrimSpace(task) == "" {
		b.WriteString("No task description was provided.")
	} else {
		b.WriteString(strings.TrimSpace(task))
	}
	b.WriteString("\n\n")
	b.WriteString(e.upstreamHandoffReferencesMarkdown())
	return strings.TrimSpace(b.String())
}

func (e *Executor) runtimeContextMarkdown() string {
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
	b.WriteString(e.exchangeReferencesMarkdown())
	return strings.TrimSpace(b.String())
}

func (e *Executor) exchangeReferencesMarkdown() string {
	paths := e.currentRuntimePaths()
	if paths.ExchangeDir == "" {
		return "### Exchange references\n\nNo exchange directory is available."
	}
	entries, err := os.ReadDir(paths.ExchangeDir)
	if err != nil {
		return fmt.Sprintf("### Exchange references\n\nExchange directory: `%s`\n\nNo exchange references are currently readable.", paths.ExchangeDir)
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
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
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
	b.WriteString("Inspect these shared public exchange documents with tools only when the stage task needs their contents.\n\n")
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

func (e *Executor) upstreamHandoffReferencesMarkdown() string {
	paths := e.currentRuntimePaths()
	if paths.UpstreamDir == "" {
		return "## Directed upstream handoff references\n\nNo directed upstream handoff directory is available."
	}
	refs := e.upstreamHandoffReferences()
	var b strings.Builder
	b.WriteString("## Directed upstream handoff references\n\n")
	b.WriteString("Inspect these directed upstream handoffs with tools when the task depends on parent output.\n\n")
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

func (e *Executor) hasUpstreamHandoffReferences() bool {
	return len(e.upstreamHandoffReferences()) > 0
}

func (e *Executor) upstreamHandoffReferences() []string {
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
		raw, readErr := os.ReadFile(handoffPath)
		if readErr != nil {
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
