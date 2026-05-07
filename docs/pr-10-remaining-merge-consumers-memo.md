# Remaining Merge Consumer Migration - PR 10 Memo

## Completed: PR 10 - Migrate remaining merge consumers

### Migrated Paths

All production stream merge paths now use hub mode:

- Planner skill stream to request stream through `mergeChildStream`.
- Runner stream to request stream through `mergeRunnerStream`.
- Executor-owned stream to runner stream through `Runner.mergeExecutorStream`.

Each path uses a private `10 * time.Millisecond` merge window and keeps the
existing upstream subscription buffer size.

### Consumer Pattern

Live subscribers on migrated streams observe raw `merge.bundle` events. Consumers
that need logical child events expand each top-level event before matching nested
event types:

```go
events, err := stream.ExpandBundleEvent(event)
```

Tests that validate planner, executor, artifact, and skill memo events now use
bundle expansion before inspecting nested event type, source, scope, metadata,
or payload fields.

### Direct Forwarding

Direct-forwarding mode remains available through `MergeFrom` and through
`MergeWithConfig` with `TickDuration == 0`. It is still covered by stream package
tests, but production merge call sites no longer use it.

### Validation

- `TestStartRequest_ReturnsReplayableRequestStream` confirms planner output is
  delivered inside a request stream bundle.
- `TestRunner_PrepareNodeExecutor_MergesExecutorOwnedStream` confirms
  executor-to-runner delivery emits a raw bundle and preserves the nested event.
- Existing runner stream tests expand bundled executor events before checking
  artifact and skill memo behavior.
- `go test ./internal/engine/stream ./internal/engine/runner ./internal/engine`

## Follow-Up

If callers need a live subscription that yields expanded events directly, add it
as a separate API after observing real consumer demand. For now, raw stream
delivery plus explicit expansion keeps persisted and live stream traffic aligned.