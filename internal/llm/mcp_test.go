package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/mcpclient"
)

const mcpTestTimeout = 5 * time.Second

func TestMCPToolsExposesNamespacedTools(t *testing.T) {
	t.Parallel()
	srv, _, cancel := newTestMCPServer(t, "demo")
	defer cancel()

	tools := MCPTools(srv)
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	if !names["demo__echo"] {
		t.Fatalf("expected demo__echo in %v", names)
	}
}

func TestMCPToolsNilServer(t *testing.T) {
	t.Parallel()
	if got := MCPTools(nil); got != nil {
		t.Fatalf("expected nil tools, got %v", got)
	}
}

func TestMCPToolExecuteCallsServer(t *testing.T) {
	t.Parallel()
	srv, _, cancel := newTestMCPServer(t, "demo")
	defer cancel()

	tool := findMCPTool(t, srv, "demo__echo")
	out, err := tool.Execute(context.Background(), `{"text":"ping"}`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "ping" {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestMCPToolErrorSurfaced(t *testing.T) {
	t.Parallel()
	srv, _, cancel := newTestMCPServer(t, "demo")
	defer cancel()

	tool := findMCPTool(t, srv, "demo__fail")
	out, err := tool.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("expected error; got %q", out)
	}
}

func TestMCPToolParametersForwardsSchema(t *testing.T) {
	t.Parallel()
	srv, _, cancel := newTestMCPServer(t, "demo")
	defer cancel()

	tool := findMCPTool(t, srv, "demo__echo")
	params := tool.Parameters()
	if params["type"] != "object" {
		t.Fatalf("schema lost type: %+v", params)
	}
}

func TestMCPCapabilityRefKind(t *testing.T) {
	t.Parallel()
	ref := MCPCapability("demo__echo")
	if ref.Kind != CapabilityMCPTool {
		t.Fatalf("unexpected kind %q", ref.Kind)
	}
	if ref.Name != "demo__echo" {
		t.Fatalf("unexpected name %q", ref.Name)
	}
}

func TestCapabilityRefForUnwrappedMCPTool(t *testing.T) {
	t.Parallel()
	srv, _, cancel := newTestMCPServer(t, "demo")
	defer cancel()

	tool := findMCPTool(t, srv, "demo__echo")
	wrapped := wrapToolsWithPolicySet([]Tool{tool}, ToolSetConfig{})
	if len(wrapped) != 1 {
		t.Fatalf("expected one wrapped tool, got %d", len(wrapped))
	}
	policyT, ok := wrapped[0].(*policyTool)
	if !ok {
		t.Fatalf("wrapped tool not *policyTool: %T", wrapped[0])
	}
	if policyT.ref.Kind != CapabilityMCPTool {
		t.Fatalf("policy ref kind not MCP: %q", policyT.ref.Kind)
	}
	if policyT.ref.Name != "demo__echo" {
		t.Fatalf("policy ref name unexpected: %q", policyT.ref.Name)
	}
}

func TestPolicyToolForwardsMCPMarkers(t *testing.T) {
	t.Parallel()
	srv, _, cancel := newTestMCPServer(t, "demo")
	defer cancel()

	tool := findMCPTool(t, srv, "demo__echo")
	wrapped := wrapToolsWithPolicySet([]Tool{tool}, ToolSetConfig{})
	m, ok := wrapped[0].(mcpToolMarker)
	if !ok {
		t.Fatalf("wrapped tool does not implement mcpToolMarker")
	}
	if m.mcpServerName() != "demo" || m.mcpToolName() != "echo" {
		t.Fatalf("forwarding broken: server=%q tool=%q", m.mcpServerName(), m.mcpToolName())
	}
}

func TestNamespacedMCPName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		server, tool, want string
	}{
		{"a", "b", "a__b"},
		{"", "b", "b"},
		{"a", "", "a"},
	}
	for _, c := range cases {
		if got := namespacedMCPName(c.server, c.tool); got != c.want {
			t.Fatalf("server=%q tool=%q got %q want %q", c.server, c.tool, got, c.want)
		}
	}
}

// findMCPTool returns the [Tool] with the given namespaced name from srv's
// adapter set, failing the test if it is not present.
func findMCPTool(t *testing.T, srv *mcpclient.Server, namespaced string) Tool {
	t.Helper()
	for _, tool := range MCPTools(srv) {
		if tool.Name() == namespaced {
			return tool
		}
	}
	t.Fatalf("tool %q not found in server", namespaced)
	return nil
}

// newTestMCPServer wires an in-memory MCP server with deterministic tools
// and returns a connected mcpclient.Server.
func newTestMCPServer(t *testing.T, name string) (*mcpclient.Server, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), mcpTestTimeout)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "echoes back text",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fail",
		Description: "always errors",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		res := &mcp.CallToolResult{}
		res.SetError(errors.New("boom"))
		return res, nil, nil
	})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		cancel()
		t.Fatalf("mcp server connect: %v", err)
	}

	srv, err := mcpclient.Start(ctx, mcpclient.ServerSpec{Name: name}, clientTransport)
	if err != nil {
		cancel()
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ctx, cancel
}

// TestConversationRun_MCPSuppressesResultDeltaAndEmitsCallEvent drives a
// conversation through one MCP tool call and verifies that the runtime stream
// carries the compact mcp.tool.call.completed event instead of the
// llm.tool_result.delta event that non-MCP tools produce.
func TestConversationRun_MCPSuppressesResultDeltaAndEmitsCallEvent(t *testing.T) {
	srv, _, cancel := newTestMCPServer(t, "demo")
	defer cancel()

	var responded int
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if responded == 0 {
			responded++
			sseResponse(w, completedEvent("", "demo__echo", `{"text":"ping"}`))
			return
		}
		sseResponse(w, textDeltaEvent("done"), completedEvent("done", "", ""))
	}))
	t.Cleanup(httpSrv.Close)

	tool := findMCPTool(t, srv, "demo__echo")
	conv := mustNewConversation(t, testClient(httpSrv.URL), "sys", []Tool{tool}, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "demo_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_mcp", NodeID: "node_mcp"},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if _, err := conv.Run(t.Context(), "go", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	eventStream.Close()
	events := collectStreamEvents(t, sub)

	if !hasStreamEvent(events, streampkg.EventMCPToolCallCompleted, "skill", "node_mcp", func(delta map[string]any) bool {
		if delta["server"] != "demo" || delta["tool"] != "echo" {
			return false
		}
		if delta["namespaced_name"] != "demo__echo" {
			return false
		}
		if delta["outcome"] != "ok" {
			return false
		}
		args, _ := delta["arguments_summary"].(string)
		return args != "" && delta["call_id"] == "call_1"
	}) {
		t.Fatalf("missing or malformed mcp.tool.call.completed event: %+v", events)
	}

	for _, ev := range events {
		if ev.EventType != streampkg.EventLLMToolResultDelta {
			continue
		}
		var payload map[string]any
		_ = json.Unmarshal(ev.Delta, &payload)
		if payload["name"] == "demo__echo" {
			t.Fatalf("MCP tool produced llm.tool_result.delta; result body must stay off the stream: %+v", payload)
		}
	}
}
