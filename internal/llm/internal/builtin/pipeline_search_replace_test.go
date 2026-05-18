package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPipelineSearchReplaceLiteral(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("alpha beta alpha\n"), 0o640); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := executePipelineSearchReplace(t, root, map[string]any{
		"path":        "notes.txt",
		"pattern":     "alpha",
		"replacement": "omega",
	})
	if err != nil {
		t.Fatalf("pipeline_search_replace: %v", err)
	}
	if out != "replaced 2 occurrence(s) in notes.txt" {
		t.Fatalf("summary = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data) != "omega beta omega\n" {
		t.Fatalf("content = %q", string(data))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat result: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %v, want 0640", got)
	}
}

func TestPipelineSearchReplaceRegexWithMaxReplacements(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "scores.txt")
	if err := os.WriteFile(path, []byte("a=1 b=22 c=333\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := executePipelineSearchReplace(t, root, map[string]any{
		"path":             "scores.txt",
		"pattern":          `\d+`,
		"replacement":      "N",
		"regex":            true,
		"max_replacements": 1,
	})
	if err != nil {
		t.Fatalf("pipeline_search_replace: %v", err)
	}
	if out != "replaced 1 occurrence(s) in scores.txt" {
		t.Fatalf("summary = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data) != "a=N b=22 c=333\n" {
		t.Fatalf("content = %q", string(data))
	}
}

func TestPipelineSearchReplaceRegexReplacesAll(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "scores.txt")
	if err := os.WriteFile(path, []byte("a=1 b=22 c=333\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := executePipelineSearchReplace(t, root, map[string]any{
		"path":        "scores.txt",
		"pattern":     `\d+`,
		"replacement": "N",
		"regex":       true,
	})
	if err != nil {
		t.Fatalf("pipeline_search_replace: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data) != "a=N b=N c=N\n" {
		t.Fatalf("content = %q", string(data))
	}
}

func TestPipelineSearchReplaceRejectsPathEscapes(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"../outside.txt", filepath.Join(t.TempDir(), "outside.txt")} {
		_, err := executePipelineSearchReplace(t, root, map[string]any{
			"path":        path,
			"pattern":     "x",
			"replacement": "y",
		})
		if err == nil {
			t.Fatalf("expected escape error for %q", path)
		}
		if !strings.Contains(err.Error(), "escapes workspace root") {
			t.Fatalf("error for %q = %v, want escape message", path, err)
		}
	}
}

func TestPipelineSearchReplaceReportsFileNotFound(t *testing.T) {
	_, err := executePipelineSearchReplace(t, t.TempDir(), map[string]any{
		"path":        "missing.txt",
		"pattern":     "x",
		"replacement": "y",
	})
	if err == nil {
		t.Fatal("expected missing file error")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("error = %v, want missing file", err)
	}
}

func TestPipelineSearchReplaceZeroMatchesIsNoOp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("alpha beta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := executePipelineSearchReplace(t, root, map[string]any{
		"path":        "notes.txt",
		"pattern":     "gamma",
		"replacement": "delta",
	})
	if err != nil {
		t.Fatalf("pipeline_search_replace: %v", err)
	}
	if out != "replaced 0 occurrence(s) in notes.txt" {
		t.Fatalf("summary = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data) != "alpha beta\n" {
		t.Fatalf("content = %q", string(data))
	}
}

func executePipelineSearchReplace(t *testing.T, root string, args map[string]any) (string, error) {
	t.Helper()
	tool := newPipelineSearchReplace(testWorkspace{root})
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), string(encoded))
}
