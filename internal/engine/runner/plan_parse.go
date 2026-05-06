package runner

import (
	"strings"
	"unicode"
)

// ExtractJSONObject returns the outermost balanced JSON object embedded in raw,
// stripping markdown code fences, leading prose, and trailing commentary. It
// exists so planner/reviewer/replanner outputs survive the common LLM mistake
// of wrapping a JSON answer in ```json … ``` or prefixing it with a
// short narrative even when the prompt forbids both.
//
// Returns the raw JSON substring on success, or an empty string when no
// plausible object is found. The caller is responsible for json.Unmarshal.
func ExtractJSONObject(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	stripped := stripCodeFences(trimmed)
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return ""
	}
	if obj := extractBalancedObject(stripped); obj != "" {
		return obj
	}
	return ""
}

// SummarizeRaw returns a short, single-line preview of raw suitable for
// embedding in error messages. It collapses whitespace and clips to roughly
// max characters so logs stay scannable when an LLM returns a wall of text.
func SummarizeRaw(raw string, max int) string {
	if max <= 0 {
		max = 200
	}
	collapsed := strings.Join(strings.Fields(raw), " ")
	if len(collapsed) <= max {
		return collapsed
	}
	return collapsed[:max] + "…"
}

func stripCodeFences(text string) string {
	// Only act when the text actually opens with a fence; otherwise we'd
	// corrupt JSON whose final characters happen to look like a closing
	// fence.
	if !strings.HasPrefix(text, "```") {
		return text
	}
	rest := strings.TrimPrefix(text, "```")
	// Drop an optional language tag on the same line ("json", "JSON", etc.).
	if idx := strings.IndexByte(rest, '\n'); idx >= 0 {
		first := strings.TrimSpace(rest[:idx])
		if first == "" || isIdentifier(first) {
			rest = rest[idx+1:]
		}
	}
	if end := strings.LastIndex(rest, "```"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func isIdentifier(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func extractBalancedObject(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' {
			if inString {
				escape = true
			}
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}
