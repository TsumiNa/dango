# MCP Support Plan

Status: design accepted, initial implementation lands together with this
document. Source design: `docs/near_term_plan/50-mcp-design.md`. Implementation
subtask files live alongside the design at `51-mcp-client.md`,
`52-mcp-adapter.md`, and `53-mcp-config-visibility.md`.

This document answers the seven questions enumerated in `50-mcp-design.md` and
records the contracts that the implementation honors.

## 1. Library and transport

- **Library.** `github.com/modelcontextprotocol/go-sdk` v1.6.1 (the official
  Go client maintained by the Model Context Protocol authors). Pinned in
  `go.mod`. A thin in-house client was rejected: the official SDK already
  covers initialization handshake, JSON-RPC framing, capability negotiation,
  list-changed notifications, and stdio/in-memory transports; reimplementing
  any of that would duplicate work that the protocol authors maintain.
- **Transport.** stdio (`mcp.CommandTransport`) is the only transport wired
  in this cycle. It is the deployment shape the ecosystem ships first and
  matches the "MCP servers run as external processes" decision in `50`. HTTP
  / streamable transports are intentionally deferred. The internal
  `mcpclient.Server` accepts a `Transport` so swapping in an HTTP transport
  later is additive — no signature change is needed.

## 2. Lifecycle

- **Spawn / stop.** `llm.StartMCPServer(ctx, spec)` builds a
  `mcp.CommandTransport` from the user-supplied command/args/env and calls
  `mcp.Client.Connect`. The caller (typically the orchestrator at startup,
  or skill-mounting code) keeps the returned `*llm.MCPServer` and calls
  `Close` during shutdown. The orchestrator owns shutdown for the servers it
  started; skills do not close servers they did not start.
- **Observation of crashes.** Because the SDK runs the subprocess via
  `exec.Cmd` and surfaces transport-level errors through `CallTool`
  responses, a crashed server first manifests as a failed tool call. The
  call's error string is forwarded to the LLM as `function_call_output` (so
  the model can self-correct on the next turn), and the
  `tool.execution.failed` event already published by the conversation layer
  carries the same error. We do not poll or proactively detect crashes
  outside the call path in this cycle.
- **Per-call timeout.** Inherited from `ctx`. The conversation already
  threads a context that can carry a deadline; this is sufficient until
  honshu reveals a need for an MCP-specific cap.
- **Reconnection.** None in this cycle. A failed connection forces the user
  to restart the server entry. Auto-reconnect needs a backoff and idempotency
  story; deferred to the post-alpha cycle alongside the broader
  external-service reliability concerns.
- **Tool list changes.** `list_changed` notifications are not consumed; the
  tool catalogue captured at connect time is held for the session. If a
  server adds tools mid-run they will only appear after the next restart. A
  follow-up subtask can subscribe to the notification once we have a
  real-world server that emits it.

## 3. Tool naming and collisions

- **Namespacing.** Every MCP tool is exposed to the LLM under
  `<server>__<tool>`, derived from `MCPServerSpec.Name` and the server's
  reported tool name. The double-underscore matches OpenAI's tool-name regex
  and is unlikely to appear in a builtin or user-defined tool.
- **Built-in collisions.** Because every MCP tool name starts with
  `<server>__`, it cannot collide with the always-available built-ins, which
  are bare lowercase identifiers (`bash`, `read_file`, …). The skill's
  existing duplicate-tool check (`validateTools`) catches accidental
  duplicates within one mount set.
- **Cross-server collisions.** Two MCP servers can advertise the same bare
  `tool` name without colliding because the server prefix differs. Two
  registrations with the **same** `MCPServerSpec.Name` are rejected by the
  orchestrator at registration time.
- **Relationship to `40`.** The skill alias model is about routing one
  *skill* under a chosen name. MCP namespacing is finer-grained (per tool)
  and operates on tool names, so it does not reuse `40`'s alias machinery.
  The relationship is consistent in spirit: ambiguity is resolved by
  user-controlled naming, not by silent precedence.

## 4. Schema and result handling

- **Argument schema.** The MCP tool's `InputSchema` (already a JSON-schema
  object) is forwarded to the LLM through the existing `Tool.Parameters()`
  contract. The adapter materializes it into a `map[string]any` so it flows
  through `buildToolParams` unchanged.
- **Result-to-string conversion.** `CallToolResult.Content` is concatenated
  into a single string in declaration order:
  - `TextContent` → its raw text.
  - `ImageContent` / `AudioContent` → a one-line stub of the form
    `[<kind> content, mime=<m>, bytes=<n>]`. Binary payloads are
    deliberately not base64-spliced into the LLM context.
  - `ResourceLink` / `EmbeddedResource` → a one-line stub naming the URI.
  - `IsError == true` → the assembled string is returned as a Go `error` so
    the conversation surfaces it to the model via the standard
    `error: <msg>\n<output>` framing.
