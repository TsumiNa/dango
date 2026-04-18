package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepInText(t *testing.T) {
	tool := NewGrep(t.TempDir())
	text := "alpha\nbeta\nalpha again\ngamma\n"
	args, _ := json.Marshal(map[string]any{
		"pattern": "alpha",
		"text":    text,
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	want := "1: alpha\n3: alpha again"
	if out != want {
		t.Errorf("grep text = %q, want %q", out, want)
	}
}

func TestGrepInFileWithContext(t *testing.T) {
	root := t.TempDir()
	body := "intro\n# Section A\ncontent a\n\n# Section B\ncontent b\n"
	if err := os.WriteFile(filepath.Join(root, "manual.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewGrep(root)
	args, _ := json.Marshal(map[string]any{
		"pattern":       "^# ",
		"path":          "manual.md",
		"regex":         true,
		"context_lines": 1,
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "2: # Section A") || !strings.Contains(out, "5: # Section B") {
		t.Errorf("grep with context missing section headers: %q", out)
	}
	if !strings.Contains(out, "--") {
		t.Errorf("grep with context expected '--' separator: %q", out)
	}
}

func TestGrepRejectsBothPathAndText(t *testing.T) {
	tool := NewGrep(t.TempDir())
	args, _ := json.Marshal(map[string]any{
		"pattern": "x",
		"path":    "a.txt",
		"text":    "x",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected error when both path and text are set")
	}
}

func TestGrepRejectsNeither(t *testing.T) {
	tool := NewGrep(t.TempDir())
	args, _ := json.Marshal(map[string]any{"pattern": "x"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected error when neither path nor text are set")
	}
}

func TestGrepMaxMatchesTruncates(t *testing.T) {
	tool := NewGrep(t.TempDir())
	text := "hit\nhit\nhit\nhit\nhit\n"
	args, _ := json.Marshal(map[string]any{
		"pattern":     "hit",
		"text":        text,
		"max_matches": 2,
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "truncated at 2 matches") {
		t.Errorf("expected truncation notice, got %q", out)
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	tool := NewGrep(t.TempDir())
	args, _ := json.Marshal(map[string]any{
		"pattern": "(",
		"text":    "x",
		"regex":   true,
	})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected invalid regex error")
	}
}
