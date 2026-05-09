# Exchange / Memo / Handoff Refactor Memo

Last updated: 2026-05-09

This memo replaces the earlier "exchange upgrade" notes. It records the shipped
rewrite of dango's three core agent-information channels — **memo**,
**exchange**, and **handoff** — together with the runner support, persistence
design, and built-in prompt layout that surround them.

**Status:** Implemented on this branch, with explicit post-PR deviations tracked
below.

## Why a Rewrite

The current `runner.ExchangeDocument` (see `internal/engine/runner/exchange.go`)
collapses three different communication needs into a single markdown document
with `Memo`, `Reasoning`, and `Handoff` sections plus stage/recipient/intent
metadata. In practice this conflates three distinct flows:

1. A skill's *own* working state across multi-step tool calls.
2. Information a skill voluntarily exposes to peers as shared context.
3. The deliverable a skill must hand to its DAG successors when it finishes.

Collapsing them has produced a few visible problems:

- Executors emit one bloated document per stage, even when peers do not need
  the inner reasoning trace; downstream context windows pay for it anyway.
- Orchestrator review and downstream consumption read from the same envelope
  with only the `to`/`intent` front matter to discriminate.
- Persistence is ad hoc: see the ~150 lines of bespoke debug-artifact writers
  in `examples/honshu_groundwater/main.go` (`writePersistenceDebugArtifacts`,
  `summarizeDescribeView`, `summarizeRunnerRecords`, `summarizeSnapshotCursor`,
  etc.). Each consumer reinvents its own snapshot scheme on top of the runner.
- Built-in prompts are split between an embedded skill markdown
  (`internal/engine/builtin/SKILL.md`) and inline string builders for executor
  stages (`polishPrompt`, `executionPrompt`, `reportPrompt` in
  `executor_exchange.go`). There is no single place to read or edit the full
  agent contract.

The rewrite separates the three flows, gives each its own routing and
persistence rules, and consolidates built-in prompt material into a
markdown/template tree that the engine embeds.

## Conceptual Model

The system has **one transport** (the existing stream) and **three named
channels** with different audiences.

### Stream — the transport

The stream remains what `internal/engine/stream` already implements: a single
ordered JSON event bus. Producers emit; subscribers (runner, persistence
sinks, terminal renderers, tests) attach with filters. The refactor adds new
event families on top of the existing stream — it does not change the stream
core.

### Exchange — the public whiteboard

A shared workspace any skill (including the orchestrator) can write to when it
believes the information may be useful to peers later. Examples: a normalised
schema description, a reusable lookup table, a shared assumption that
constrains downstream choices.

- **Audience:** all skills in the runner, plus subscribers.
- **Producer:** any skill or the orchestrator.
- **Routing:** broadcast. Runner aggregates exchange entries into a single
  public folder readable by every executor's runtime skill.
- **Form:** front-mattered markdown, streamed as JSON.
- **Lifetime:** lives for the runner's lifetime (or longer if persisted).

### Memo — the skill's private notebook

A skill's own running state — TODOs before a long tool chain, CoT/ReAct
conclusions, lifecycle invariants, intermediate calculations the skill wants
to revisit. Skills maintain memos through ordinary shell tools (`cat`, `sed`,
`grep`, `awk`, `cp`, `mv`) inside their workspace.

- **Audience:** the skill itself only. The runner may read for archival and
  debugging; no other skill ever sees it.
- **Producer:** the skill, via filesystem operations on `memo/` in its
  workspace.
- **Routing:** none. Stays in the skill's workspace.
- **Form:** markdown. The runner does **not** prescribe a body schema; only
  the location (`<skill-workspace>/memo/`) is fixed.
- **Lifetime:** runner lifetime; the runner snapshots and archives during
  persistence.

### Handoff — the directed parcel to next skill(s)

After a skill finishes its task and produces artifacts, it writes a handoff
that explains, for each downstream skill, *what artifacts exist and how to
use them*. The handoff body itself stays small; bulky material (sample code,
test fixtures, dataset dictionaries) travels as referenced artifacts.

