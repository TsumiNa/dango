# 51 — MCP Client Wrapper (code)

Kind: code. First of the three MCP implementation subtasks split from `50`.

**Prerequisite.** `50` design accepted (this file lands together with the
implementation).

## Goal

Wrap the official `github.com/modelcontextprotocol/go-sdk` client in a thin
internal package the rest of the codebase can depend on without taking a
direct dependency on the SDK. Provide stdio lifecycle, tool listing, tool
calling, and result-to-string conversion.

## Scope

1. New package `internal/llm/internal/mcpclient`:
   - `Server` struct: holds the spec (name, command, args, env), the active
     `*mcp.ClientSession`, and the captured tool catalogue.
   - `Start(ctx, spec)`: builds `mcp.CommandTransport`, connects, calls
     `ListTools`, and returns the live `*Server`.
   - `Tools() []ToolMetadata`: returns the captured catalogue.
   - `Call(ctx, name, arguments)`: dispatches to `ClientSession.CallTool`,
     concatenates `Content` items into one string, truncates at 16 KiB,
     and returns `IsError == true` as a Go error.
   - `Close()`: closes the session, which closes stdin so the subprocess
     can terminate cleanly via the SDK's `pipeRWC.Close` path.
2. Result formatting:
   - `TextContent` → raw text in order.
   - `ImageContent` / `AudioContent` → `[image content, mime=…, bytes=…]`.
   - `ResourceLink` / `EmbeddedResource` → `[resource uri=…]`.
3. Test coverage uses `mcp.NewInMemoryTransports()` plus an in-process
   `mcp.Server` so the tests do not spawn real subprocesses.

## Tests

- `TestServerListsAndCallsTool`.
- `TestServerCallReturnsTextResult`.
- `TestServerCallErrorSurfacedAsGoError`.
- `TestServerTruncatesLargeResult`.
- `TestServerCloseStopsSession`.

## Out of scope

- HTTP / streamable transport (deferred — `Transport` plumbed but only
  stdio is wired today).
- Reconnection / list-changed notifications.

## Verifiable acceptance

- `go test ./internal/llm/internal/mcpclient/...` green.
- The package depends on the official SDK and exposes a surface narrow
  enough that the parent `llm` package adapter can wrap it without
  re-implementing JSON-RPC bookkeeping.
