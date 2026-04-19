package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteFileRemovesFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewDeleteFile(root)
	args, _ := json.Marshal(map[string]string{"path": "a.txt"})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("delete_file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed, stat err = %v", err)
	}
}

func TestDeleteFileRecursive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "nested", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewDeleteFile(root)

	// Non-recursive should fail because directory is non-empty.
	args, _ := json.Marshal(map[string]any{"path": "sub"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected non-recursive delete of non-empty dir to fail")
	}

	// Recursive should succeed.
	args, _ = json.Marshal(map[string]any{"path": "sub", "recursive": true})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("recursive delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub")); !os.IsNotExist(err) {
		t.Errorf("expected directory removed, stat err = %v", err)
	}
}

func TestDeleteFileRejectsEscape(t *testing.T) {
	root := t.TempDir()
	tool := NewDeleteFile(root)
	args, _ := json.Marshal(map[string]string{"path": "../escape.txt"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected escape rejection")
	}
}