- **Audience:** the next skill(s) in the DAG only. The runner delivers.
- **Producer:** the finishing skill.
- **Routing:** one-to-many along DAG edges. The runner reads the handoff,
  resolves successors, copies/links the handoff and its referenced artifacts
  into each successor's inbox.
- **Form:** front-mattered markdown referencing artifact paths, plus optional
  per-artifact descriptors (column descriptions, summary stats, sample-code
  pointers for CSV/Parquet/Excel/pandas/etc.).
- **Lifetime:** persists with the runner; the runner archives both the
  handoff and the artifacts.

The orchestrator's coarse plan is structurally a handoff: it is produced
before the DAG is finalised and is delivered to the initial-layer skills as
their starting brief. The runner treats it through the same handoff
machinery, with a "plan" stage marker so subscribers can tell the bootstrap
case apart.

### At-a-glance comparison

| Channel  | Audience            | Routing           | Storage location                     | Schema strictness         |
| -------- | ------------------- | ----------------- | ------------------------------------ | ------------------------- |
| Memo     | Skill + runner only | None (filesystem) | `<skill-workspace>/memo/`            | None (free-form markdown) |
| Exchange | All skills + subs   | Broadcast         | `<runner-root>/exchange/`            | Front matter required     |
| Handoff  | Listed successors   | DAG one-to-many   | Successor `<skill-workspace>/inbox/` | Front matter required     |

## Workspace Layout

Every runner gets a root directory. Underneath:

```
<runner-root>/
  exchange/                    # public whiteboard, runner-curated
    <seq>-<producer>.md
  skills/
    <node-id>/
      memo/                    # skill-private (the skill owns these files)
        <free-form>.md
      inbox/                   # runner-delivered handoffs from upstream
        <upstream-node-id>/
          handoff.md
          artifacts/...
      outbox/                  # skill-emitted handoff drafts (runner consumes)
        handoff.md
        artifacts/...
      workspace/               # skill scratch area (tools/bash playground)
  archive/                     # runner persistence sink (configurable)
```

Access rules:

- Skill `<node-id>` may read+write its own `memo/`, `outbox/`, `workspace/`.
- Skill `<node-id>` may read its own `inbox/` and `<runner-root>/exchange/`.
- Skill `<node-id>` must not see any other skill's `memo/`, `outbox/`, or
  `workspace/`. The runner enforces this when it provisions tool sandboxes.
- Only the runner writes to `inbox/`, `archive/`, and
  `<runner-root>/exchange/`.

## Data Flow

```
       ┌─────────────────────────────────────────────────────┐
       │                       Stream                        │
       │   (one ordered JSON event bus, existing core)       │
       └──┬───────────────┬───────────────┬──────────────────┘
   emits  │      emits    │       emits   │
          │               │               │
   ┌──────▼─────┐  ┌──────▼─────┐  ┌──────▼──────┐
   │  exchange. │  │  handoff.  │  │ memo.       │
   │  published │  │  emitted   │  │ snapshot    │
   └──────┬─────┘  └──────┬─────┘  └──────┬──────┘
          │               │               │
          ▼               ▼               ▼
   ┌────────────────────────────────────────────┐
   │                  Runner                    │
   │   routes per DAG, persists per config      │
   └──┬─────────────┬───────────────┬───────────┘
      │             │               │
      ▼             ▼               ▼
  exchange/   inbox/<successor>   archive/
```

### Skill side

- A skill writes a memo file directly (e.g. `memo/plan.md`) using its bash
  tooling. No stream event is required for the body; the runner snapshots
  the directory at checkpoints.
- A skill calls a runner-provided primitive to publish an exchange document
  or to emit a handoff. Both go out as stream events.
- A skill never writes to another skill's directories or to the public
  exchange folder directly. The runner is the only writer for those paths.

### Runner side

- Receives `exchange.published` events; appends the document under
  `<runner-root>/exchange/` and rebroadcasts on the merged subscription.
- Receives `handoff.emitted` events; consults the DAG, resolves successors,
  copies the handoff (and any referenced artifacts) into each successor's
  `inbox/<producer-node-id>/`.
- On node completion, snapshots `memo/` for archival.
- All three flows feed the persistence pipeline (next section).

