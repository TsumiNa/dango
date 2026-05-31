# Memo: tighten the public `stream` surface (deferred)

Status: **deferred** — tracking note. Not a bug; the code is correct today. This
is about narrowing what `stream` exposes as public API before any external
consumer freezes onto the internal plumbing.

## Problem

`stream` (promoted to the module root for library use) currently exposes two
different audiences through one public surface:

**A. Consumer / subscriber API — keep public.** What a downstream module needs
to *read* events:
`Stream` (construct + `Subscribe`), `Subscription`, `Event`, `Scope`, `Source`,
`Filter`, the `Event*` / `Status*` constants, and subscribe options
(`WithSubscriberBuffer`, `WithReplayLast`, `WithReplayFrom`, `WithNoReplay`,
`WithOverflowPolicy`).

**B. Internal fan-in / bundle plumbing — should be private.** How the
orchestrator *assembles* the request stream from child planner/runner/skill
streams. Verified callers are only `orchestrator/request.go` and
`runner/skill_binding.go` (producer side) plus `stream`'s own internals:
- merge: `Stream.MergeFrom`, `Stream.MergeWithConfig`, `Merge`,
  `MergeWindowConfig`, `DefaultMergeWindowConfig`, `DefaultHubMergeWindowConfig`,
  `DefaultMergeTickDuration`, `DefaultMergePerUpstreamBufferDepth`.
- bundle wire format: `EventBatch`, `EncodeEventBatch`, `DecodeEventBatch`,
  `IsValidEventBatch`, `EventMergeBundle` (used only inside the `stream` package).

Group B is internal orchestration mechanics. A third party doing secondary
development consumes events; it never assembles dango's stream topology.

### Concrete downsides of exposing B

1. **Surface bloat / confusion** — consumers see `MergeWithConfig`, `EventBatch`,
   hub-window tuning, etc. that they never need, obscuring the small read API.
2. **Frozen plumbing** — if anyone depends on `MergeWithConfig` or the
   `EventBatch` wire format, reworking fan-in/bundling (tick windows, buffer
   depths, bundle encoding) becomes a breaking change. That machinery is exactly
   what we want to keep free to evolve.
3. **Bundle leak into the read path (the subtle one)** — hub mode packs several
   child events into one `merge.bundle` frame per tick, and those bundles are
   persisted to the event log. So a consumer reading history *must* know about
   bundles and call `stream.ExpandBundleEvent` (see
   `examples/honshu_groundwater/main.go` and `orchestrator/describe.go`). An internal
   optimization has leaked into the consumer API.

## Proposed tightening (when we do it)

- Move group B (merge/hub + bundle codec) into `stream/internal/...` or make it
  unexported, so in-dango producers still use it but third parties cannot.
- Make the consumer read path (`Subscribe` + event-log reads) expand bundles
  transparently, so `ExpandBundleEvent` / `EventBatch` / `merge.bundle` never
  appear in consumer code.
- Final public `stream` surface: `Stream` (+ `Subscribe`), `Subscription`,
  `Event`, `Scope`, `Source`, `Filter`, the `Event*`/`Status*` constants, and the
  subscribe options.

## Constraints / why deferred

- **Invariant:** `stream` must stay a zero-dango-internal-dependency leaf (8+
  packages import it). Any refactor must not pull orchestrator/llm/store imports into
  `stream`, or it creates an import cycle.
- Tightening now means adding an adapter/relocation layer, which the in-branch
  API-compatibility rule discourages doing speculatively. dango is early-stage
  with no external consumer yet. Best done at the first real external consumer or
  an API-freeze moment, in its own change.

See also `docs/cmd-serve-engine-wiring-memo.md` (the serve work that will be the
first non-`examples` consumer of the stream read path).
