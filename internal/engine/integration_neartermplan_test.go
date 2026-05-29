package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/mcpclient"
)

// TestNearTermPlanIntegrationCheck is the cross-subtask engineering gate
// required by docs/near_term_plan/90-closeout.md. It composes the
// delivered subtasks (built-in tools, MCP, URL allowlist, audit-tagged
// stream events) and asserts they coexist without conflict.
//
// Individual axes already have focused tests; this test only asserts
// composition. Specifically:
//   - core built-in ordering: covered here.
//   - MCP-vs-builtin name collisions: covered here.
//   - URL allowlist enforcement on bash: covered here.
//   - audit category on tool-call events: covered in
//     internal/llm/audit_test.go, which runs the full SSE conversation
//     cycle the engine package would otherwise have to duplicate. The
//     engine package never re-tags or strips the metadata, so the
//     llm-package coverage is the binding gate.
func TestNearTermPlanIntegrationCheck(t *testing.T) {
	t.Run("CoreBuiltinOrdering", checkCoreBuiltinOrdering)
	t.Run("MCPDoesNotCollideWithBuiltins", checkMCPDoesNotCollideWithBuiltins)
	t.Run("URLAllowlistEnforced", checkURLAllowlistEnforced)
}

func checkCoreBuiltinOrdering(t *testing.T) {
	sk := loadTestSkillFromDir(t, writeTestSkill(t, "core-skill", "core ordering"))
	sk, err := sk.SetAccessibleDirsAndBuiltinTools()
	if err != nil {
		t.Fatalf("rebuild builtins: %v", err)
	}
	wantPrefix := []string{
		"bash",
		"read_file",
		"write_file",
		"edit_file",
		"delete_file",
		"move_file",
		"grep",
		"pipeline_search_replace",
		"file_excerpt",
		"artifact_catalog",
		"structured_preview",
	}
	tools := sk.Tools()
	if len(tools) < len(wantPrefix) {
		t.Fatalf("expected at least %d tools, got %d", len(wantPrefix), len(tools))
	}
	for i, name := range wantPrefix {
		if got := tools[i].Name(); got != name {
			t.Fatalf("tools[%d] = %q, want %q (the closeout integration relies on this order)", i, got, name)
		}
	}
}

func checkMCPDoesNotCollideWithBuiltins(t *testing.T) {
	o := newOrchestrator(testLogger)
	srv, cleanup := startCollidingMCPServer(t, "demo")
	defer cleanup()

	if err := o.AddMCPServers(srv); err != nil {
		t.Fatalf("AddMCPServers: %v", err)
	}
	mustAddSkills(t, o, newTestSkillRegistration(t, "writer", "writes", nil))

	sk := o.Skills()["writer"]
	if sk == nil {
		t.Fatal("writer not registered")
	}
	names := make(map[string]int, len(sk.Tools()))
	for _, tool := range sk.Tools() {
		names[tool.Name()]++
	}
	if names["bash"] != 1 {
		t.Fatalf("built-in bash count = %d, want 1; names=%v", names["bash"], names)
	}
	if names["demo__bash"] != 1 {
		t.Fatalf("MCP-namespaced bash count = %d, want 1; names=%v", names["demo__bash"], names)
	}
	for name, n := range names {
		if n > 1 {
			t.Fatalf("tool name collision: %q appears %d times", name, n)
		}
	}
}

func checkURLAllowlistEnforced(t *testing.T) {
	sk := loadTestSkillFromDir(t, writeTestSkill(t, "url-skill", "url allowlist"))
	cfg := llm.DefaultToolSetConfig()
	cfg.BashURLAllowlist = []string{"https://api.example.com"}
	sk, err := sk.WithToolSetConfig(cfg)
	if err != nil {
		t.Fatalf("WithToolSetConfig: %v", err)
	}
	sk, err = sk.SetAccessibleDirsAndBuiltinTools()
	if err != nil {
		t.Fatalf("rebuild builtins: %v", err)
	}
	bash := findToolByName(t, sk, "bash")
	if _, err := bash.Execute(context.Background(), `{"command":"curl https://attacker.example/"}`); err == nil {
		t.Fatal("URL allowlist should reject unlisted URL but call succeeded")
	}
}

// startCollidingMCPServer spawns an in-memory MCP server whose tool name
// is "bash" — chosen specifically to exercise the namespacing rule, since
// "bash" is the most likely collision target with a Go built-in.
func startCollidingMCPServer(t *testing.T, name string) (*mcpclient.Server, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	server := mcp.NewServer(&mcp.Implementation{Name: "integration", Version: "v0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "bash", Description: "collision probe"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
		},
	)
	mcp.AddTool(server, &mcp.Tool{Name: "fail", Description: "errors"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			res := &mcp.CallToolResult{}
			res.SetError(errors.New("boom"))
			return res, nil, nil
		},
	)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		cancel()
		t.Fatalf("server connect: %v", err)
	}
	srv, err := mcpclient.Start(ctx, mcpclient.ServerSpec{Name: name}, clientTransport)
	if err != nil {
		cancel()
		t.Fatalf("client connect: %v", err)
	}
	return srv, func() {
		_ = srv.Close()
		cancel()
	}
}

func findToolByName(t *testing.T, sk *llm.Skill, name string) llm.Tool {
	t.Helper()
	for _, tool := range sk.Tools() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found on skill %q", name, sk.Name)
	return nil
}