### Subscriber side

Subscribers consume the merged stream as today. Three new event families are
added:

- `exchange.published` — a new public-whiteboard entry.
- `handoff.emitted` / `handoff.delivered` — emit on the producer side, deliver
  on the runner side after routing.
- `memo.snapshot` — runner-emitted, includes the snapshot path; the body
  itself is not inlined.

## Ownership: Orchestrator Owns Runner Lifecycle

A runner is **not** constructed by the caller. The caller constructs an
`Orchestrator`, calls `StartRequest`, and the orchestrator's
`newRunnerFromPlan` (in `internal/engine/request.go`) is the single place that
calls `runnerpkg.New(...)`. Any configuration that needs to reach the runner
has to flow through the orchestrator.

That ownership constraint shapes the persistence and workspace design below:
both are configured on the orchestrator and forwarded internally to each
runner the orchestrator creates.

## Persistence Design (unified)

The orchestrator gains one persistence option, configured at construction
time, that covers all three channels and the existing event log:

```go
orchestrator := orchestrate.NewOrchestrator(
    orchestrate.WithOrchestratorContext(ctx),
    orchestrate.WithOrchestratorLogger(logger),
    orchestrate.WithPersistence(persistence.Markdown(rootDir)),
    // alternative: persistence.SQLite(db), persistence.Composite(...)
)
```

The three current orchestrator options — `WithRunnerStore`,
`WithEventLogStore`, `WithSnapshotCursorStore` — are deleted. The new
`WithPersistence` replaces all three.

Backends implement a single interface that handles every artefact the runner
produces: stream event records, exchange documents, handoffs, artifacts, memo
snapshots, runner records, and the cursor needed to resume. The
`writePersistenceDebugArtifacts` family in
`examples/honshu_groundwater/main.go` is replaced by one call to
`orchestrate.WithPersistence(persistence.Markdown(...))` and a tiny
example-side helper that just points at the persistence root.

Backends to ship in this refactor:

- **None** — default, drops everything. Useful for tests.
- **Markdown** — writes a directory tree mirroring the workspace layout.
  Good for human inspection and for the Honshu example.
- **Composite** — fan-out wrapper for combining Markdown with a database
  backend later. The interface is fixed in this refactor; SQLite/Postgres
  backends can land outside this branch (the existing `internal/store/runtime`
  Open helper is rewired to return a `Composite(SQLite, Markdown)` backend so
  the Honshu example keeps both behaviours).

The persistence interface lives in a new
`internal/engine/runner/persistence/` subpackage so the runner does not depend
on a specific store implementation. The existing `runnerpkg.RunnerStore`,
`storepkg.EventLogStore`, and `storepkg.SnapshotCursorStore` are migrated into
this single interface; consumers configure one backend rather than three.

### How persistence flows from orchestrator to runner

The orchestrator holds **one** `persistence.Backend` for its entire lifetime
and many runners. The backend carries the user-configured *global* workspace
root (e.g. `/workspace/`). The orchestrator does **not** compute per-runner
subdirectories — that decision belongs to the runner so the orchestrator
keeps holding only runner IDs as identity.

When `StartRequest` is accepted, the orchestrator:

1. Hands the runner the global root (via the persistence handle) plus the
   configured path rule (see next subsection).
2. Calls `runnerpkg.New(..., runnerpkg.WithPersistenceHandle(handle), runnerpkg.WithRootPathRule(rule))`.

When the runner starts, it:

1. Calls the path rule with its own assigned runner ID to compute a
   subdirectory name (e.g. `task_<runnerID>`).
2. Joins that under the global root to produce its per-runner workspace
   root (e.g. `/workspace/task_<runnerID>/`).
3. Provisions the workspace tree (`exchange/`, `skills/...`, `archive/`)
   and registers itself with the persistence backend at that path.

The orchestrator never reads or constructs the per-runner path. To find a
runner's data later, it goes through the persistence backend with just the
runner ID; the backend resolves the path through the same rule.

### Path rules

A path rule is a small function that maps a runner ID to a subdirectory
name (relative to the persistence root):

