package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredPreviewJSONObject(t *testing.T) {
	root := t.TempDir()
	writeStructuredPreviewFile(t, root, "config.json", `{
  "name": "demo",
  "count": 2,
  "config": {
    "enabled": true,
    "threshold": 3
  },
  "items": [
    {"id": 1, "label": "a"}
  ]
}`)

	out, err := executeStructuredPreview(t, root, map[string]any{"path": "config.json"})
	if err != nil {
		t.Fatalf("structured_preview: %v", err)
	}

	want := strings.TrimSpace(`
object{keys:[config, count, items, name]}
  config: object{keys:[enabled, threshold]}
    enabled: boolean
    threshold: number
  count: number
  items: array[len=1, elem:object{keys:[id, label]}]
  name: string
`)
	if out != want {
		t.Fatalf("structured_preview output = %q, want %q", out, want)
	}
}

func TestStructuredPreviewJSONArray(t *testing.T) {
	root := t.TempDir()
	writeStructuredPreviewFile(t, root, "rows.json", `[1,2,3]`)

	out, err := executeStructuredPreview(t, root, map[string]any{"path": "rows.json"})
	if err != nil {
		t.Fatalf("structured_preview: %v", err)
	}
	if out != "array[len=3, elem:number]" {
		t.Fatalf("structured_preview output = %q, want %q", out, "array[len=3, elem:number]")
	}
}

func TestStructuredPreviewJSONLSchemaInference(t *testing.T) {
	root := t.TempDir()
	writeStructuredPreviewFile(t, root, "rows.jsonl", strings.Join([]string{
		`{"id":1,"name":"alpha"}`,
		`{"id":2}`,
		`{"id":3,"name":"gamma"}`,
	}, "\n"))

	out, err := executeStructuredPreview(t, root, map[string]any{"path": "rows.jsonl"})
	if err != nil {
		t.Fatalf("structured_preview: %v", err)
	}

	want := strings.TrimSpace(`
jsonl{rows_scanned:3, keys:[id, name]}
  id: types=[number], null_rate=0.00
  name: types=[string], null_rate=0.33
`)
	if out != want {
		t.Fatalf("structured_preview output = %q, want %q", out, want)
	}
}

func TestStructuredPreviewYAMLObject(t *testing.T) {
	root := t.TempDir()
	writeStructuredPreviewFile(t, root, "config.yaml", `
name: demo
settings:
  enabled: true
items:
  - id: 1
`)

	out, err := executeStructuredPreview(t, root, map[string]any{"path": "config.yaml"})
	if err != nil {
		t.Fatalf("structured_preview: %v", err)
	}

	want := strings.TrimSpace(`
object{keys:[items, name, settings]}
  items: array[len=1, elem:object{keys:[id]}]
  name: string
  settings: object{keys:[enabled]}
    enabled: boolean
`)
	if out != want {
		t.Fatalf("structured_preview output = %q, want %q", out, want)
	}
}

func TestStructuredPreviewRespectsMaxDepth(t *testing.T) {
	root := t.TempDir()
	writeStructuredPreviewFile(t, root, "depth.json", `{"config":{"nested":{"value":1}}}`)

	out, err := executeStructuredPreview(t, root, map[string]any{
		"path":      "depth.json",
		"max_depth": 1,
	})
	if err != nil {
		t.Fatalf("structured_preview: %v", err)
	}

	want := strings.TrimSpace(`
object{keys:[config]}
  config: object{keys:[nested]} (truncated)
`)
	if out != want {
		t.Fatalf("structured_preview output = %q, want %q", out, want)
	}
}

func TestStructuredPreviewRespectsMaxKeysPerLevel(t *testing.T) {
	root := t.TempDir()
	writeStructuredPreviewFile(t, root, "keys.json", `{"a":1,"b":2,"c":3}`)

	out, err := executeStructuredPreview(t, root, map[string]any{
		"path":               "keys.json",
		"max_keys_per_level": 2,
	})
	if err != nil {
		t.Fatalf("structured_preview: %v", err)
	}

	want := strings.TrimSpace(`
object{keys:[a, b], truncated:1} (truncated)
  a: number
  b: number
`)
	if out != want {
		t.Fatalf("structured_preview output = %q, want %q", out, want)
	}
}

func TestStructuredPreviewAutoFormatUnknownExtension(t *testing.T) {
	root := t.TempDir()
	writeStructuredPreviewFile(t, root, "data.txt", `{"a":1}`)

	_, err := executeStructuredPreview(t, root, map[string]any{"path": "data.txt"})
	if err == nil {
		t.Fatal("expected auto format error")
	}
	if !strings.Contains(err.Error(), "set format explicitly") {
		t.Fatalf("error = %v, want auto format guidance", err)
	}
}

func TestStructuredPreviewMalformedInput(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		root := t.TempDir()
		writeStructuredPreviewFile(t, root, "bad.json", `{"a":`)

		_, err := executeStructuredPreview(t, root, map[string]any{"path": "bad.json"})
		if err == nil {
			t.Fatal("expected malformed json error")
		}
		if !strings.Contains(err.Error(), "parse json") {
			t.Fatalf("error = %v, want json parse context", err)
		}
	})

	t.Run("jsonl", func(t *testing.T) {
		root := t.TempDir()
		writeStructuredPreviewFile(t, root, "bad.jsonl", "{oops}\n")

		_, err := executeStructuredPreview(t, root, map[string]any{"path": "bad.jsonl"})
		if err == nil {
			t.Fatal("expected malformed jsonl error")
		}
		if !strings.Contains(err.Error(), "parse jsonl row 1") {
			t.Fatalf("error = %v, want jsonl parse context", err)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		root := t.TempDir()
		writeStructuredPreviewFile(t, root, "bad.yaml", "a: [")

		_, err := executeStructuredPreview(t, root, map[string]any{"path": "bad.yaml"})
		if err == nil {
			t.Fatal("expected malformed yaml error")
		}
		if !strings.Contains(err.Error(), "parse yaml") {
			t.Fatalf("error = %v, want yaml parse context", err)
		}
	})
}

func TestStructuredPreviewPathEscapeRejected(t *testing.T) {
	root := t.TempDir()
	_, err := executeStructuredPreview(t, root, map[string]any{"path": "../outside.json"})
	if err == nil {
		t.Fatal("expected path escape error")
	}
	if !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("error = %v, want path escape rejection", err)
	}
}

func executeStructuredPreview(t *testing.T, root string, args map[string]any) (string, error) {
	t.Helper()
	tool := newStructuredPreview(testWorkspace{root})
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), string(encoded))
}

func writeStructuredPreviewFile(t *testing.T, root string, name string, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
