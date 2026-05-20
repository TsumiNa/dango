package engine

import (
	"context"
	"fmt"
	"strings"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
)

const defaultExecutionEffort llm.ReasoningEffort = llm.ReasoningEffortMedium

// Execute runs the task. When [Agent.RunE] is set it is invoked directly;
// otherwise Execute asks the bound runtime skill for handoff content and writes
// stage artifacts through the runner workspace channels.
func (e *Agent) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			e.Status = StatusFailed
			return nil, nil, err
		}
	}
	e.logf("Executing tasks...")
	e.Status = StatusRunning

	if e.RunE != nil {
		output, newNodes, err := e.RunE(ctx, parentOutputs)
		if err != nil {
			e.Status = StatusFailed
			return nil, nil, err
		}
		e.captureResult(output)
		e.Status = StatusDone
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				e.Status = StatusFailed
				return nil, nil, err
			}
		}
		return output, newNodes, nil
	}
	output, err := e.runExecuteStage(ctx, parentOutputs)
	if err != nil {
		e.Status = StatusFailed
		return nil, nil, err
	}
	e.captureResult(output)
	e.Status = StatusDone
	return output, nil, nil
}

// Polish satisfies the runner's polish contract. The default implementation
// refreshes the planner via [Agent.PolishPlan] and returns handoff markdown
// for orchestrator review.
func (e *Agent) Polish(ctx context.Context) (any, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := e.PolishPlan(); err != nil {
		return nil, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return e.runPolishStage(ctx)
}

// Report satisfies the runner's report contract. The default implementation
// returns handoff markdown summarizing the execution output.
func (e *Agent) Report(ctx context.Context, output any) (any, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return e.runReportStage(ctx, output)
}

func (e *Agent) runPolishStage(ctx context.Context) (string, error) {
	var task, reason, solution string
	var version uint32
	if e.planner != nil {
		task = e.planner.TaskDescription
		version = e.planner.Version
		reason = e.planner.Reason
		solution = e.planner.Solution
	}
	defaultBody := strings.TrimSpace(fmt.Sprintf("Task description:\n\n%s\n\nPlanner version: %d\n\nReason:\n%s\n\nSolution:\n%s",
		task,
		version,
		reason,
		solution,
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
	return e.renderStageOutputs("polish", "review", []string{"orchestrator"}, body)
}

func (e *Agent) runExecuteStage(ctx context.Context, parentOutputs map[string]any) (string, error) {
	task := ""
	if e.planner != nil {
		task = e.planner.TaskDescription
	}
	fallback := fallbackExecutionHandoff(task, parentOutputs)
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
	return e.renderStageOutputs("execute", "continue", []string{"downstream"}, body)
}

func (e *Agent) runReportStage(ctx context.Context, output any) (string, error) {
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
	return e.renderStageOutputs("report", "summarize", []string{"orchestrator"}, body)
}

func (e *Agent) runnableRuntimeSkill() (*llm.Skill, bool, error) {
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