```go
package persistence

// PathRule returns the per-runner workspace subdirectory under the global
// persistence root. The returned value must be a single path segment or a
// relative path that does not escape the root (no leading "/", no "..").
type PathRule func(runnerID string) string

// DefaultPathRule produces "task_<runnerID>".
func DefaultPathRule(runnerID string) string { return "task_" + runnerID }
```

Both the orchestrator and the runner expose a constructor option for it:

- `orchestrate.WithRunnerPathRule(rule)` — sets the default rule the
  orchestrator forwards to every runner it creates. Optional. If omitted,
  the orchestrator forwards `persistence.DefaultPathRule`.
- `runnerpkg.WithRootPathRule(rule)` — sets the rule directly on a runner.
  Optional. If omitted, the runner uses whatever the orchestrator forwarded;
  if neither is set (e.g. a unit test that calls `runnerpkg.New` directly,
  in test code only — production code never does this), it falls back to
  `persistence.DefaultPathRule`.

Both options accept the same `PathRule` type so a rule chosen at the
orchestrator layer is byte-identical to the one a test injects on the
runner directly.

Constraints validated by the runner before creating any directory:

- Returned path is non-empty, contains no `..` segment, no absolute prefix,
  and resolves under the global root.
- Returned path is unique per runner ID — collisions are rejected at
  provision time.

### Request.ArtifactsDir

`Request.ArtifactsDir` today plays two roles: a user-supplied output
directory *and* the path that gets fed into executor `accessibleDirs`. After
the refactor, the workspace is fully runner-managed, so:

- `Request.ArtifactsDir` keeps its meaning as the **user-facing** output
  directory: a place the caller wants final deliverables copied to. The
  orchestrator instructs the runner to mirror specified handoff artifacts
  into this directory at terminal phases. It no longer participates in
  executor sandbox setup.
- All internal artifacts (memos, exchange entries, handoffs, archive)
  live under the persistence-controlled workspace root, never under
  `Request.ArtifactsDir`.
- `runnerpkg.WithAllowedResourceRoots(req.ArtifactsDir)` (currently in
  `request.go::newRunnerFromPlan`) is replaced by allowed-roots wiring that
  the workspace provisioner emits from PR 2.

## Built-in Prompt / Instruction Extraction

Goal: make the agent contract a set of files developers can read and edit, and
keep a clear seam for future user override.

- All built-in prompts move under `internal/engine/builtin/prompts/`:

  ```
  prompts/
    orchestrator/
      planning.md
      review.md
      replan.md
    executor/
      polish.md
      execute.md
      report.md
    common/
      memo-discipline.md
      handoff-discipline.md
      exchange-discipline.md
  ```

- Each file is a Go `text/template` rendered with the runtime context
  (task description, parent handoffs, available tools, etc.). Files are
  embedded via `//go:embed`.
- A small `prompts.Renderer` in the same package owns embedding, parsing, and
  rendering. Code that today builds prompts inline (`polishPrompt`,
  `executionPrompt`, `reportPrompt`) calls the renderer instead.
- The renderer accepts an optional override map keyed by template path.
  Exposing that hook through public orchestrator/runner options is approved
  as advanced usage. The docs should mark the option as advanced, and the
  initial public surface does not need a dedicated example.

## Hard Requirements

1. **No backwards compatibility.** Old `ExchangeDocument`,
   `ExchangeStage*`, `ExchangeRecipient*`, `ExchangeIntent*`,
   `ExchangeHandoff`, `ExchangeResource`, draft-parser helpers, and their
   call sites are deleted, not aliased. Tests are rewritten against the new
   types.
2. **Unified persistence on the orchestrator.** The orchestrator exposes a
   single `WithPersistence` option; the three legacy options
   (`WithRunnerStore`, `WithEventLogStore`, `WithSnapshotCursorStore`) are
   removed. Examples must not reach into runners to reconstruct snapshots
   themselves.
3. **Prompts as files, not strings.** No new inline prompt strings land in
   `internal/engine`; everything goes through the renderer.
4. **Workspace isolation enforced by the runner.** A skill must not be able
   to read another skill's memo, even by accident, through the tools the
   runner installs in its sandbox.
