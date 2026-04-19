package builtin

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPwdReturnsAbsRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewPwd(root)
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	if !filepath.IsAbs(out) {
		t.Errorf("pwd = %q, want absolute path", out)
	}
}
