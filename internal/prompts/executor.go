package prompts

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed executor_detail_plan.md
var executorDetailPlanPrompt string

//go:embed executor_execute.md
var executorExecutePrompt string

// ExecutorDetailPlanInput contains the render data for the built-in executor
// detail-planning prompt.
//
// Executor code assembles this input from the current runtime context, the
// merged tool configuration, and a summarized view of any upstream input.
type ExecutorDetailPlanInput struct {
	// TaskID identifies the parent task run.
	TaskID string
	// SubTask is the current stage-local sub-task markdown content.
	SubTask string
	// ToolJSON is the serialized tool spec shown to the model.
	ToolJSON string
	// ToolConfigYAML is the merged tool configuration shown to the model.
	ToolConfigYAML string
	// InputContextJSON summarizes upstream input artifacts available to the executor.
	InputContextJSON string
}

// ExecutorExecuteInput contains the render data for the built-in executor
// execute-generation prompt.
//
// It extends the detail-planning context with explicit expected output hints so
// the model can generate concrete public and private artifacts.
type ExecutorExecuteInput struct {
	// TaskID identifies the parent task run.
	TaskID string
	// SubTask is the current stage-local sub-task markdown content.
	SubTask string
	// ToolJSON is the serialized tool spec shown to the model.
	ToolJSON string
	// ToolConfigYAML is the merged tool configuration shown to the model.
	ToolConfigYAML string
	// InputContextJSON summarizes upstream input artifacts available to the executor.
	InputContextJSON string
	// ExpectedOutputsJSON contains the suggested public output file names.
	ExpectedOutputsJSON string
}

// RenderExecutorDetailPlan renders the built-in executor detail-planning
// prompt.
//
// Callers pass the fully prepared execution context and receive the exact
// system prompt that should be submitted to the LLM client for the
// executor-side refinement stage that turns one DAG edge into a concrete local
// sub-task description and output contract.
func RenderExecutorDetailPlan(input ExecutorDetailPlanInput) (string, error) {
	return renderPromptTemplate("executor_detail_plan", executorDetailPlanPrompt, input)
}

// RenderExecutorExecute renders the built-in executor execute-generation
// prompt.
//
// The rendered prompt is used by the executor's built-in AI execution path to
// request concrete artifacts, downstream-only handoff metadata, and final
// markdown output files from the already refined edge context.
func RenderExecutorExecute(input ExecutorExecuteInput) (string, error) {
	return renderPromptTemplate("executor_execute", executorExecutePrompt, input)
}

func renderPromptTemplate(name string, source string, data any) (string, error) {
	tmpl, err := template.New(name).Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse %s prompt template: %w", name, err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", fmt.Errorf("render %s prompt: %w", name, err)
	}
	return strings.TrimSpace(out.String()), nil
}
