package llm

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tsumina/dango/internal/llm/internal/mcpclient"
)

// MCPTransport is the MCP-SDK transport interface, re-exported so callers can
// inject a custom transport (e.g. HTTP, in-memory) via
// [StartMCPServerWithTransport] without taking a direct dependency on the SDK
// import path.
type MCPTransport = mcp.Transport

// MCPServerSpec describes one MCP server stdio process the orchestrator or a
// skill wants to mount.
//
// Name is the user-chosen identifier used both for log/risk-notice output and
// as the namespace prefix for every tool the server exposes (each tool is
// advertised to the LLM as "<Name>__<tool>"). Command, Args, and Env describe
// the subprocess that runs the MCP server; when Env is nil the subprocess
// inherits the parent process environment.
type MCPServerSpec = mcpclient.ServerSpec

// MCPServer is a live MCP client session plus the captured tool catalogue.
//
// MCPServer is safe for concurrent use across [MCPServer.Call] invocations
// because the underlying SDK session serializes write traffic. [MCPServer.Close]
// is idempotent. The orchestrator owns shutdown for servers started at app/cmd
// startup; skill-mount callers own shutdown for per-skill servers they start.
type MCPServer struct {
	inner *mcpclient.Server
}

// StartMCPServer launches spec via stdio and connects an MCP client to it.
//
// The returned MCPServer keeps the live session; the caller must call
// [MCPServer.Close] when done. The MCP initialize handshake and the
// tools/list call complete before this function returns.
func StartMCPServer(ctx context.Context, spec MCPServerSpec) (*MCPServer, error) {
	inner, err := mcpclient.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &MCPServer{inner: inner}, nil
}

// startMCPServerWithInner is a test seam for wrapping a pre-built
// mcpclient.Server (created via mcpclient.StartWithTransport) without going
// through subprocess spawning.
func startMCPServerWithInner(inner *mcpclient.Server) *MCPServer {
	return &MCPServer{inner: inner}
}

// StartMCPServerWithTransport connects an MCP client to t instead of
// spawning the spec's command. It exists for tests and for future
// non-stdio transports (HTTP, in-memory); production code should normally
// call [StartMCPServer] with a stdio command spec. The returned MCPServer
// owns t through the underlying SDK session, and the caller must call
// [MCPServer.Close] when done.
func StartMCPServerWithTransport(ctx context.Context, spec MCPServerSpec, t MCPTransport) (*MCPServer, error) {
	inner, err := mcpclient.StartWithTransport(ctx, spec, t)
	if err != nil {
		return nil, err
	}
	return startMCPServerWithInner(inner), nil
}

// Name returns the configured server name. The same name is used as the
// namespace prefix for every tool produced by [MCPServer.Tools].
func (s *MCPServer) Name() string {
	if s == nil || s.inner == nil {
		return ""
	}
	return s.inner.Name()
}

// Tools returns one [Tool] adapter per tool advertised by the server.
//
// Each adapter's Name() returns the namespaced form "<server>__<tool>".
// Parameters() forwards the server's JSON input schema verbatim. Execute()
// dispatches one tool call through the live session; results are
// concatenated and truncated by the mcpclient layer.
func (s *MCPServer) Tools() []Tool {
	if s == nil || s.inner == nil {
		return nil
	}
	metas := s.inner.Tools()
	tools := make([]Tool, 0, len(metas))
	for _, m := range metas {
		tools = append(tools, &mcpTool{
			server: s,
			meta:   m,
		})
	}
	return tools
}

// Close shuts down the underlying MCP session. It is safe to call multiple
// times.
func (s *MCPServer) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

// MCPCapability returns the policy key for one namespaced MCP tool.
func MCPCapability(namespacedName string) CapabilityRef {
	return CapabilityRef{Kind: CapabilityMCPTool, Name: namespacedName}
}

// mcpTool adapts one MCP tool to the [Tool] interface so the conversation
// loop can execute it through the same path as built-in or user-supplied
// tools. The mcpToolMarker interface lets the conversation suppress the
// generic tool-result stream event and emit the compact MCP-specific event
// instead.
type mcpTool struct {
	server *MCPServer
	meta   mcpclient.ToolMetadata
}

// mcpToolMarker lets the conversation layer detect MCP-backed tools without
// importing the mcpclient package. policyTool forwards through to its base
// so a policy-wrapped MCP tool still answers truthfully.
type mcpToolMarker interface {
	mcpServerName() string
	mcpToolName() string
}

func (t *mcpTool) Name() string {
	return namespacedMCPName(t.server.Name(), t.meta.Name)
}

func (t *mcpTool) Description() string {
	return t.meta.Description
}

func (t *mcpTool) Parameters() map[string]any {
	return cloneMap(t.meta.InputSchema)
}

func (t *mcpTool) Execute(ctx context.Context, arguments string) (string, error) {
	if t.server == nil || t.server.inner == nil {
		return "", fmt.Errorf("llm: mcp tool %q has no live server", t.Name())
	}
	return t.server.inner.Call(ctx, t.meta.Name, arguments)
}

func (t *mcpTool) mcpServerName() string { return t.server.Name() }
func (t *mcpTool) mcpToolName() string   { return t.meta.Name }

// namespacedMCPName builds the LLM-facing tool name. Empty inputs collapse
// to the non-empty side so the LLM still sees something usable in
// pathological cases (the caller is also expected to validate non-empty
// names elsewhere).
func namespacedMCPName(server, tool string) string {
	if server == "" {
		return tool
	}
	if tool == "" {
		return server
	}
	return server + "__" + tool
}