5. **Stream stays the only transport.** Routing decisions live in the
   runner, not in producers. Skills emit events; the runner decides who sees
   what.

## Surface to Delete

After the refactor lands, the following symbols and files are gone:

- `internal/engine/runner/exchange.go` — replaced by three smaller files
  (`exchange_doc.go`, `handoff_doc.go`, `memo_snapshot.go`).
- `internal/engine/runner/exchange_output.go`,
  `internal/engine/runner/exchange_resources.go` — folded into the new
  handoff routing code.
- `internal/engine/executor_exchange.go` — replaced by separate
  polish/execute/report functions that emit handoffs (and optionally
  exchange entries) instead of one combined document.
- `internal/engine/orchestrator.go::WithRunnerStore`,
  `WithEventLogStore`, `WithSnapshotCursorStore` — replaced by a single
  `WithPersistence` option.
- `examples/honshu_groundwater/main.go::writePersistenceDebugArtifacts` and
  the related summary structs — replaced by the unified persistence backend.
- Inline prompt builders in the executor — replaced by template files.

## Orchestrator-Side Minimal Adjustments

Orchestrator is **not** a refactor target, but the following minimum changes
are required for the new model to land. They should be the smallest delta
that exposes the new contract; they all live in PR 4 unless noted.

- Replace the three persistence options with `WithPersistence(backend)`.
  Internally, the orchestrator stores the backend handle and removes its
  three current store fields.
- Add `orchestrate.WithRunnerPathRule(rule)` (optional). The orchestrator
  stores the rule on itself and forwards it to every runner it creates.
- In `request.go::newRunnerFromPlan`:
  - Pass the persistence handle to `runnerpkg.New` via a new internal-only
    option (`runnerpkg.WithPersistenceHandle`), replacing
    `runnerpkg.WithStore(store)`. The handle exposes the global root only
    — no per-runner subdirectory is computed here.
  - Forward the orchestrator's path rule via `runnerpkg.WithRootPathRule`.
  - Drop `runnerpkg.WithAllowedResourceRoots(req.ArtifactsDir)` — accessible
    dirs come from the workspace provisioner instead.
  - Stop appending `req.ArtifactsDir` into per-skill `AccessibleDirs`. The
    orchestrator only uses `Request.ArtifactsDir` to receive the runner's
    final-deliverable mirror at terminal phases.
- In `orchestrator.go`, the `LoadRunnerRecords` / `DescribeRequest` /
  `WaitRunner` paths are rewired to read through the persistence handle by
  runner ID (the backend resolves paths through the same rule). Public
  method signatures do not change. The orchestrator still only holds runner
  IDs as identity — it never stores per-runner paths itself.
- Stream merging (`request.go::mergeRunnerStream`) stays unchanged: the
  three new event families ride the existing merge path so subscribers
  attached at the orchestrator see them automatically.

These changes are surgical: no orchestrator semantics, no public method
signatures, and no planning-flow logic shift. They only swap the storage
plumbing.

## PR Breakdown (shipped sequence)

Each PR must build, test, and (where applicable) run the Honshu example to
green on its own. Numbered PRs are sequential where dependencies require it;
the prompt-extraction PR can land in parallel with the type work.

### PR 1 — Memo, Exchange, Handoff types and parsers

- Add `internal/engine/runner/memo.go`, `exchange_doc.go`, `handoff_doc.go`
  with the three new front-mattered document types.
- Implement `Markdown()` / `Parse*Markdown()` for each.
- Unit tests cover round-trip, missing fields, and rejection of legacy
  `dango.exchange` documents.
- The old `ExchangeDocument` symbols still exist but are now unused by tests
  in this PR; deletion happens in PR 4.
- **Test signal:** `go test ./internal/engine/runner/...` passes; new types
  cover ≥90% of their own files.

### PR 2 — Workspace layout, path rule, and provisioner

- Introduce `runner.Workspace` that owns the directory tree above.
- Define `persistence.PathRule` (`func(runnerID string) string`) and
  `persistence.DefaultPathRule` that returns `"task_" + runnerID`. The
  type lives in the new `persistence` subpackage so both layers can import
  it without depending on each other.
