# Memo: wire the engine into `cmd serve` (deferred)

Status: **deferred** — placeholder note, to be developed later.

## Current state

`cmd serve` (`cmd/serve.go` → `cmd/server`) is a stub. `server.New().Start(...)`
brings up a gin HTTP / Unix-socket server that only serves `/api/v1/ping` and
`/api/v1/hello`. It does **not** construct or run the engine, store, or llm —
`cmd/server` imports none of them. Today the only place the orchestrator engine
is actually driven is the in-process `examples/honshu_groundwater` binary.

So: running `dango serve` does not currently expose the agent engine or stream
any engine events to a client.

## What "done" looks like

`serve` should become a real host for the library:

1. Build the runtime: `store/runtime.Open(runtime.Config{...})` for persistence,
   construct an `llm.Client`, then `engine.NewOrchestrator(WithPersistence(...),
   WithLogger(...), ...)` and register skills / MCP servers.
2. Accept a request over HTTP and call `orchestrator.StartRequest(ctx, engine.Request{...})`.
3. Bridge the returned `resp.Stream` to a streaming transport (SSE or WebSocket):
   `resp.Stream.Subscribe(stream.Filter{...}, stream.WithSubscriberBuffer(...))`
   → drain the `*stream.Subscription` and forward each `stream.Event` to the client.
   Per-runner streams are available via `orchestrator.SubscribeRunnerStream(id, ...)`.
4. Expose query/describe endpoints (`orchestrator.DescribeRequest`, runner lookup)
   for clients that poll instead of streaming.

## Pointers / reference

- Reference consumer wiring (the pattern to mirror): `examples/honshu_groundwater/main.go`.
- Public entrypoints: `engine.NewOrchestrator`, `engine.Orchestrator.StartRequest`,
  `engine.Response.Stream`, `engine.Orchestrator.SubscribeRunnerStream`,
  `store/runtime.Open`, `streamrender` (for a terminal client).

## Gotcha: hub-mode bundle expansion

When reading persisted events from the event log (and when subscribing in
hub/tick-bundled mode), some frames arrive as `merge.bundle` events that pack
several child events together. A consumer must call `stream.ExpandBundleEvent`
to expand them — see how `examples/honshu_groundwater/main.go` and
`engine/describe.go` handle it. (This bundle concept currently leaks into the
consumer read path; tightening that is tracked separately as a stream public-API
cleanup, not part of this serve work.)
