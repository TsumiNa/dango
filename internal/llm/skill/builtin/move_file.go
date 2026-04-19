package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tsumina/dango/internal/llm/skill"
)

// NewMoveFile returns a Tool that renames or moves a file or directory
// within the skill workspace.
//
// Both source and destination must resolve inside root; cross-workspace
// moves are rejected. The destination's parent directory is created if it
// does not exist. This tool fills the gap left by omitting mv from the
// default bash allowlist.
func NewMoveFile(root string) skill.Tool {
	return skill.NewFuncTool(
		"move_file",
		"Rename or move a file or directory within the skill workspace. Both src and dst must stay inside the workspace root; the destination's parent directory is created if needed.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"src": map[string]any{
					"type":        "string",
					"description": "Source path relative to the workspace root.",
				},
				"dst": map[string]any{
					"type":        "string",
					"description": "Destination path relative to the workspace root.",
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
			srcAbs, err := skill.ResolveWorkspacePath(root, args.Src)
			if err != nil {
				return "", fmt.Errorf("move_file: src: %w", err)
			}
			dstAbs, err := skill.ResolveWorkspacePath(root, args.Dst)
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
