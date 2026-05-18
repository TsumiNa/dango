package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const fileExcerptDefaultMaxMatches = 5

// newFileExcerpt returns a Tool that extracts matching regions from a file.
func newFileExcerpt(ws workspace) tool {
	return newFuncTool(
		"file_excerpt",
		"Return matching regions from a file inside the skill temp playground, source workspace, or user-added accessible directories. This is the ResolvePath-bounded equivalent of grep -A N -B M path. By default anchor_pattern is a literal substring; set regex=true to use a Go regular expression. max_matches defaults to 5.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
				"anchor_pattern": map[string]any{
					"type":        "string",
					"description": "Substring (default) or Go regular expression that anchors each excerpt region.",
				},
				"before": map[string]any{
					"type":        "integer",
					"description": "Number of lines to include before each matching line. Default 0.",
					"minimum":     0,
				},
				"after": map[string]any{
					"type":        "integer",
					"description": "Number of lines to include after each matching line. Default 0.",
					"minimum":     0,
				},
				"regex": map[string]any{
					"type":        "boolean",
					"description": "Treat anchor_pattern as a Go regular expression when true. Default false.",
				},
				"max_matches": map[string]any{
					"type":        "integer",
					"description": "Cap the number of anchor matches returned. Default 5.",
					"minimum":     1,
				},
			},
			"required":             []string{"path", "anchor_pattern"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path          string `json:"path"`
				AnchorPattern string `json:"anchor_pattern"`
				Before        int    `json:"before,omitempty"`
				After         int    `json:"after,omitempty"`
				Regex         bool   `json:"regex,omitempty"`
				MaxMatches    int    `json:"max_matches,omitempty"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("file_excerpt: parse arguments: %w", err)
			}
			if args.AnchorPattern == "" {
				return "", fmt.Errorf("file_excerpt: anchor_pattern is required")
			}
			if args.Before < 0 {
				return "", fmt.Errorf("file_excerpt: before must be >= 0")
			}
			if args.After < 0 {
				return "", fmt.Errorf("file_excerpt: after must be >= 0")
			}
			maxMatches := args.MaxMatches
			if maxMatches < 0 {
				return "", fmt.Errorf("file_excerpt: max_matches must be >= 0")
			}
			if maxMatches == 0 {
				maxMatches = fileExcerptDefaultMaxMatches
			}

			p, err := ws.ResolvePath(args.Path)
			if err != nil {
				return "", fmt.Errorf("file_excerpt: %w", err)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("file_excerpt: %w", err)
			}
			matcher, err := newLineMatcher(args.AnchorPattern, args.Regex)
			if err != nil {
				return "", fmt.Errorf("file_excerpt: invalid regex: %w", err)
			}
			return renderFileExcerpt(string(data), matcher, args.Before, args.After, maxMatches), nil
		},
	)
}

func renderFileExcerpt(source string, matcher func(string) bool, before, after, maxMatches int) string {
	lines := strings.Split(source, "\n")
	anchors := make(map[int]struct{})
	var windows [][2]int
	matchCount := 0
	truncated := false
	overLimitIndex := len(lines)
	for i, line := range lines {
		if !matcher(line) {
			continue
		}
		matchCount++
		if matchCount > maxMatches {
			truncated = true
			overLimitIndex = i
			break
		}
		anchors[i] = struct{}{}
		start := 0
		if before < i {
			start = i - before
		}
		end := len(lines) - 1
		if after < len(lines)-1-i {
			end = i + after
		}
		windows = append(windows, [2]int{start, end})
	}
	if len(windows) == 0 {
		return ""
	}

	var out []string
	lastPrint := -1
	for _, window := range windows {
		start, end := window[0], window[1]
		if end >= overLimitIndex {
			end = overLimitIndex - 1
		}
		if end < start {
			continue
		}
		if lastPrint >= 0 && start > lastPrint+1 {
			out = append(out, "--")
		}
		if start <= lastPrint {
			start = lastPrint + 1
		}
		for j := start; j <= end; j++ {
			sep := "-"
			if _, ok := anchors[j]; ok {
				sep = ":"
			}
			out = append(out, fmt.Sprintf("%d%s %s", j+1, sep, lines[j]))
		}
		lastPrint = end
	}
	result := strings.Join(out, "\n")
	if truncated {
		remaining := 0
		for _, line := range lines {
			if matcher(line) {
				remaining++
			}
		}
		remaining -= maxMatches
		result += fmt.Sprintf("\n... (truncated at %d matches, %d more)", maxMatches, remaining)
	}
	return result
}
