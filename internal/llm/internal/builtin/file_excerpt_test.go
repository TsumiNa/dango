package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileExcerptLiteralWindow(t *testing.T) {
	root := t.TempDir()
	body := "intro\nalpha\nbefore\nneedle\nmiddle\nomega\noutro\n"
	if err := os.WriteFile(filepath.Join(root, "manual.txt"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := executeFileExcerpt(t, root, map[string]any{
		"path":           "manual.txt",
		"anchor_pattern": "needle",
		"before":         1,
		"after":          2,
	})
	if err != nil {
		t.Fatalf("file_excerpt: %v", err)
	}
	want := "3- before\n4: needle\n5- middle\n6- omega"
	if out != want {
		t.Fatalf("excerpt = %q, want %q", out, want)
	}
}

func TestFileExcerptRegexWithMaxMatches(t *testing.T) {
	root := t.TempDir()
	body := "intro\n# One\na\n# Two\nb\n# Three\nc\n"
	if err := os.WriteFile(filepath.Join(root, "manual.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := executeFileExcerpt(t, root, map[string]any{
		"path":           "manual.md",
		"anchor_pattern": `^# `,
		"regex":          true,
		"after":          1,
		"max_matches":    2,
	})
	if err != nil {
		t.Fatalf("file_excerpt: %v", err)
	}
	want := "2: # One\n3- a\n4: # Two\n5- b\n... (truncated at 2 matches, 1 more)"
	if out != want {
		t.Fatalf("excerpt = %q, want %q", out, want)
	}
}

func TestFileExcerptRejectsPathEscapes(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"../outside.txt", filepath.Join(t.TempDir(), "outside.txt")} {
		_, err := executeFileExcerpt(t, root, map[string]any{
			"path":           path,
			"anchor_pattern": "x",
		})
		if err == nil {
			t.Fatalf("expected escape error for %q", path)
		}
		if !strings.Contains(err.Error(), "escapes workspace root") {
			t.Fatalf("error for %q = %v, want escape message", path, err)
		}
	}
}

func TestFileExcerptReportsFileNotFound(t *testing.T) {
	_, err := executeFileExcerpt(t, t.TempDir(), map[string]any{
		"path":           "missing.txt",
		"anchor_pattern": "x",
	})
	if err == nil {
		t.Fatal("expected missing file error")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("error = %v, want missing file", err)
	}
}

func TestFileExcerptZeroMatchesIsNoOp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manual.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := executeFileExcerpt(t, root, map[string]any{
		"path":           "manual.txt",
		"anchor_pattern": "gamma",
	})
	if err != nil {
		t.Fatalf("file_excerpt: %v", err)
	}
	if out != "" {
		t.Fatalf("excerpt = %q, want empty no-op", out)
	}
}

func TestFileExcerptRejectsInvalidRegex(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manual.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := executeFileExcerpt(t, root, map[string]any{
		"path":           "manual.txt",
		"anchor_pattern": "(",
		"regex":          true,
	})
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %v, want invalid regex", err)
	}
}

func executeFileExcerpt(t *testing.T, root string, args map[string]any) (string, error) {
	t.Helper()
	tool := newFileExcerpt(testWorkspace{root})
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), string(encoded))
}
