package skill

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Tool is the contract the [Agent] uses to invoke a single function tool.
//
// A Tool exposes the metadata needed to advertise itself to the LLM and a
// handler that executes a single call. Implementations must be safe for
// concurrent use when the same instance is shared across multiple [Agent]
// runs. Handlers should return a compact string representation of the tool's
// output; that string is sent back to the model verbatim as the
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
		return "", fmt.Errorf("skill: tool %q has no handler", t.name)
	}
	return t.handler(ctx, arguments)
}

// ResolveWorkspacePath resolves rel against root and ensures the cleaned
// result stays inside root. It returns a cleaned absolute path on success
// and is the standard helper that built-in filesystem tools (and
// third-party tools that want the same containment guarantees) should use
// to validate user-supplied paths.
//
// rel must be non-empty and relative; absolute paths and parent traversals
// that escape root are rejected.
func ResolveWorkspacePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the workspace root", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	cleaned := filepath.Clean(filepath.Join(absRoot, rel))
	relCheck, err := filepath.Rel(absRoot, cleaned)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return cleaned, nil
}
