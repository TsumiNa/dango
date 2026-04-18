package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveFileRenames(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewMoveFile(root)
	args, _ := json.Marshal(map[string]string{"src": "old.txt", "dst": "renamed/new.txt"})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("move_file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Errorf("expected src removed, stat err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "renamed", "new.txt"))
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != "hi" {
		t.Errorf("dst contents = %q", string(data))
	}
}

func TestMoveFileRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewMoveFile(root)

	argsSrc, _ := json.Marshal(map[string]string{"src": "../a.txt", "dst": "b.txt"})
	if _, err := tool.Execute(context.Background(), string(argsSrc)); err == nil {
		t.Fatal("expected src escape rejection")
	}
	argsDst, _ := json.Marshal(map[string]string{"src": "a.txt", "dst": "../b.txt"})
	if _, err := tool.Execute(context.Background(), string(argsDst)); err == nil {
		t.Fatal("expected dst escape rejection")
	}
}
