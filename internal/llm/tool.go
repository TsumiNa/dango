package llm

import (
	"context"
	"fmt"
)

// Tool is the contract a [Conversation] uses to invoke a single function
// tool during its tool-calling loop.
//
// A Tool exposes the metadata needed to advertise itself to the LLM and a
// handler that executes a single call. Implementations must be safe for
// concurrent use when the same instance is shared across multiple
// conversations. Handlers should return a compact string representation of
// the tool's output; that string is sent back to the model verbatim as the
// function_call_output.
//
// The default set of filesystem and shell tools lives in the
// [github.com/tsumina/dango/internal/llm/skill/builtin] subpackage; callers
// typically wire those alongside any tool they implement themselves.
type Tool interface {
	// Name returns the unique tool name advertised to the model.
	Name() string
	// Description explains to the model when the tool should be used.
	Description() string
	// Parameters returns the JSON Schema object describing the tool arguments.
	Parameters() map[string]any
	// Execute runs the tool with the raw JSON arguments string produced by
	// the model and returns the output reported back to the model.
	Execute(ctx context.Context, arguments string) (string, error)
}

// FuncTool is the default [Tool] implementation, built from a name,
// description, parameter schema, and a handler function.
//
// FuncTool is the preferred way to register new built-in or user-supplied
// tools without declaring a new Go type. The zero value is not usable;
// construct instances with [NewFuncTool].
type FuncTool struct {
	name        string
	description string
	parameters  map[string]any
	handler     func(ctx context.Context, arguments string) (string, error)
}

// NewFuncTool constructs a [FuncTool] from its components.
func NewFuncTool(name, description string, parameters map[string]any, handler func(ctx context.Context, arguments string) (string, error)) *FuncTool {
	return &FuncTool{
		name:        name,
		description: description,
		parameters:  parameters,
		handler:     handler,
	}
}

func (t *FuncTool) Name() string               { return t.name }
func (t *FuncTool) Description() string        { return t.description }
func (t *FuncTool) Parameters() map[string]any { return t.parameters }
func (t *FuncTool) Execute(ctx context.Context, arguments string) (string, error) {
	if t.handler == nil {
		return "", fmt.Errorf("llm: tool %q has no handler", t.name)
	}
	return t.handler(ctx, arguments)
}
