package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFuncToolMissingHandler(t *testing.T) {
	tool := &FuncTool{NameV: "x"}
	if _, err := tool.Execute(context.Background(), "{}"); err == nil {
		t.Fatal("expected error when handler is nil")
	}
}

func TestBuiltinToolsNames(t *testing.T) {
	root := t.TempDir()
	got := map[string]bool{}
	for _, tool := range BuiltinTools(root) {
		got[tool.Name()] = true
	}
	for _, want := range []string{"bash", "read_file", "write_file", "list_dir", "pwd"} {
		if !got[want] {
			t.Errorf("BuiltinTools missing %q", want)
		}
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	write := NewWriteFileTool(root)
	args, _ := json.Marshal(map[string]string{"path": "sub/hello.txt", "content": "hi"})
	if _, err := write.Execute(ctx, string(args)); err != nil {
		t.Fatalf("write_file: %v", err)
	}

	read := NewReadFileTool(root)
	args, _ = json.Marshal(map[string]string{"path": "sub/hello.txt"})
	out, err := read.Execute(ctx, string(args))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if out != "hi" {
		t.Errorf("read_file output = %q, want %q", out, "hi")
	}
}

func TestListDirSortedWithSlash(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewListDirTool(root)
	out, err := tool.Execute(context.Background(), `{"path": "."}`)
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if out != "a.txt\nscripts/" {
		t.Errorf("list_dir = %q, want %q", out, "a.txt\nscripts/")
	}
}

func TestResolveWorkspacePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	cases := []string{"../etc/passwd", "/etc/passwd", "sub/../../outside"}
	for _, rel := range cases {
		if _, err := resolveWorkspacePath(root, rel); err == nil {
			t.Errorf("resolveWorkspacePath(%q) = nil, want error", rel)
		}
	}
}

func TestResolveWorkspacePathRejectsEmpty(t *testing.T) {
	if _, err := resolveWorkspacePath(t.TempDir(), ""); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestReadFileRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := NewReadFileTool(root)
	args, _ := json.Marshal(map[string]string{"path": "../" + filepath.Base(outside)})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected error when path escapes root")
	}
}

func TestBashRunsInRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewBashTool(root)
	out, err := tool.Execute(context.Background(), `{"command": "pwd"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	// macOS /var vs /private/var: compare canonicalized paths.
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotRoot, _ := filepath.EvalSymlinks(strings.TrimSpace(out))
	if gotRoot != wantRoot {
		t.Errorf("bash pwd = %q, want %q", gotRoot, wantRoot)
	}
}

func TestBashEmptyCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if _, err := tool.Execute(context.Background(), `{"command": "  "}`); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBashInvalidJSON(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Fatal("expected error for invalid JSON arguments")
	}
}

func TestPwdReturnsAbsRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewPwdTool(root)
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	if !filepath.IsAbs(out) {
		t.Errorf("pwd = %q, want absolute path", out)
	}
}
