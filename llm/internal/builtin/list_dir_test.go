package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestListDirSortedWithSlash(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := newListDir(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{"path": "."}`)
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if out != "a.txt\nscripts/" {
		t.Errorf("list_dir = %q, want %q", out, "a.txt\nscripts/")
	}
}
