package skill

import (
	"testing"
)

func TestResolveWorkspacePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	cases := []string{"../etc/passwd", "/etc/passwd", "sub/../../outside"}
	for _, rel := range cases {
		if _, err := ResolveWorkspacePath(root, rel); err == nil {
			t.Errorf("ResolveWorkspacePath(%q) returned nil error, want escape rejection", rel)
		}
	}
}

func TestResolveWorkspacePathRejectsEmpty(t *testing.T) {
	if _, err := ResolveWorkspacePath(t.TempDir(), ""); err == nil {
		t.Error("expected error for empty path")
	}
}
