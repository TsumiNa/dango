package llm

import (
	"context"
	"errors"
	"testing"
)

func TestFuncTool_NilHandlerReturnsError(t *testing.T) {
	tool := NewFuncTool("x", "", nil, nil)
	if _, err := tool.Execute(context.Background(), "{}"); err == nil {
		t.Fatal("expected error when handler is nil")
	}
}

func TestFuncTool_ForwardsArgumentsAndResult(t *testing.T) {
	want := errors.New("boom")
	tool := NewFuncTool("echo", "desc", map[string]any{"type": "object"},
		func(_ context.Context, args string) (string, error) {
			if args != `{"msg":"hi"}` {
				t.Errorf("handler got args=%q", args)
			}
			return "ok", want
		})
	if got := tool.Name(); got != "echo" {
		t.Errorf("Name=%q", got)
	}
	if got := tool.Description(); got != "desc" {
		t.Errorf("Description=%q", got)
	}
	if got := tool.Parameters(); got["type"] != "object" {
		t.Errorf("Parameters=%v", got)
	}
	out, err := tool.Execute(context.Background(), `{"msg":"hi"}`)
	if out != "ok" || !errors.Is(err, want) {
		t.Errorf("Execute=%q,%v", out, err)
	}
}
