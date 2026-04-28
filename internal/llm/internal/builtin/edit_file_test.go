package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEditFileReplacesUnique(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	if err := os.WriteFile(path, []byte("hello world\nhello there\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := newEditFile(testWorkspace{root})
	args, _ := json.Marshal(map[string]string{
		"path":       "doc.md",
		"old_string": "hello world",
		"new_string": "HELLO, WORLD",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "HELLO, WORLD\nhello there\n" {
		t.Errorf("file after edit = %q", string(got))
	}
}

func TestEditFileRejectsNonUnique(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	if err := os.WriteFile(path, []byte("foo\nfoo\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := newEditFile(testWorkspace{root})
	args, _ := json.Marshal(map[string]string{
		"path":       "doc.md",
		"old_string": "foo",
		"new_string": "bar",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected error for non-unique old_string")
	}
}

func TestEditFileRejectsMissing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doc.md")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := newEditFile(testWorkspace{root})
	args, _ := json.Marshal(map[string]string{
		"path":       "doc.md",
		"old_string": "not-present",
		"new_string": "x",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected error for missing old_string")
	}
}

func TestEditFileRejectsEmptyOldString(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := newEditFile(testWorkspace{root})
	args, _ := json.Marshal(map[string]string{
		"path":       "doc.md",
		"old_string": "",
		"new_string": "y",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected error for empty old_string")
	}
}
