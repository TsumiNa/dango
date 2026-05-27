package llm

import (
	"context"
	"fmt"

	"github.com/tsumina/dango/internal/mcpclient"
)

// MCPTools adapts every tool advertised by srv into an [Tool] that the
// conversation loop can dispatch through the same path as built-in or
// user-supplied tools.
//
// Each adapter's Name() returns the namespaced form "<server>__<tool>"
// drawn from [mcpclient.Server.Name] and the bare tool name. Parameters()
// forwards the server's JSON input schema verbatim. Execute() dispatches
// the call through srv; results are concatenated and truncated by the
// mcpclient layer.
//
// The caller owns srv's lifecycle. MCPTools captures srv by pointer, so
// closing srv while any of the returned tools is in flight will surface
// as a tool-call error on the next invocation.
func MCPTools(srv *mcpclient.Server) []Tool {
	if srv == nil {
		return nil
	}
	metas := srv.Tools()
	tools := make([]Tool, 0, len(metas))
	for _, m := range metas {
		tools = append(tools, &mcpTool{
			server: srv,
			meta:   m,
		})
	}
	return tools
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
	server *mcpclient.Server
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
	if t.server == nil {
		return "", fmt.Errorf("llm: mcp tool %q has no live server", t.Name())
	}
	return t.server.Call(ctx, t.meta.Name, arguments)
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
