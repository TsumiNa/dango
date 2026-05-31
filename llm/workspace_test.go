package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspacePathRejectsEscape(t *testing.T) {
	root, err := canonicalDir(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalDir: %v", err)
	}
	cases := []string{"../etc/passwd", "/etc/passwd", "sub/../../outside"}
	for _, rel := range cases {
		if _, err := resolveWorkspacePath(root, rel); err == nil {
			t.Errorf("resolveWorkspacePath(%q) returned nil error, want escape rejection", rel)
		}
	}
}

func TestResolveWorkspacePathRejectsEmpty(t *testing.T) {
	root, err := canonicalDir(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalDir: %v", err)
	}
	if _, err := resolveWorkspacePath(root, ""); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestWorkspaceRootResolvePathAllowsTempAndSkillRoots(t *testing.T) {
	skillRoot := t.TempDir()
	workspace, err := newWorkspaceRoot(skillRoot)
	if err != nil {
		t.Fatalf("newWorkspaceRoot: %v", err)
	}
	t.Cleanup(func() { _ = workspace.cleanup() })

	got, err := workspace.ResolvePath("logs/run.log")
	if err != nil {
		t.Fatalf("ResolvePath(relative): %v", err)
	}
	want := filepath.Join(workspace.TempRoot(), "logs/run.log")
	if got != want {
		t.Fatalf("relative ResolvePath = %q, want temp path %q", got, want)
	}

	sourceFile := filepath.Join(workspace.SkillRoot(), "reference.txt")
	if err := os.WriteFile(sourceFile, []byte("reference"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	got, err = workspace.ResolvePath(sourceFile)
	if err != nil {
		t.Fatalf("ResolvePath(source absolute): %v", err)
	}
	if got != sourceFile {
		t.Fatalf("source absolute ResolvePath = %q, want %q", got, sourceFile)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if _, err := workspace.ResolvePath(outside); err == nil {
		t.Fatal("expected absolute path outside temp/source roots to be rejected")
	}
}

func TestWorkspaceRootResolvePathAllowsAccessibleDirs(t *testing.T) {
	workspace, err := newWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("newWorkspaceRoot: %v", err)
	}
	t.Cleanup(func() { _ = workspace.cleanup() })
	extraRoot := t.TempDir()
	nestedFile := filepath.Join(extraRoot, "nested", "note.txt")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o755); err != nil {
		t.Fatalf("mkdir nested extra dir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("note"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	if err := workspace.setAccessibleDirs(extraRoot); err != nil {
		t.Fatalf("setAccessibleDirs: %v", err)
	}

	got, err := workspace.ResolvePath(nestedFile)
	if err != nil {
		t.Fatalf("ResolvePath(extra absolute): %v", err)
	}
	want, err := filepath.EvalSymlinks(nestedFile)
	if err != nil {
		t.Fatalf("EvalSymlinks(nestedFile): %v", err)
	}
	if got != want {
		t.Fatalf("extra absolute ResolvePath = %q, want %q", got, want)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if _, err := workspace.ResolvePath(outside); err == nil {
		t.Fatal("expected absolute path outside temp/source/extra roots to be rejected")
	}
	if err := workspace.setAccessibleDirs(); err != nil {
		t.Fatalf("clear accessible dirs: %v", err)
	}
	if _, err := workspace.ResolvePath(nestedFile); err == nil {
		t.Fatal("expected cleared accessible dir to be rejected")
	}
}

func TestWorkspaceRootResolvePathRejectsTempSymlinkEscape(t *testing.T) {
	workspace, err := newWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatalf("newWorkspaceRoot: %v", err)
	}
	t.Cleanup(func() { _ = workspace.cleanup() })

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace.TempRoot(), "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := workspace.ResolvePath("link/secret.txt"); err == nil {
		t.Fatal("expected symlink escape through temp root to be rejected")
	}
}
