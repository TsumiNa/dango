package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// newWriteFile returns a Tool that writes UTF-8 content to a file within the
// temp playground, source workspace, or a user-added accessible directory,
// creating parent directories as needed. Existing files are overwritten.
func newWriteFile(ws workspace) tool {
	return newFuncTool(
		"write_file",
		"Write UTF-8 text content to a file in the skill temp playground, source workspace, or user-added accessible directories, creating parent directories as needed. Relative paths resolve in the temp playground; absolute paths must stay inside one of those roots. Overwrites existing files.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
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
			p, err := ws.ResolvePath(args.Path)
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
