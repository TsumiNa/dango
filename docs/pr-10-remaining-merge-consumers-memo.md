# Remaining Merge Consumer Migration - PR 10 Memo

## Completed: PR 10 - Migrate remaining merge consumers

**PR**: https://github.com/TsumiNa/dango/pull/35

### Migrated Paths

All production stream merge paths now use hub mode:

- Planner skill stream to request stream through `mergeChildStream`.
- Runner stream to request stream through `mergeRunnerStream`.
- Executor-owned stream to runner stream through `Runner.mergeExecutorStream`.

Each path uses `stream.DefaultHubMergeWindowConfig`, whose shared tick is
`stream.DefaultMergeTickDuration`, and keeps the existing upstream subscription
buffer size. The shared hub config uses `stream.DefaultMergePerUpstreamBufferDepth`
for per-upstream FIFO buffering, currently sized to match the 4096-event
subscription buffers used by migrated runtime merges.

PR 12 follow-up: compatible hub-mode merges into the same downstream stream now
reuse one downstream-owned hub instead of creating one hub per `MergeWithConfig`
call. Planner, runner, and executor merge paths therefore share downstream-scoped
tick IDs and can emit one bundle containing ready items from multiple upstreams
within the same tick window.

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