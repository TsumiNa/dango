# Tool-Call Audit Event Schema

Status: stable contract. Source events live in
`internal/engine/stream/event.go`; emission lives in
`internal/llm/conversation_stream_events.go`. Last updated 2026-05-28
alongside subtask `60a`.

This document is the stability contract that downstream consumers
(the trace analyzer in `tools/analyze-tool-traces`; the post-alpha
audit storage) can rely on. Any field rename or semantic change must
land alongside an update here.

## Audit category

Every event in the audit trio carries the metadata field

```
"category": "audit"
```

merged into `Event.Metadata`. Downstream consumers should filter on
that field rather than on an event-type allowlist; new audit-bearing
event types added in the future will carry the same tag.

The audit category is applied to **four** event types:

| `event_type`                 | Role in the audit trio                                                                                                       |
| ---                          | ---                                                                                                                          |
| `llm.tool_call.started`      | The LLM has emitted a `function_call` and the conversation has accepted it. Carries the tool name and arguments.             |
| `llm.tool_call.completed`    | Same call, marked completed when the LLM's response stream emits the matching `function_call` output item.                    |
| `llm.tool_result.delta`      | The tool finished executing; carries the (truncated) output string. **Not emitted for MCP tools** — see the row below.       |
| `mcp.tool.call.completed`    | MCP-specific replacement for the result delta. Carries only the call metadata (no result body) — see `docs/mcp-support-plan.md` §6. |

The three events for one call share a `delta.call_id` (the OpenAI
function-call id, e.g. `call_1`) so the audit consumer can stitch the
records back together without holding per-call state across event
types.

## Common envelope (every audit event)

| Field                  | Type              | Meaning                                                                                                    |
| ---                    | ---               | ---                                                                                                        |
| `event_type`           | string            | One of the four types above.                                                                               |
| `status`               | string            | `running` for `started`, `completed` for `completed`/result success, `failed` for execution-failed results. |
| `sequence_number`      | uint64            | Per-stream monotonic counter. Assigned by `Stream.Emit`.                                                   |
| `logical_time`         | uint64            | Per-stream monotonic logical timestamp; orders bundles, replay, debugging.                                 |
| `timestamp`            | RFC 3339 UTC time | Wall-clock time the event was emitted.                                                                     |
| `from.layer`           | string            | The runtime layer that emitted the event (`skill`, `runner`, etc.).                                        |
| `from.id`              | string            | The bound skill instance identifier (when present) — this is the **skill ID** the audit pipeline keys on.  |
| `from.parent_id`       | string            | Parent runtime identifier when applicable.                                                                 |
| `scope.request_id`     | string            | Orchestrator request ID.                                                                                   |
| `scope.runner_id`      | string            | Runner instance ID.                                                                                        |
| `scope.node_id`        | string            | Plan node ID when the call originates inside a runner-managed node.                                        |
| `scope.session_id`     | string            | Session ID when a session store is attached to the conversation.                                           |
| `metadata.category`    | `"audit"`         | Audit-pipeline marker.                                                                                     |

## Per-event `delta` fields

### `llm.tool_call.started` / `llm.tool_call.completed`

| Field                       | Type              | Truncation                                       |
| ---                         | ---               | ---                                              |
| `call_id`                   | string            | —                                                |
| `name`                      | string            | —                                                |
| `arguments`                 | string (JSON)     | 4096 bytes via `compactJSONText`                 |
| `arguments_truncated`       | bool (optional)   | Present and `true` when `arguments` was clipped. |

### `llm.tool_result.delta`

| Field        | Type   | Truncation                                                                                       |
| ---          | ---    | ---                                                                                              |
| `call_id`    | string | —                                                                                                |
| `name`       | string | The dispatched tool name when known; absent if the conversation can no longer correlate the id.  |
| `output`     | string | 4096 bytes via `compactConversationText`                                                         |
| `truncated`  | bool (optional) | Present and `true` when `output` was clipped.                                          |
| `error`      | string (optional) | Compact error text (whitespace-normalised, 512 bytes) when the tool returned an error.    |

### `mcp.tool.call.completed`

| Field               | Type              | Truncation                                                |
| ---                 | ---               | ---                                                       |
| `server`            | string            | —                                                         |
| `tool`              | string            | The bare tool name reported by the server (no namespace). |
| `namespaced_name`   | string            | `<server>__<tool>`.                                       |
| `call_id`           | string            | —                                                         |
| `arguments_summary` | string (JSON)     | 4096 bytes via `compactJSONText`                          |
| `outcome`           | `"ok"` / `"error"` | —                                                        |
| `error`             | string (optional) | Compact error text (≤ 512 bytes).                         |

MCP tools deliberately do **not** emit `llm.tool_result.delta`; the
result body stays in the exchange / memo / handoff documents.

## Truncation caps

| Source                                    | Cap        | Constant                                  |
| ---                                       | ---        | ---                                       |
| `arguments` / `arguments_summary`         | 4096 bytes | `conversationStreamTextLimit`             |
| `output`                                  | 4096 bytes | `conversationStreamTextLimit`             |
| `error`                                   | 512 bytes  | inline in `compactErrorText`              |

The 4096-byte cap is intentionally smaller than the bash 16 KiB output
cap: the audit stream is meant for fast scanning and post-hoc
analysis, not for reproducing full tool output. Consumers that need
the full output should read the exchange / memo / handoff documents
the runner already writes.

## Stability guarantees

- Field names, types, and the `category: "audit"` marker are stable.
  Renaming any of them requires a coordinated downstream update.
- New optional fields may be added; consumers must tolerate unknown
  keys.
- Truncation caps may shrink with notice; consumers must tolerate
  outputs at or under the documented cap.
- Event types may gain the audit tag, but no event type already
  tagged here will lose it without a deprecation note.
