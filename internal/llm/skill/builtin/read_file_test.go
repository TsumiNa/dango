package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewReadFile(root)
	args, _ := json.Marshal(map[string]string{"path": "../" + filepath.Base(outside)})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected error when path escapes root")
	}
}

func TestReadFileLineRange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lines.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewReadFile(root)
	ctx := context.Background()

	// Explicit slice.
	args, _ := json.Marshal(map[string]any{"path": "lines.txt", "start_line": 2, "end_line": 4})
	out, err := tool.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("read_file range: %v", err)
	}
	if out != "b\nc\nd" {
		t.Errorf("range output = %q, want %q", out, "b\nc\nd")
	}

	// end_line past EOF clamps.
	args, _ = json.Marshal(map[string]any{"path": "lines.txt", "start_line": 4, "end_line": 999})
	out, err = tool.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("read_file clamp: %v", err)
	}
	if out != "d\ne" {
		t.Errorf("clamp output = %q, want %q", out, "d\ne")
	}

	// Omitting both returns full file.
	args, _ = json.Marshal(map[string]any{"path": "lines.txt"})
	out, err = tool.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("read_file full: %v", err)
	}
	if out != "a\nb\nc\nd\ne" {
		t.Errorf("full output = %q", out)
	}

	// end_line < start_line is an error.
	args, _ = json.Marshal(map[string]any{"path": "lines.txt", "start_line": 3, "end_line": 1})
	if _, err := tool.Execute(ctx, string(args)); err == nil {
		t.Fatal("expected error for end_line < start_line")
	}
}
