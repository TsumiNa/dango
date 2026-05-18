package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// newPipelineSearchReplace returns a Tool that replaces text in a single file
// after resolving the path through the workspace.
func newPipelineSearchReplace(ws workspace) tool {
	return newFuncTool(
		"pipeline_search_replace",
		"Replace text in a file inside the skill temp playground, source workspace, or user-added accessible directories. This is the ResolvePath-bounded equivalent of sed -i 's/find/replace/g' path. By default pattern is a literal substring; set regex=true to use a Go regular expression. max_replacements=0 means replace all matches.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
				"pattern": map[string]any{
					"type":        "string",
					"description": "Literal substring to replace, or a Go regular expression when regex is true.",
				},
				"replacement": map[string]any{
					"type":        "string",
					"description": "Replacement text. In regex mode, Go regexp replacement expansion is applied.",
				},
				"regex": map[string]any{
					"type":        "boolean",
					"description": "Treat pattern as a Go regular expression when true. Default false.",
				},
				"max_replacements": map[string]any{
					"type":        "integer",
					"description": "Maximum number of replacements. Default 0 replaces all matches.",
					"minimum":     0,
				},
			},
			"required":             []string{"path", "pattern", "replacement"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path            string `json:"path"`
				Pattern         string `json:"pattern"`
				Replacement     string `json:"replacement"`
				Regex           bool   `json:"regex,omitempty"`
				MaxReplacements int    `json:"max_replacements,omitempty"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("pipeline_search_replace: parse arguments: %w", err)
			}
			if args.Pattern == "" {
				return "", fmt.Errorf("pipeline_search_replace: pattern is required")
			}
			if args.MaxReplacements < 0 {
				return "", fmt.Errorf("pipeline_search_replace: max_replacements must be >= 0")
			}
			p, err := ws.ResolvePath(args.Path)
			if err != nil {
				return "", fmt.Errorf("pipeline_search_replace: %w", err)
			}
			info, err := os.Stat(p)
			if err != nil {
				return "", fmt.Errorf("pipeline_search_replace: %w", err)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("pipeline_search_replace: %w", err)
			}
			content := string(data)
			updated, replacements, err := replacePipelineMatches(content, args.Pattern, args.Replacement, args.Regex, args.MaxReplacements)
			if err != nil {
				return "", err
			}
			if replacements > 0 {
				if err := os.WriteFile(p, []byte(updated), info.Mode()); err != nil {
					return "", fmt.Errorf("pipeline_search_replace: %w", err)
				}
			}
			return fmt.Sprintf("replaced %d occurrence(s) in %s", replacements, args.Path), nil
		},
	)
}

func replacePipelineMatches(content, pattern, replacement string, regex bool, maxReplacements int) (string, int, error) {
	if !regex {
		count := strings.Count(content, pattern)
		if maxReplacements > 0 && count > maxReplacements {
			count = maxReplacements
		}
		if count == 0 {
			return content, 0, nil
		}
		return strings.Replace(content, pattern, replacement, count), count, nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", 0, fmt.Errorf("pipeline_search_replace: invalid regex: %w", err)
	}
	matches := re.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content, 0, nil
	}
	if maxReplacements > 0 && len(matches) > maxReplacements {
		matches = matches[:maxReplacements]
	}

	var out []byte
	last := 0
	for _, match := range matches {
		out = append(out, content[last:match[0]]...)
		out = re.ExpandString(out, replacement, content, match)
		last = match[1]
	}
	out = append(out, content[last:]...)
	return string(out), len(matches), nil
}
