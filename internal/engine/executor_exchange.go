package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
)

const defaultExecutionEffort llm.ReasoningEffort = llm.ReasoningEffortMedium

func (e *Executor) polishExchange(ctx context.Context) (string, error) {
	defaults := e.exchangeDocument(runnerpkg.ExchangeStagePolish, []runnerpkg.ExchangeHandoff{{
		To:      runnerpkg.ExchangeRecipientOrchestrator,
		Intent:  runnerpkg.ExchangeIntentReview,
		Summary: "Review this executor's refined task plan before execution.",
	}})
	defaults.Memo = fmt.Sprintf("Task description:\n\n%s\n\nPlanner version: %d", e.planner.TaskDescription, e.planner.Version)
	defaults.Reasoning = e.planner.Reason
	defaults.Handoff = e.planner.Solution

	runtime, ok, err := e.runnableRuntimeSkill()
	if err != nil {
		return "", err
	}
	if !ok {
		return defaults.Markdown()
	}
	raw, err := runtime.Run(normalizeContext(ctx), e.polishPrompt(), defaultExecutionEffort)
	if err != nil {
		return "", err
	}
	return normalizeSkillExchange(raw, defaults, runtime)
}

func (e *Executor) executeExchange(ctx context.Context, parentOutputs map[string]any) (string, error) {
	defaults := e.exchangeDocument(runnerpkg.ExchangeStageExecute, []runnerpkg.ExchangeHandoff{{
		To:      runnerpkg.ExchangeRecipientDownstream,
		Intent:  runnerpkg.ExchangeIntentContinue,
		Summary: "Use this execution output as context for dependent skills.",
	}})
	defaults.Memo = "Execution started from the assigned task and parent markdown handoffs."

	runtime, ok, err := e.runnableRuntimeSkill()
	if err != nil {
		return "", err
	}
	if !ok {
		defaults.Handoff = fallbackExecutionHandoff(e.planner.TaskDescription, parentOutputs)
		return defaults.Markdown()
	}

	raw, err := runtime.Run(normalizeContext(ctx), e.executionPrompt(parentOutputs), defaultExecutionEffort)
	if err != nil {
		return "", err
	}
	return normalizeSkillExchange(raw, defaults, runtime)
}

func (e *Executor) reportExchange(ctx context.Context, output any) (string, error) {
	defaults := e.exchangeDocument(runnerpkg.ExchangeStageReport, []runnerpkg.ExchangeHandoff{{
		To:      runnerpkg.ExchangeRecipientOrchestrator,
		Intent:  runnerpkg.ExchangeIntentSummarize,
		Summary: "Use this report in final request synthesis.",
	}})
	defaults.Memo = "Report generated from this executor's execution output."

	runtime, ok, err := e.runnableRuntimeSkill()
	if err != nil {
		return "", err
	}
	if !ok {
		defaults.Handoff = formatAny(output)
		return defaults.Markdown()
	}

	raw, err := runtime.Run(normalizeContext(ctx), e.reportPrompt(output), defaultExecutionEffort)
	if err != nil {
		return "", err
	}
	return normalizeSkillExchange(raw, defaults, runtime)
}

func (e *Executor) exchangeDocument(stage runnerpkg.ExchangeStage, handoffs []runnerpkg.ExchangeHandoff) runnerpkg.ExchangeDocument {
	doc := runnerpkg.ExchangeDocument{
		Stage:    stage,
		Handoffs: append([]runnerpkg.ExchangeHandoff(nil), handoffs...),
	}
	if e.planner != nil {
		doc.NodeID = e.planner.id
		doc.TaskDescription = e.planner.TaskDescription
	}
	if e.skill != nil {
		doc.SkillName = e.skill.Name
	}
	return doc
}

func (e *Executor) runnableRuntimeSkill() (*llm.Skill, bool, error) {
	runtime, err := e.runtimeSkill()
	if err != nil {
		return nil, false, nil
	}
	client := runtime.Client()
	if client == nil || client.Model() == "" {
		return runtime, false, nil
	}
	return runtime, true, nil
}

