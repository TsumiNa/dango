package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// newReadFile returns a Tool that reads a file's contents as UTF-8 text. The
// path must resolve inside the skill source root, temp playground, or a
// user-added accessible directory.
//
// When start_line and/or end_line are provided (1-indexed, inclusive) the
// tool returns only that slice of lines, which is useful for sampling long
// files without sending every byte back to the model. end_line is clamped
// to the last line of the file.
func newReadFile(ws workspace) tool {
	return newFuncTool(
		"read_file",
		"Read the contents of a file in the skill temp playground, source workspace, or user-added accessible directories and return it as text. Relative paths resolve in the temp playground; absolute paths must stay inside one of those roots. Optional start_line/end_line (1-indexed, inclusive) return only a slice of lines, useful for long files.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "Optional 1-indexed starting line. When omitted, reading starts at line 1.",
					"minimum":     1,
				},
				"end_line": map[string]any{
					"type":        "integer",
					"description": "Optional 1-indexed end line (inclusive). When omitted, reading goes to the end of the file.",
					"minimum":     1,
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path      string `json:"path"`
				StartLine *int   `json:"start_line,omitempty"`
				EndLine   *int   `json:"end_line,omitempty"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("read_file: parse arguments: %w", err)
			}
			p, err := ws.ResolvePath(args.Path)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			if args.StartLine == nil && args.EndLine == nil {
				return string(data), nil
			}
			lines := strings.Split(string(data), "\n")
			start := 1
			if args.StartLine != nil {
				start = *args.StartLine
			}
			end := len(lines)
			if args.EndLine != nil {
				end = *args.EndLine
			}
			if start < 1 {
				return "", fmt.Errorf("read_file: start_line must be >= 1")
			}
			if end < start {
				return "", fmt.Errorf("read_file: end_line (%d) must be >= start_line (%d)", end, start)
			}
			if start > len(lines) {
				return "", nil
			}
			if end > len(lines) {
				end = len(lines)
			}
			return strings.Join(lines[start-1:end], "\n"), nil
		},
	)
}
