package builtin

import (
	"context"
	"encoding/json"
	"fmt"
)

type tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, arguments string) (string, error)
}

type workspace interface {
	WorkDir() string
	SkillRoot() string
	TempRoot() string
	AccessibleDirs() []string
	ResolvePath(rel string) (string, error)
}

type funcTool struct {
	name        string
	description string
	parameters  map[string]any
	handler     func(ctx context.Context, arguments string) (string, error)
}

func newFuncTool(name, description string, parameters map[string]any, handler func(ctx context.Context, arguments string) (string, error)) *funcTool {
	return &funcTool{
		name:        name,
		description: description,
		parameters:  cloneMap(parameters),
		handler:     handler,
	}
}

func (t *funcTool) Name() string               { return t.name }
func (t *funcTool) Description() string        { return t.description }
func (t *funcTool) Parameters() map[string]any { return cloneMap(t.parameters) }
func (t *funcTool) Execute(ctx context.Context, arguments string) (string, error) {
	if t.handler == nil {
		return "", fmt.Errorf("builtin: tool %q has no handler", t.name)
	}
	return t.handler(ctx, arguments)
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return m
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
