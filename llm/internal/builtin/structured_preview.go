package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
)

const (
	structuredPreviewDefaultFormat          = "auto"
	structuredPreviewDefaultMaxKeysPerLevel = 20
	structuredPreviewDefaultMaxDepth        = 3
	structuredPreviewDefaultSampleRows      = 5
	structuredPreviewJSONLMaxRowBytes       = 8 * 1024 * 1024
)

type structuredPreviewConfig struct {
	maxKeysPerLevel int
	maxDepth        int
	sampleRows      int
}

type jsonlFieldStats struct {
	nulls int
	types map[string]struct{}
}

// newStructuredPreview returns a Tool that previews the shape of JSON, JSONL,
// and YAML files without returning the full document body.
func newStructuredPreview(ws workspace) tool {
	return newFuncTool(
		"structured_preview",
		"Preview the shape of a JSON, JSONL, or YAML file with a token-frugal schema sketch. Relative paths resolve in the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
				"format": map[string]any{
					"type":        "string",
					"enum":        []string{"auto", "json", "jsonl", "yaml"},
					"default":     structuredPreviewDefaultFormat,
					"description": "Input format. Defaults to auto and infers from the file extension.",
				},
				"max_keys_per_level": map[string]any{
					"type":        "integer",
					"default":     structuredPreviewDefaultMaxKeysPerLevel,
					"minimum":     1,
					"description": "Maximum number of object keys to preview at each level.",
				},
				"max_depth": map[string]any{
					"type":        "integer",
					"default":     structuredPreviewDefaultMaxDepth,
					"minimum":     0,
					"description": "Maximum object nesting depth to expand.",
				},
				"sample_rows": map[string]any{
					"type":        "integer",
					"default":     structuredPreviewDefaultSampleRows,
					"minimum":     1,
					"description": "Maximum number of JSONL rows to inspect.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path            string `json:"path"`
				Format          string `json:"format,omitempty"`
				MaxKeysPerLevel *int   `json:"max_keys_per_level,omitempty"`
				MaxDepth        *int   `json:"max_depth,omitempty"`
				SampleRows      *int   `json:"sample_rows,omitempty"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("structured_preview: parse arguments: %w", err)
			}
			if args.Path == "" {
				return "", fmt.Errorf("structured_preview: path is required")
			}

			cfg := structuredPreviewConfig{
				maxKeysPerLevel: structuredPreviewDefaultMaxKeysPerLevel,
				maxDepth:        structuredPreviewDefaultMaxDepth,
				sampleRows:      structuredPreviewDefaultSampleRows,
			}
			if args.MaxKeysPerLevel != nil {
				cfg.maxKeysPerLevel = *args.MaxKeysPerLevel
			}
			if args.MaxDepth != nil {
				cfg.maxDepth = *args.MaxDepth
			}
			if args.SampleRows != nil {
				cfg.sampleRows = *args.SampleRows
			}
			if cfg.maxKeysPerLevel < 1 {
				return "", fmt.Errorf("structured_preview: max_keys_per_level must be >= 1")
			}
			if cfg.maxDepth < 0 {
				return "", fmt.Errorf("structured_preview: max_depth must be >= 0")
			}
			if cfg.sampleRows < 1 {
				return "", fmt.Errorf("structured_preview: sample_rows must be >= 1")
			}

			resolvedPath, err := ws.ResolvePath(args.Path)
			if err != nil {
				return "", fmt.Errorf("structured_preview: %w", err)
			}
			data, err := os.ReadFile(resolvedPath)
			if err != nil {
				return "", fmt.Errorf("structured_preview: %w", err)
			}

			format, err := structuredPreviewFormat(args.Format, args.Path)
			if err != nil {
				return "", fmt.Errorf("structured_preview: %w", err)
			}
			switch format {
			case "json":
				var value any
				if err := json.Unmarshal(data, &value); err != nil {
					return "", fmt.Errorf("structured_preview: parse json: %w", err)
				}
				return renderStructuredSketch(value, cfg), nil
			case "jsonl":
				out, err := renderJSONLPreview(data, cfg)
				if err != nil {
					return "", fmt.Errorf("structured_preview: %w", err)
				}
				return out, nil
			case "yaml":
				var value any
				if err := yamlv3.Unmarshal(data, &value); err != nil {
					return "", fmt.Errorf("structured_preview: parse yaml: %w", err)
				}
				return renderStructuredSketch(normalizeYAMLValue(value), cfg), nil
			default:
				return "", fmt.Errorf("structured_preview: unsupported format %q", format)
			}
		},
	)
}

func structuredPreviewFormat(raw string, path string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "" {
		format = structuredPreviewDefaultFormat
	}
	switch format {
	case "json", "jsonl", "yaml":
		return format, nil
	case "auto":
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json":
			return "json", nil
		case ".jsonl", ".ndjson":
			return "jsonl", nil
		case ".yaml", ".yml":
			return "yaml", nil
		default:
			return "", fmt.Errorf("auto format for %q is unknown; set format explicitly", path)
		}
	default:
		return "", fmt.Errorf("format must be one of auto, json, jsonl, yaml")
	}
}

func renderStructuredSketch(value any, cfg structuredPreviewConfig) string {
	switch typed := value.(type) {
	case map[string]any:
		lines := renderStructuredObject(typed, cfg, 0, "", "")
		return strings.Join(lines, "\n")
	default:
		summary, _ := inlineStructuredSummary(value, cfg, 0)
		return summary
	}
}

func renderStructuredObject(value map[string]any, cfg structuredPreviewConfig, depth int, indent string, label string) []string {
	line, _ := objectSummary(value, cfg, depth)
	lines := []string{indent + label + line}
	if depth >= cfg.maxDepth {
		return lines
	}

	keys := previewKeys(value, cfg.maxKeysPerLevel)
	for _, key := range keys {
		child := value[key]
		if childObject, ok := child.(map[string]any); ok {
			childLines := renderStructuredObject(childObject, cfg, depth+1, indent+"  ", key+": ")
			lines = append(lines, childLines...)
			continue
		}
		summary, _ := inlineStructuredSummary(child, cfg, depth+1)
		lines = append(lines, indent+"  "+key+": "+summary)
	}
	return lines
}

func inlineStructuredSummary(value any, cfg structuredPreviewConfig, depth int) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return objectSummary(typed, cfg, depth)
	case []any:
		return arraySummary(typed, cfg, depth)
	case nil:
		return "null", false
	case bool:
		return "boolean", false
	case string:
		return "string", false
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number", false
	default:
		return "unknown", false
	}
}

func objectSummary(value map[string]any, cfg structuredPreviewConfig, depth int) (string, bool) {
	keys := previewKeys(value, cfg.maxKeysPerLevel)
	truncatedKeys := len(value) - len(keys)
	summary := fmt.Sprintf("object{keys:[%s]", strings.Join(keys, ", "))
	if truncatedKeys > 0 {
		summary += fmt.Sprintf(", truncated:%d", truncatedKeys)
	}
	summary += "}"
	truncated := truncatedKeys > 0 || (depth >= cfg.maxDepth && len(value) > 0)
	if truncated {
		summary += " (truncated)"
	}
	return summary, truncated
}

func arraySummary(value []any, cfg structuredPreviewConfig, depth int) (string, bool) {
	elemSummary := "unknown"
	elemTruncated := false
	if len(value) > 0 {
		elemSummary, elemTruncated = arrayElementSummary(value, cfg, depth)
	}
	summary := fmt.Sprintf("array[len=%d, elem:%s]", len(value), elemSummary)
	truncated := elemTruncated || (depth >= cfg.maxDepth && len(value) > 0)
	if truncated {
		summary += " (truncated)"
	}
	return summary, truncated
}

func arrayElementSummary(value []any, cfg structuredPreviewConfig, depth int) (string, bool) {
	if len(value) == 0 {
		return "unknown", false
	}

	typeSet := make(map[string]struct{})
	var firstObject map[string]any
	var firstArray []any
	for _, elem := range value {
		kind := structuredTypeName(elem)
		typeSet[kind] = struct{}{}
		switch typed := elem.(type) {
		case map[string]any:
			if firstObject == nil {
				firstObject = typed
			}
		case []any:
			if firstArray == nil {
				firstArray = typed
			}
		}
	}

	if len(typeSet) == 1 {
		switch {
		case firstObject != nil:
			return objectSummary(firstObject, cfg, depth+1)
		case firstArray != nil:
			return arraySummary(firstArray, cfg, depth+1)
		default:
			types := mapsKeys(typeSet)
			return types[0], false
		}
	}

	types := mapsKeys(typeSet)
	return fmt.Sprintf("mixed[%s]", strings.Join(types, ", ")), false
}

func renderJSONLPreview(data []byte, cfg structuredPreviewConfig) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), structuredPreviewJSONLMaxRowBytes)

	stats := map[string]*jsonlFieldStats{}
	rowsScanned := 0
	rowsTruncated := false
	for scanner.Scan() {
		if rowsScanned >= cfg.sampleRows {
			rowsTruncated = true
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return "", fmt.Errorf("parse jsonl row %d: %w", rowsScanned+1, err)
		}
		row, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("jsonl row %d must be an object", rowsScanned+1)
		}

		present := make(map[string]struct{}, len(row))
		for key, value := range row {
			present[key] = struct{}{}
			fieldStats, ok := stats[key]
			if !ok {
				fieldStats = &jsonlFieldStats{
					nulls: rowsScanned,
					types: map[string]struct{}{},
				}
				stats[key] = fieldStats
			}
			if value == nil {
				fieldStats.nulls++
				fieldStats.types["null"] = struct{}{}
				continue
			}
			fieldStats.types[structuredTypeName(value)] = struct{}{}
		}
		for key, fieldStats := range stats {
			if _, ok := present[key]; !ok {
				fieldStats.nulls++
				fieldStats.types["null"] = struct{}{}
			}
		}
		rowsScanned++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan jsonl: %w", err)
	}

	keys := previewKeys(stats, cfg.maxKeysPerLevel)
	truncatedKeys := len(stats) - len(keys)
	header := fmt.Sprintf("jsonl{rows_scanned:%d, keys:[%s]", rowsScanned, strings.Join(keys, ", "))
	if truncatedKeys > 0 {
		header += fmt.Sprintf(", truncated:%d", truncatedKeys)
	}
	header += "}"
	if rowsTruncated || truncatedKeys > 0 {
		header += " (truncated)"
	}

	lines := []string{header}
	for _, key := range keys {
		fieldStats := stats[key]
		types := mapsKeys(fieldStats.types)
		lines = append(lines, fmt.Sprintf("  %s: types=[%s], null_rate=%.2f", key, strings.Join(types, ", "), nullRate(fieldStats.nulls, rowsScanned)))
	}
	return strings.Join(lines, "\n"), nil
}

func previewKeys[T any](value map[string]T, maxKeys int) []string {
	keys := mapsKeys(value)
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
	}
	return keys
}

func mapsKeys[T any](value map[string]T) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func structuredTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "unknown"
	}
}

func nullRate(nulls int, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(nulls) / float64(total)
}

func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = normalizeYAMLValue(child)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[fmt.Sprint(key)] = normalizeYAMLValue(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, normalizeYAMLValue(child))
		}
		return out
	default:
		return value
	}
}