- Workspace provisioning: given a global root, a runner ID, and a path
  rule, the provisioner computes the per-runner workspace root, creates
  the directory tree, and produces per-skill `AccessibleDirs`. The
  provisioner validates the rule output (non-empty, no `..`, resolves
  under the global root, unique per runner ID).
- Tests using `t.TempDir()` verify path layout, isolation invariants,
  inbox routing copies, and rule-validation rejection paths.
- No skills consume the workspace yet; the executor still uses its current
  exchange flow.
- **Test signal:** workspace tests pass; permission tests prove a skill
  cannot resolve a path outside its sandbox; bad-rule tests cover empty,
  absolute, and parent-escaping outputs.

### PR 3 — Stream event families for memo / exchange / handoff

- Define `exchange.published`, `handoff.emitted`, `handoff.delivered`,
  `memo.snapshot` event payloads in `internal/engine/stream`.
- Add subscription filters and replay tests.
- Producers and consumers do not yet emit/consume these in production; the
  PR only adds the contract.
- **Test signal:** `go test ./internal/engine/stream/...` plus new
  contract tests.

### PR 4 — Runner dispatcher, unified persistence, and orchestrator wiring

- Replace the runner's existing exchange handling with a dispatcher that
  routes the new events into workspace paths.
- Introduce `internal/engine/runner/persistence/` with the unified
  interface, the `none` backend, and the `markdown` backend.
- Migrate `RunnerStore`, `EventLogStore`, `SnapshotCursorStore` into the new
  interface. Delete the legacy ad-hoc store wiring.
- **Orchestrator changes (minimal, in this same PR):**
  - Add `orchestrate.WithPersistence(backend)`; remove
    `WithRunnerStore`, `WithEventLogStore`, `WithSnapshotCursorStore`.
  - Add `orchestrate.WithRunnerPathRule(rule)` (optional, defaults to
    `persistence.DefaultPathRule`).
  - Add `runnerpkg.WithRootPathRule(rule)` and
    `runnerpkg.WithPersistenceHandle(handle)` as runner-side options. The
    orchestrator forwards both when calling `runnerpkg.New`.
  - In `request.go::newRunnerFromPlan`, hand the runner the persistence
    handle (carrying the global root) and the path rule. The runner
    decides its own per-runner subdirectory at provision time. Remove
    `runnerpkg.WithStore(store)`.
  - Stop pushing `req.ArtifactsDir` into per-skill `AccessibleDirs` and
    drop `runnerpkg.WithAllowedResourceRoots(req.ArtifactsDir)` — those
    paths now come from the workspace provisioner introduced in PR 2.
  - Rewire `LoadRunnerRecords` / `DescribeRequest` / `WaitRunner` to read
    through the handle by runner ID. Public signatures unchanged.
- Update `internal/store/runtime.Open` to return a backend that satisfies
  the new persistence interface (Composite of SQLite + Markdown), so the
  Honshu example's call site in PR 8 reduces to a single
  `WithPersistence(...)` line.
- **Test signal:** runner tests pass with each backend; an integration test
  drives a small DAG end-to-end through the markdown backend and asserts
  the on-disk layout; orchestrator tests use the new option and confirm
  per-runner workspace allocation under the configured root.

### PR 5 — Executor migration to the new channels

- Rewrite `polish` / `execute` / `report` to:
  - Read parent handoffs from `inbox/` instead of receiving inline maps.
  - Emit a handoff (and optionally an exchange entry) per stage.
  - Snapshot memos through the workspace.
- Delete `internal/engine/executor_exchange.go` and the legacy
  `ExchangeDocument` symbols.
- Update orchestrator review / replan paths to read handoffs (not the old
  combined exchange document).
- **Test signal:** `go test ./internal/engine/...` passes; orchestrator
  tests assert the new envelope; old `ExchangeDocument` symbols are absent.

### PR 6 — Built-in prompt extraction

- Create `internal/engine/builtin/prompts/` tree above. Move the
  orchestrator's existing `SKILL.md` body into split files.
- Add the `prompts.Renderer`, embed via `//go:embed`, replace inline
  `polishPrompt` / `executionPrompt` / `reportPrompt` with template renders.