- **Truncation.** The assembled string is truncated to **16 KiB**, mirroring
  the bash output cap. A truncation suffix (`\n…truncated`) is appended when
  the cap fires so the model knows to ask for a narrower call.

## 5. Config shape

- **Global servers** (visible to every skill) live on the orchestrator. The
  app/cmd entry point calls `Orchestrator.AddMCPServers(specs…)` at startup;
  the orchestrator spawns each server once and reuses the live handle for
  every later `AddSkills` call.
- **Per-skill servers** are declared by the user when mounting a skill, on
  the new field `SkillRegistration.MCPServers []*llm.MCPServer`. The
  orchestrator only appends those servers' tools to the one skill they were
  registered with.
- **Server tool registration.** The orchestrator translates each
  `*llm.MCPServer` into its `Tools()` list and appends them to the skill via
  the existing `Skill.AddTools(...)` path. From the conversation's point of
  view, an MCP tool is just another `Tool` implementation. This keeps the
  tool-execution loop, policy enforcement (`10`/`12a`), and approval flow
  (`12b`, when it lands) uniform across builtins, extras, and MCP.
- **Availability / policy axis (`10`).** Each MCP tool surfaces a
  `CapabilityRef{Kind: CapabilityMCPTool, Name: "<server>__<tool>"}`, so the
  per-runner policy map and dynamic adjustment API treat MCP tools as a
  first-class entry. The default policy is `passby` per `10`.
- **`SkillConfig`** is *not* extended with MCP fields. MCP visibility is a
  mount-time decision owned by whoever is wiring up the skill, not a SKILL.md
  setting, so it lives in the mounting struct (`SkillRegistration`), not in
  per-skill config carried in the SKILL.md.

## 6. Call-event contract

Per the design decision "results out, calls in", MCP tool *results* are not
written to the runtime stream. The existing `tool.execution.started` /
`tool.execution.completed` / `tool.execution.failed` events already advertise
the call (capability kind, name, policy, and error context); they are kept
as the primary "a call happened" signal so callers do not need to subscribe
to a new event family for MCP awareness.

In addition, the conversation emits an MCP-specific compact event for the
top-level caller that wants to filter only MCP traffic:

```
event_type: mcp.tool.call.completed
status:     completed | failed
delta: {
  "server":            "<server name>",
  "tool":              "<bare tool name>",
  "namespaced_name":   "<server>__<tool>",
  "call_id":           "<openai call id>",
  "arguments_summary": "<compact JSON, truncated to 4 KiB>",
  "outcome":           "ok" | "error",
  "error":             "<compact error text>"  // only when outcome == error
}
```

The full result body is **not** included. The matching
`llm.tool_result.delta` event (which carries the truncated tool output for
non-MCP tools) is suppressed for MCP tools by checking the adapter's
`isMCPTool()` marker before emitting, so MCP results stay confined to the
exchange / memo / handoff documents.

## 7. Implementation split

Three implementation subtasks land alongside this design. Each is
independently verifiable through its own colocated tests.

| File | Subtask | Scope |
| --- | --- | --- |
| `51-mcp-client.md` | MCP client wrapper | `internal/llm/internal/mcpclient`: connection management, `ListTools`, `CallTool`, result-to-string conversion, truncation, stdio transport. |
| `52-mcp-adapter.md` | LLM tool adapter | `internal/llm/mcp.go`: `MCPServerSpec`, `MCPServer`, `mcpTool` (`Tool` impl), namespacing, capability-ref kind, MCP-specific stream event. |
| `53-mcp-config-visibility.md` | Orchestrator visibility | `internal/engine/orchestrator.go`: `AddMCPServers` (global), `SkillRegistration.MCPServers` (per-skill), per-runner registration, shutdown ownership. |

## Startup listing and risk notice

Per `50`, the runtime prints the mounted MCP servers and a one-line risk
notice at startup. This is implemented in the orchestrator's
`AddMCPServers` call path: it logs one INFO-level line per registered
server (`name`, `command`, `tool_count`) and one WARN-level line stating
that user-supplied MCP servers run as external processes with host
privileges. The same WARN is emitted again if `Orchestrator.AddSkills` adds
per-skill MCP servers.

## Honshu observation

Recorded as planned: after the implementation lands, run the honshu example
to judge whether the call-only stream payload is the right granularity. The
expected adjustment is shaping `arguments_summary` — too compact and the
top-level caller cannot tell what the model asked for; too verbose and the
stream becomes noisy.

## Verifiable acceptance

- This document exists and answers the seven questions.
- `51`/`52`/`53` exist as implementation subtasks alongside `50`.
- Tests cover: stdio lifecycle, schema forwarding, result truncation,
  namespacing, isolation between global and per-skill servers, the call
  event payload, and the orchestrator shutdown path.
- `go test ./...`, `go vet ./...`, `go build ./...` are green.
