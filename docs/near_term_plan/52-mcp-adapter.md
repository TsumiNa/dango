# 52 — MCP Tool Adapter (code)

Kind: code. Second of the three MCP implementation subtasks split from `50`.

**Prerequisite.** `51` merged.

## Goal

Adapt MCP tools (as surfaced by `mcpclient.Server`) into the existing
`llm.Tool` interface so the conversation loop, policy enforcement, and
session machinery treat them uniformly with builtins. Add the MCP-specific
stream event ("call only, no result body").

## Scope

1. New file `internal/llm/mcp.go`:
   - `MCPServerSpec` — user-facing struct (name, command, args, env).
   - `MCPServer` — handle returned by `StartMCPServer(ctx, spec)`. Wraps
     `mcpclient.Server` and exposes `Tools() []Tool`, `Close() error`,
     `Name() string`.
   - `mcpTool` — unexported `Tool` implementation. `Name()` returns
     `<server>__<tool>`. `Parameters()` returns the server's
     `InputSchema`. `Execute()` calls the MCP server and emits the
     MCP-specific call-completed stream event via the surrounding
     conversation.
2. New event constant in `internal/engine/stream/event.go`:
   `EventMCPToolCallCompleted = "mcp.tool.call.completed"`.
3. Conversation hook:
   - Suppress `EventLLMToolResultDelta` for MCP tools (an unexported
     `skipResultStreamTool` interface that `mcpTool` satisfies).
   - Emit `EventMCPToolCallCompleted` with `server`, `tool`,
     `namespaced_name`, `call_id`, `arguments_summary`, `outcome`, and
     optional `error`.
4. Capability ref kind:
   `CapabilityRef{Kind: CapabilityMCPTool, Name: "<server>__<tool>"}`.
   Already defined in `toolpolicy`; the adapter wires the policy lookup
   through the existing `policyTool` wrapping in `Skill.Bind`.

## Tests

- `TestMCPToolForwardsArgumentsAndReturnsResult`.
- `TestMCPToolNamespacedName`.
- `TestMCPToolSuppressesResultDeltaAndEmitsCallEvent`.
- `TestMCPServerCloseReleasesSubprocess`.

## Out of scope

- HTTP transport (still deferred — comes with the same future task as
  `51`'s out-of-scope item).
- Approval-flow UI; the adapter already feeds through the existing policy
  path, so `12b` benefits from this work for free.

## Verifiable acceptance

- `go test ./internal/llm/...` green.
- The conversation can execute a function-call against an MCP tool with
  no special-case branching outside the result-event suppression hook.