func (e *Executor) executionPrompt(parentOutputs map[string]any) string {
	var b strings.Builder
	b.WriteString("Execute the assigned task.\n\n")
	b.WriteString("Tool budget is limited. Trust your SKILL.md and the Workspace access block already in this conversation: ")
	b.WriteString("do not call pwd, do not list_dir on paths already named, and do not re-read SKILL.md or your own scripts. ")
	b.WriteString("If your SKILL.md documents a canonical script entrypoint that fits this task, run it directly with one bash call and pass the parent handoff verbatim on stdin when the script accepts it. ")
	b.WriteString("Treat the script's stdout as the authoritative result — do not re-read or paginate output files just to confirm them.\n\n")
	b.WriteString("Return exactly one Dango exchange markdown document with YAML front matter. ")
	b.WriteString("Use the Memo section for progress/state, Reasoning for a short summary of what you ran, and Handoff for the output intended for the listed recipients (paste structured tool output verbatim inside a fenced code block).\n\n")
	b.WriteString("Task:\n")
	b.WriteString(e.planner.TaskDescription)
	if strings.TrimSpace(e.planner.SourceInput) != "" && len(parentOutputs) == 0 {
		b.WriteString("\n\nOriginal request input for this root task:\n")
		b.WriteString(e.planner.SourceInput)
	}
	b.WriteString("\n\nParent exchange documents:\n")
	b.WriteString(formatParentOutputs(parentOutputs))
	if e.planner.ArtifactsDir != "" {
		b.WriteString("\n\nArtifacts root:\n")
		b.WriteString(e.planner.ArtifactsDir)
		b.WriteString("\nUse a skill-specific subdirectory under this root for durable files. Include generated files in exchange front matter resources.\n")
	}
	if len(e.accessibleDirs) > 0 {
		b.WriteString("\n\nAccessible resource directories from parent exchange front matter:\n")
		for _, dir := range e.accessibleDirs {
			b.WriteString("- ")
			b.WriteString(dir)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (e *Executor) polishPrompt() string {
	var b strings.Builder
	b.WriteString("Polish the assigned task plan before execution.\n\n")
	b.WriteString("This is a feasibility review only. Do not call any tools, do not run scripts, do not write files. ")
	b.WriteString("Return immediately with one Dango exchange markdown document with YAML front matter. ")
	b.WriteString("Use the Memo section for readiness notes, Reasoning for a concise feasibility summary, ")
	b.WriteString("and Handoff for the revised execution approach intended for orchestrator review. ")
	b.WriteString("If the task is outside your skill's scope, say so plainly in the Handoff and recommend the right skill.\n\n")
	b.WriteString("Task:\n")
	b.WriteString(e.planner.TaskDescription)
	b.WriteString("\n\nCurrent planner draft:\n")
	b.WriteString("Reason:\n")
	b.WriteString(e.planner.Reason)
	b.WriteString("\n\nSolution:\n")
	b.WriteString(e.planner.Solution)
	b.WriteString(fmt.Sprintf("\n\nPlanner version: %d", e.planner.Version))
	return b.String()
}

func (e *Executor) reportPrompt(output any) string {
	var b strings.Builder
	b.WriteString("Summarize this executor output for final orchestration.\n\n")
	b.WriteString("Do not call execution tools and do not regenerate artifacts. ")
	b.WriteString("Return one Dango exchange markdown document with YAML front matter, routed to the orchestrator. ")
	b.WriteString("Keep the Handoff to a short summary plus any artifact paths from the executor output.\n\n")
	b.WriteString("Executor output:\n")
	b.WriteString(formatAny(output))
	return b.String()
}

func normalizeSkillExchange(raw string, defaults runnerpkg.ExchangeDocument, runtime *llm.Skill) (string, error) {
	normalized, err := runnerpkg.NormalizeExchangeMarkdown(raw, defaults)
	if err != nil {
		return "", err
	}
	doc, err := runnerpkg.ParseExchangeMarkdown(normalized)
	if err != nil {
		return "", err
	}
	if doc.Reasoning == "" {
		doc.Reasoning = latestReasoning(runtime)
	}
	return doc.Markdown()
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
		return "No parent outputs."
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
