package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// grepDefaultMaxMatches bounds the number of matches returned by the grep
// tool when max_matches is not specified.
const grepDefaultMaxMatches = 50

// newGrep returns a Tool that searches for a pattern in a file or in an
// inline string.
//
// Callers must supply exactly one of path (a file inside the temp playground,
// source workspace, or a user-added accessible directory) or text (an inline
// string). The default matching mode is a case-sensitive literal
// substring; set regex=true to interpret pattern as a Go regular expression.
// Matches are returned one per line, prefixed with the 1-indexed line
// number. context_lines adds surrounding lines to each hit (like grep -C),
// and max_matches caps the number of hits to keep responses small. This
// tool is the preferred way to locate sections of long manuals, READMEs, or
// logs before issuing a targeted [newReadFile] call.
func newGrep(ws workspace) tool {
	return newFuncTool(
		"grep",
		"Search for a pattern in a file or an inline text string and return matching lines with 1-indexed line numbers. Relative file paths resolve in the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories. Supply exactly one of path or text. Use this before read_file to locate sections in long documents.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Substring (default) or Go regular expression to match.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Optional file path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories. Mutually exclusive with text.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Optional inline text to search. Mutually exclusive with path.",
				},
				"regex": map[string]any{
					"type":        "boolean",
					"description": "Treat pattern as a Go regular expression when true. Default false (literal substring).",
				},
				"context_lines": map[string]any{
					"type":        "integer",
					"description": "Include this many lines before and after each match. Default 0.",
					"minimum":     0,
				},
				"max_matches": map[string]any{
					"type":        "integer",
					"description": "Cap the number of matches returned. Default 50.",
					"minimum":     1,
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Pattern      string  `json:"pattern"`
				Path         string  `json:"path,omitempty"`
				Text         *string `json:"text,omitempty"`
				Regex        bool    `json:"regex,omitempty"`
				ContextLines int     `json:"context_lines,omitempty"`
				MaxMatches   int     `json:"max_matches,omitempty"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("grep: parse arguments: %w", err)
			}
			if args.Pattern == "" {
				return "", fmt.Errorf("grep: pattern is required")
			}
			if (args.Path == "" && args.Text == nil) || (args.Path != "" && args.Text != nil) {
				return "", fmt.Errorf("grep: specify exactly one of path or text")
			}
			if args.ContextLines < 0 {
				return "", fmt.Errorf("grep: context_lines must be >= 0")
			}
			maxMatches := args.MaxMatches
			if maxMatches <= 0 {
				maxMatches = grepDefaultMaxMatches
			}

			var source string
			if args.Text != nil {
				source = *args.Text
			} else {
				p, err := ws.ResolvePath(args.Path)
				if err != nil {
					return "", fmt.Errorf("grep: %w", err)
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return "", fmt.Errorf("grep: %w", err)
				}
				source = string(data)
			}

			var matcher func(string) bool
			if args.Regex {
				re, err := regexp.Compile(args.Pattern)
				if err != nil {
					return "", fmt.Errorf("grep: invalid regex: %w", err)
				}
				matcher = re.MatchString
			} else {
				needle := args.Pattern
				matcher = func(line string) bool { return strings.Contains(line, needle) }
			}

			lines := strings.Split(source, "\n")
			var (
				out        []string
				matchCount int
				truncated  bool
				lastPrint  = -1 // highest line index already emitted
			)
			for i, line := range lines {
				if !matcher(line) {
					continue
				}
				matchCount++
				if matchCount > maxMatches {
					truncated = true
					break
				}
				start := i - args.ContextLines
				if start < 0 {
					start = 0
				}
				end := i + args.ContextLines
				if end >= len(lines) {
					end = len(lines) - 1
				}
				// Separate distinct match groups with "--" when context is
				// requested. With no context the flat list needs no separator.
				if lastPrint >= 0 && args.ContextLines > 0 {
					out = append(out, "--")
				}
				if start <= lastPrint {
					start = lastPrint + 1
				}
				for j := start; j <= end; j++ {
					sep := "-"
					if j == i {
						sep = ":"
					}
					out = append(out, fmt.Sprintf("%d%s %s", j+1, sep, lines[j]))
				}
				lastPrint = end
			}
			if len(out) == 0 {
				return "", nil
			}
			result := strings.Join(out, "\n")
			if truncated {
				remaining := 0
				// count any remaining matches beyond the limit for better reporting
				for i := lastPrint + 1; i < len(lines); i++ {
					if matcher(lines[i]) {
						remaining++
					}
				}
				result += fmt.Sprintf("\n... (truncated at %d matches, %d more)", maxMatches, remaining)
			}
			return result, nil
		},
	)
}
