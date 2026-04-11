package llm

import "fmt"

// Module identifies the subsystem that owns an LLM step.
type Module string

const (
	// ModuleOrchestrator identifies the orchestrator control plane.
	ModuleOrchestrator Module = "orchestrator"
	// ModuleRunner identifies the runner execution plane.
	ModuleRunner Module = "runner"
	// ModuleExecutor identifies executor-local LLM steps.
	ModuleExecutor Module = "executor"
)

// Kind identifies one LLM-assisted step category.
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

// CannotProceedError reports that an LLM-assisted stage could not produce a valid result.
type CannotProceedError struct {
	Module  Module
	Kind    Kind
	Message string
	Cause   error
}

// NewCannotProceedError constructs a structured cannot-proceed error.
func NewCannotProceedError(module Module, kind Kind, message string, cause error) *CannotProceedError {
	return &CannotProceedError{
		Module:  module,
		Kind:    kind,
		Message: message,
		Cause:   cause,
	}
}

// Error returns the textual form of the cannot-proceed error.
func (e *CannotProceedError) Error() string {
	if e == nil {
		return "task cannot proceed"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s/%s cannot proceed: %s: %v", e.Module, e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s/%s cannot proceed: %s", e.Module, e.Kind, e.Message)
}

// Unwrap returns the underlying cause.
func (e *CannotProceedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
