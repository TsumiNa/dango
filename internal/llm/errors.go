package llm

import "fmt"

// CannotProceedError reports that a hook-backed stage could not produce a valid result.
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
