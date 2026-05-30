package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// newMoveFile returns a Tool that renames or moves a file or directory
// within the skill workspace.
//
// Both source and destination must resolve inside the temp playground, source
// workspace, or user-added accessible directories; cross-workspace moves are
// rejected. The destination's parent directory is created if it does not
// exist. This tool fills the gap left by omitting mv from the default bash
// allowlist.
func newMoveFile(ws workspace) tool {
	return newFuncTool(
		"move_file",
		"Rename or move a file or directory within the skill temp playground, source workspace, or user-added accessible directories. Relative paths resolve in the temp playground; absolute paths must stay inside one of those roots. The destination's parent directory is created if needed.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"src": map[string]any{
					"type":        "string",
					"description": "Source path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
				"dst": map[string]any{
					"type":        "string",
					"description": "Destination path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
			},
			"required":             []string{"src", "dst"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Src string `json:"src"`
				Dst string `json:"dst"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("move_file: parse arguments: %w", err)
			}
			srcAbs, err := ws.ResolvePath(args.Src)
			if err != nil {
				return "", fmt.Errorf("move_file: src: %w", err)
			}
			dstAbs, err := ws.ResolvePath(args.Dst)
			if err != nil {
				return "", fmt.Errorf("move_file: dst: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
				return "", fmt.Errorf("move_file: %w", err)
			}
			if err := os.Rename(srcAbs, dstAbs); err != nil {
				return "", fmt.Errorf("move_file: %w", err)
			}
			return fmt.Sprintf("moved %s -> %s", args.Src, args.Dst), nil
		},
	)
}
