# Stream Merge Hub Mode Migration - PR 8 Memo

## Completed: PR 8 - Migrate one real caller to hub mode

### Migrated Path

The runner-to-request stream merge path now uses hub mode:

```go
request stream <- runner event stream
```

Planning skill streams merged into the request stream remain in direct-forwarding mode. Executor-to-runner stream merges also remain in direct-forwarding mode.

### Public Configuration

No new public API was added for this migration. The migrated path uses the existing stream merge configuration:

```go
streampkg.MergeWindowConfig{
	TickDuration: 10 * time.Millisecond,
}
```

The per-upstream buffer depth uses the existing stream default.

### Observed Bundle Shape

The request stream receives top-level `merge.bundle` events from the hub. Each bundle delta contains the nested runner events that were ready for that tick:

```json
{
  "tick_id": 1,
  "nested_events": [
    {
      "event_type": "runner.node.completed",
      "from": {"layer": "runner", "id": "run_..."},
      "status": "completed",
      "scope": {"runner_id": "run_...", "node_id": "only"},
      "metadata": {"runner_id": "run_...", "node_id": "only", "phase": "executing"}
    }
  ]
}
```

Nested events keep the merged request/runner/node scope and preserve their original source layer. Consumers that need the logical runner events should call `stream.ExpandBundleEvent` before matching event types.

### Consumer Migration Pattern

Request stream consumers expand each received event before matching event types:

```go
expanded, err := stream.ExpandBundleEvent(event)
if err != nil {
	return err
}
for _, nested := range expanded {
	// Match nested.EventType and scope as before.
}
```

This handles direct request-owned events and bundled runner events with one path.

The terminal stream renderer also expands events during subscription rendering after observing the raw top-level event. This keeps debug JSONL logs faithful to the request stream while rendering nested runner events as normal terminal updates.

### Test Coverage

`internal/engine/request_test.go` verifies that:

- The migrated request stream receives at least one `merge.bundle` event.
- Expanded bundled runner events still expose runner phase, node completion, and executor completion events to the consumer.
- Direct orchestrator planning events remain visible as direct request stream events.

`internal/streamrender/renderer_test.go` verifies that:

- The stream renderer observes raw bundle events but renders their nested runner events.

Existing runner stream tests continue to cover the unmigrated executor-to-runner path as direct-event behavior.

## Next: PR 9 - Add replay and persistence handling for bundles

Confirm stored and replayed bundle events preserve live bundle semantics, then add expanded replay helpers where callers need nested event replay.