- Tests render each template and assert key invariants (e.g. memo discipline
  text appears, task description is interpolated).
- This PR can land in parallel with PR 5; if PR 5 lands first, PR 6 only
  swaps the prompt source. If PR 6 lands first, the executor consumes
  templates immediately.
- **Test signal:** template tests pass; no string literal prompt over ~10
  lines remains in `internal/engine/*.go`.

### PR 7 — Orchestrator coarse plan as handoff

- Remove the special `plan` exchange stage path. The planner emits a
  `plan` handoff routed through the same dispatcher, with the bootstrap
  `from = orchestrator` marker.
- Subscribers continue to see a planner-completed status; the underlying
  delivery is now handoff machinery.
- **Test signal:** `request_test.go` planner-flow tests pass with the new
  envelope.

### PR 8 — Honshu example cleanup

- Deleted `writePersistenceDebugArtifacts` and the local summary structs.
- Continued using a single `orchestrate.WithPersistence(...)` call from
  `runtimepkg.Open(...)`.
- Updated the Honshu example tests to verify persistence-managed workspace
  outputs (exchange entries, memo snapshots, and handoff routes) directly.
- **Current test signal:** Honshu tests assert workspace artifacts under the
  persistence workspace root and final deliverables under `Request.ArtifactsDir`.

### PR 9 — Documentation

- Refreshed this memo into a shipped-status record.
- Added cross-reference updates in `docs/stream-refactor-memo.md` to point to
  this exchange/memo/handoff rewrite record.

## Post-PR Verification (2026-05-09)

A full branch check was run against the design goals in this memo.

### Confirmed implemented

- Unified orchestrator persistence via `WithPersistence(...)` and runtime
  persistence backend wiring is in place.
- Runner workspace layout, path-rule allocation, memo snapshots, exchange
  publication, and handoff routing are in place.
- Honshu example now relies on persistence-managed workspace artifacts instead
  of ad-hoc JSON summary writers.

### Deviations from this memo's original hard requirements

1. **Legacy `ExchangeDocument` compatibility is still present.**
   - Legacy exchange symbols and parsers remain in `internal/engine/runner/`.
   - Executor stage paths still emit a legacy exchange markdown return value.
   - This diverges from the "No backwards compatibility" requirement in this
     memo.
2. **`Request.ArtifactsDir` still participates in runner trusted roots.**
   - `newRunnerFromPlan` still forwards `runnerpkg.WithTrustedResourceRoots(req.ArtifactsDir)`.
   - This diverges from the stricter target where `Request.ArtifactsDir` is
     only the caller-facing final-deliverable mirror destination.

These deviations are intentionally recorded here so follow-up cleanup can be
tracked without rewriting historical PR notes.

### Post-PR follow-up work to complete

1. **Prompt override public surface.**
   - The built-in prompt override hook no longer needs to stay internal.
   - Follow-up work should expose it as a public advanced API on
     orchestrator/runner configuration.
   - Documentation must label it advanced usage.
   - No dedicated example is required for the initial public surface.

## Open Questions / Deferred

1. **Visibility/redaction controls:** accepted as deferred. Privacy labels,
  redaction policy, and per-recipient visibility on exchange/handoff remain out
  of scope. The new types should not preclude adding a `visibility:`
  front-matter field later.
2. **SQLite/Postgres persistence backends:** moved to
  `docs/runner-persistence-rdb-backends-memo.md`. The follow-up should be a
  multi-PR plan where every PR has one clear target and enough tests to be
  completed inside that PR.
3. **Cross-runner exchange:** no active plan. Runner-local exchange remains the
  contract for now, while recording that sibling-runner whiteboard sharing may
  become useful later.

## Non-Goals For This Branch

- Do not preserve the legacy `dango.exchange` document kind or its parsers.
- Do not add adapter/wrapper layers between old and new types — call sites
  are migrated in the same PR that introduces the replacement.
- Do not merge stream persistence and exchange/handoff persistence into
  one storage row format; the unified interface routes them but their
  on-disk shapes stay distinct.
- Do not add a dedicated example for prompt template overrides; document the
  public surface as advanced usage instead.
