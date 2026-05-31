package builtin

import (
	"context"
	"strings"
	"testing"
)

func TestPwdReturnsWorkspaceRoots(t *testing.T) {
	root := t.TempDir()
	extraDir := t.TempDir()
	tool := newPwd(pwdTestWorkspace{testWorkspace{root}, []string{extraDir}})
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	for _, want := range []string{"skill_root: " + root, "temp_root: " + root, "workdir: " + root, "accessible_dir: " + extraDir} {
		if !strings.Contains(out, want) {
			t.Errorf("pwd = %q, want to contain %q", out, want)
		}
	}
}

type pwdTestWorkspace struct {
	testWorkspace
	accessibleDirs []string
}

func (w pwdTestWorkspace) AccessibleDirs() []string {
	return append([]string(nil), w.accessibleDirs...)
}
