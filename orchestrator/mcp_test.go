package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tsumina/dango/internal/mcpclient"
	"github.com/tsumina/dango/llm"
)

const orchestratorMCPTimeout = 5 * time.Second

func TestOrchestratorAddMCPServersRejectsNil(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.AddMCPServers(nil); err == nil {
		t.Fatal("expected nil-entry error")
	}
}

func TestOrchestratorAddMCPServersRejectsDuplicates(t *testing.T) {
	o := newOrchestrator(testLogger)
	srv, cleanup := newOrchestratorTestMCPServer(t, "demo")
	defer cleanup()
	srv2, cleanup2 := newOrchestratorTestMCPServer(t, "demo")
	defer cleanup2()

	if err := o.AddMCPServers(srv); err != nil {
		t.Fatalf("first AddMCPServers: %v", err)
	}
	if err := o.AddMCPServers(srv2); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestOrchestratorAddMCPServersRejectsDuplicatesInSingleCall(t *testing.T) {
	o := newOrchestrator(testLogger)
	srv1, cleanup1 := newOrchestratorTestMCPServer(t, "demo")
	defer cleanup1()
	srv2, cleanup2 := newOrchestratorTestMCPServer(t, "demo")
	defer cleanup2()

	if err := o.AddMCPServers(srv1, srv2); err == nil {
		t.Fatal("expected duplicate-name error in single call")
	}
}

func TestOrchestratorAttachesGlobalMCPToolsToEverySkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	srv, cleanup := newOrchestratorTestMCPServer(t, "global")
	defer cleanup()

	if err := o.AddMCPServers(srv); err != nil {
		t.Fatalf("AddMCPServers: %v", err)
	}

	mustAddSkills(t,
		o,
		newTestSkillRegistration(t, "writer", "writes things", nil),
		newTestSkillRegistration(t, "reader", "reads things", nil),
	)

	for _, name := range []string{"writer", "reader"} {
		sk := o.Skills()[name]
		if sk == nil {
			t.Fatalf("skill %q not registered", name)
		}
		if !skillExposesTool(sk, "global__echo") {
			t.Fatalf("skill %q is missing global MCP tool global__echo", name)
		}
	}
}

func TestOrchestratorPerSkillMCPIsolation(t *testing.T) {
	o := newOrchestrator(testLogger)
	perSkill, cleanup := newOrchestratorTestMCPServer(t, "private")
	defer cleanup()

	regWithMCP := newTestSkillRegistration(t, "writer", "writes things", nil)
	regWithMCP.MCPServers = []*mcpclient.Server{perSkill}
	regBare := newTestSkillRegistration(t, "reader", "reads things", nil)
	mustAddSkills(t, o, regWithMCP, regBare)

	if !skillExposesTool(o.Skills()["writer"], "private__echo") {
		t.Fatal("expected writer to expose private__echo")
	}
	if skillExposesTool(o.Skills()["reader"], "private__echo") {
		t.Fatal("reader must not see another skill's private MCP server")
	}
}

func TestOrchestratorAddMCPServersEmitsLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o := newOrchestrator(logger)
	srv, cleanup := newOrchestratorTestMCPServer(t, "logged")
	defer cleanup()

	if err := o.AddMCPServers(srv); err != nil {
		t.Fatalf("AddMCPServers: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "registered MCP server") || !strings.Contains(out, `server=logged`) {
		t.Fatalf("missing INFO line in logs: %q", out)
	}
	if !strings.Contains(out, "user-supplied MCP servers run as external processes") {
		t.Fatalf("missing risk-notice WARN in logs: %q", out)
	}
}

func TestOrchestratorMCPServersSnapshot(t *testing.T) {
	o := newOrchestrator(testLogger)
	srv, cleanup := newOrchestratorTestMCPServer(t, "demo")
	defer cleanup()

	if err := o.AddMCPServers(srv); err != nil {
		t.Fatalf("AddMCPServers: %v", err)
	}
	servers := o.MCPServers()
	if len(servers) != 1 || servers[0] != srv {
		t.Fatalf("snapshot mismatch: %+v", servers)
	}
	// Mutating the snapshot must not affect orchestrator state.
	servers[0] = nil
	if got := o.MCPServers(); len(got) != 1 || got[0] != srv {
		t.Fatal("MCPServers snapshot is not a copy")
	}
}

func TestOrchestratorCloseShutsDownMCPServers(t *testing.T) {
	o := newOrchestrator(testLogger)
	srv, cleanup := newOrchestratorTestMCPServer(t, "demo")
	defer cleanup()

	if err := o.AddMCPServers(srv); err != nil {
		t.Fatalf("AddMCPServers: %v", err)
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := o.MCPServers(); len(got) != 0 {
		t.Fatalf("MCPServers should be cleared after Close: %+v", got)
	}
	// Calling once more must remain a no-op.
	if err := o.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestOrchestratorAddSkillsEmitsPerSkillMCPWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o := newOrchestrator(logger)
	srv, cleanup := newOrchestratorTestMCPServer(t, "perskill")
	defer cleanup()

	reg := newTestSkillRegistration(t, "writer", "writes", nil)
	reg.MCPServers = []*mcpclient.Server{srv}
	mustAddSkills(t, o, reg)

	out := buf.String()
	if !strings.Contains(out, "per-skill MCP servers run as external processes") {
		t.Fatalf("missing per-skill WARN risk notice: %q", out)
	}
	if !strings.Contains(out, `skill=writer`) {
		t.Fatalf("WARN should name the skill: %q", out)
	}
}

func TestOrchestratorAddSkillsAcceptsMCPServers(t *testing.T) {
	// Sanity check: a registration carrying per-skill MCP servers is accepted
	// and the skill ends up with the namespaced tool. Bound-skill rejection
	// has its own coverage in the base orchestrator tests.
	o := newOrchestrator(testLogger)
	srv, cleanup := newOrchestratorTestMCPServer(t, "demo")
	defer cleanup()

	reg := newTestSkillRegistration(t, "writer", "writes", nil)
	reg.MCPServers = []*mcpclient.Server{srv}
	mustAddSkills(t, o, reg)
	if !skillExposesTool(o.Skills()["writer"], "demo__echo") {
		t.Fatal("expected writer to expose demo__echo after accepting per-skill MCP server")
	}
}

// skillExposesTool reports whether sk lists a tool with the given name in
// its registered tool set.
func skillExposesTool(sk *llm.Skill, name string) bool {
	if sk == nil {
		return false
	}
	for _, tool := range sk.Tools() {
		if tool.Name() == name {
			return true
		}
	}
	return false
}

// newOrchestratorTestMCPServer wires an in-memory MCP server with a single
// "echo" tool and returns a connected *mcpclient.Server plus a cleanup func.
func newOrchestratorTestMCPServer(t *testing.T, name string) (*mcpclient.Server, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), orchestratorMCPTimeout)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echoes back"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
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
	cleanup := func() {
		_ = srv.Close()
		cancel()
	}
	return srv, cleanup
}
