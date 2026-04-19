package builtin

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tsumina/dango/internal/llm"
)

// NewPwd returns a Tool that reports the absolute path of the skill
// workspace root.
func NewPwd(root string) llm.Tool {
	return llm.NewFuncTool(
		"pwd",
		"Return the absolute path of the skill workspace root.",
		map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			abs, err := filepath.Abs(root)
			if err != nil {
				return "", fmt.Errorf("pwd: %w", err)
			}
			return abs, nil
		},
	)
}
