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

// ExecutorDetailPlanInput contains the render data for the built-in executor detail-planning prompt.
type ExecutorDetailPlanInput struct {
	TaskID           string
	SubTask          string
	ToolJSON         string
	ToolConfigYAML   string
	InputContextJSON string
}

// ExecutorExecuteInput contains the render data for the built-in executor execute-generation prompt.
type ExecutorExecuteInput struct {
	TaskID              string
	SubTask             string
	ToolJSON            string
	ToolConfigYAML      string
	InputContextJSON    string
	ExpectedOutputsJSON string
}

// RenderExecutorDetailPlan renders the built-in executor detail-planning prompt.
func RenderExecutorDetailPlan(input ExecutorDetailPlanInput) (string, error) {
	return renderPromptTemplate("executor_detail_plan", executorDetailPlanPrompt, input)
}

// RenderExecutorExecute renders the built-in executor execute-generation prompt.
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
