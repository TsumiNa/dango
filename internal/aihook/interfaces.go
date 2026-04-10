package aihook

import (
	"context"

	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/taskflow"
)

// Module identifies the subsystem that owns a hook.
type Module string

const (
	// ModuleOrchestrator identifies control-plane hooks.
	ModuleOrchestrator Module = "orchestrator"
	// ModuleRunner identifies execution-plane planning hooks.
	ModuleRunner Module = "runner"
	// ModuleExecutor identifies executor-local hooks.
	ModuleExecutor Module = "executor"
)

// Kind identifies one AI-assisted hook category.
type Kind string

const (
	// KindIntentUnderstanding interprets inbound user requests.
	KindIntentUnderstanding Kind = "intent_understanding"
	// KindDraftPlanning creates an initial executable draft plan.
	KindDraftPlanning Kind = "draft_planning"
	// KindReviewPlanning validates or adjusts a plan before execution.
	KindReviewPlanning Kind = "review_planning"
	// KindRepairPlanning repairs a plan after review or execution feedback.
	KindRepairPlanning Kind = "repair_planning"
	// KindDetailPlanning refines one executor-owned stage into an executable plan.
	KindDetailPlanning Kind = "detail_planning"
	// KindExecuteGeneration generates execute-time scripts or glue logic.
	KindExecuteGeneration Kind = "execute_generation"
)

// InstructionQuery identifies the instruction set requested for a hook.
type InstructionQuery struct {
	Module Module
	Kind   Kind
	Name   string
}

// InstructionDocument is one user- or repo-provided instruction payload.
type InstructionDocument struct {
	Name        string         `json:"name,omitempty" yaml:"name,omitempty"`
	Source      string         `json:"source,omitempty" yaml:"source,omitempty"`
	FrontMatter map[string]any `json:"frontmatter,omitempty" yaml:"frontmatter,omitempty"`
	Body        string         `json:"body" yaml:"body"`
}

// InstructionSet contains the layered instructions resolved for one hook.
type InstructionSet struct {
	Documents []InstructionDocument `json:"documents,omitempty" yaml:"documents,omitempty"`
}

// InstructionProvider resolves future external instruction overrides.
type InstructionProvider interface {
	Load(ctx context.Context, query InstructionQuery) (InstructionSet, error)
}

// IntentRequest is the input to an orchestrator intent-understanding hook.
type IntentRequest struct {
	Request taskflow.RequestEnvelope `json:"request"`
	Entry   taskflow.RequestMetadata `json:"entry"`
}

// IntentResult is the structured output of intent understanding.
type IntentResult struct {
	Request  taskflow.RequestEnvelope `json:"request"`
	Metadata map[string]string        `json:"metadata,omitempty"`
	Summary  string                   `json:"summary,omitempty"`
}

// IntentUnderstandingHook interprets an inbound user request.
type IntentUnderstandingHook interface {
	Understand(ctx context.Context, request IntentRequest) (IntentResult, error)
}

// ToolCatalogEntry describes one registered tool exposed to planning hooks.
type ToolCatalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	InputTypes  []string `json:"input_types"`
	OutputTypes []string `json:"output_types"`
	Model       string   `json:"model,omitempty"`
}

// DraftPlanRequest is the input to a runner draft-planning hook.
type DraftPlanRequest struct {
	TaskID  string                   `json:"task_id"`
	Request taskflow.RequestEnvelope `json:"request"`
	Tools   []ToolCatalogEntry       `json:"tools"`
}

// DraftPlanningHook creates an initial executable DAG plan.
type DraftPlanningHook interface {
	Draft(ctx context.Context, request DraftPlanRequest) (spec.DAGPlan, error)
}

// ReviewPlanRequest is the input to a runner review hook.
type ReviewPlanRequest struct {
	TaskID  string                   `json:"task_id"`
	Request taskflow.RequestEnvelope `json:"request"`
	Tools   []ToolCatalogEntry       `json:"tools,omitempty"`
	Plan    spec.DAGPlan             `json:"plan"`
}

// ReviewPlanningHook validates or adjusts a plan before execution.
type ReviewPlanningHook interface {
	Review(ctx context.Context, request ReviewPlanRequest) (spec.DAGPlan, error)
}

// RepairPlanRequest is the input to a runner repair hook.
type RepairPlanRequest struct {
	TaskID  string                   `json:"task_id"`
	Request taskflow.RequestEnvelope `json:"request"`
	Tools   []ToolCatalogEntry       `json:"tools,omitempty"`
	Plan    spec.DAGPlan             `json:"plan"`
	Reason  string                   `json:"reason,omitempty"`
}

// RepairPlanningHook repairs a plan after review or execution feedback.
type RepairPlanningHook interface {
	Repair(ctx context.Context, request RepairPlanRequest) (spec.DAGPlan, error)
}

// DetailPlanningRequest is the input to an executor detail-planning hook.
type DetailPlanningRequest struct {
	TaskID     string        `json:"task_id"`
	SubTask    string        `json:"sub_task"`
	Tool       spec.ToolSpec `json:"tool"`
	ToolConfig string        `json:"tool_config,omitempty"`
	InputPath  string        `json:"input_path,omitempty"`
	InputURL   string        `json:"input_url,omitempty"`
}

// DetailPlanningHook refines an executor-owned stage into a structured plan.
type DetailPlanningHook interface {
	Plan(ctx context.Context, request DetailPlanningRequest) (spec.ExecutorPlan, error)
}

// GeneratedArtifact describes one execute-time generated file.
type GeneratedArtifact struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Private     bool   `json:"private,omitempty"`
	Content     string `json:"content,omitempty"`
}

// ExecuteGenerationRequest is the input to an executor generation hook.
type ExecuteGenerationRequest struct {
	TaskID            string        `json:"task_id"`
	SubTask           string        `json:"sub_task"`
	Tool              spec.ToolSpec `json:"tool"`
	ToolConfig        string        `json:"tool_config,omitempty"`
	InputPath         string        `json:"input_path,omitempty"`
	InputURL          string        `json:"input_url,omitempty"`
	PublicOutputPath  string        `json:"public_output_path,omitempty"`
	PrivateOutputPath string        `json:"private_output_path,omitempty"`
}

// ExecuteGenerationResult is the structured output of execute-time generation.
type ExecuteGenerationResult struct {
	Summary            string              `json:"summary,omitempty"`
	HandoffBody        string              `json:"handoff_body,omitempty"`
	ExpectedOutputs    []string            `json:"expected_outputs,omitempty"`
	GeneratedArtifacts []GeneratedArtifact `json:"generated_artifacts,omitempty"`
}

// ExecuteGenerationHook generates execute-time scripts or glue logic.
type ExecuteGenerationHook interface {
	Generate(ctx context.Context, request ExecuteGenerationRequest) (ExecuteGenerationResult, error)
}
