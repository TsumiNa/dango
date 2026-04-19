package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tsumina/dango/internal/llm/skill"
)

// NewWriteFile returns a Tool that writes UTF-8 content to a file within
// root, creating parent directories as needed. Existing files are
// overwritten.
func NewWriteFile(root string) skill.Tool {
	return skill.NewFuncTool(
		"write_file",
		"Write UTF-8 text content to a file within the skill workspace, creating parent directories as needed. Overwrites existing files.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file relative to the skill workspace root.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The file content to write.",
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("write_file: parse arguments: %w", err)
			}
			p, err := skill.ResolveWorkspacePath(root, args.Path)
			if err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			if err := os.WriteFile(p, []byte(args.Content), 0o644); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
		},
	)
}
