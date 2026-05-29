# PR D — API Cleanup and Compatibility Review (Implementation Plan)

Status: Drafted 2026-05-30. Tracked from
`docs/deferred-refactor-tracker-memo.md` "PR D".

This plan replaces the placeholder "current assessment / decision criteria"
block in the tracker memo with a concrete deletion list and a per-PR
delivery order. When work starts, this document is the source of truth;
the memo should be updated to point here and to record sub-PR completion
in the same style as PR A.

The plan covers every cleanup target large enough to merit a dedicated
PR. Trivial follow-ups discovered during execution may be folded into the
nearest matching sub-PR without amending this document.

## 1. Goal

Produce a clean baseline for the next phase of development. The current
tree carries the residue of three concurrent refactor tracks (logging,
builtin tools, near-term plan): test-only public APIs, wrapper layers
that no longer hide anything, customization hooks nobody wires, and
re-export façades that exist only because internal packages were once
private. Remove or merge all of it so a new contributor sees one obvious
way to do each thing.

Concretely, after PR D lands:

- Every public symbol either has a real production caller or is part of
  a documented test-fixture surface.
- No `internal/internal` nesting; first-order subpackages live one level
  below their parent.
- No file under `internal/` or `cmd/` exists solely to re-export, alias,
  or thinly wrap another package's symbol.
- The top three oversized files (`conversation.go`, `renderer.go`,
  `runner.go`) are split along the seams a reader would expect.
- Cobra-scaffold leftovers and empty package directories are gone from
  `cmd/` and `internal/`.

## 2. Non-goals

- No new features. Every PR is removal, rename, move, or split.
- No backward-compatibility shims, aliases, or deprecation comments.
  Callers update in the same commit as the rename or removal. This
  matches `.github/instructions/in-branch-api-compat.instructions.md`.
- No tightening or loosening of access control beyond what the cleanup
  itself implies (e.g., promoting a test-only seam to unexported is
  fine; introducing a new public API is out of scope).
- No behavior changes. If a test fails because the production behavior
  shifted, that PR is wrong and should be split.
- No `internal/streamrender` extraction or terminal UI refactor — that
  stays in PR E.
- No `internal/store` schema changes — runtime contracts and migrations
  are untouched.

## 3. Guiding principles

- **Delete first, refactor second.** Many "wrappers" disappear once the
  symbol they wrap is shown to be unused. Run the deletion PRs (D-1,
  D-2, D-3) before the structural ones (D-4 onward) so the structural
  PRs operate on smaller files.
- **Test-only public symbols collapse to unexported.** If a function or
  method is reached only from `_test.go` files in the same package, it
  is private. Cross-package test seams keep their export but are
  documented as such.
- **One file, one cohesive concept.** Files under ~50 production lines
  merge into the file that owns their concept unless they are an
  acknowledged package entry point (`doc.go`, generated code, embed
  boundary).
- **Types live next to behavior.** Re-export "types.go" files
  (`internal/engine/types.go`) are inlined into the file that produces
  them. Locally-scoped types stay with the function/struct that
  consumes them; only genuinely cross-cutting domain types live in a
  `types.go` companion.
- **Keep PRs small enough to bisect.** Each PR ends with a green
  `go build ./...` and `go test ./...`. Cross-cutting moves
  (`internal/llm/internal` flatten, `conversation.go` split) get their
  own PR rather than riding on a deletion PR.

## 4. Inventory

This section enumerates the cleanup targets discovered during the
survey. Each item carries the file:line where it lives, a one-line
justification, and the PR that will deliver the change. Items not
listed are out of scope; if a code path is touched incidentally, its
removal moves into the nearest in-scope PR.

### 4.1 Unreachable symbols (deadcode tool + manual verification)

Verified via `deadcode ./...` on 2026-05-30 plus a manual grep across
`internal/`, `cmd/`, `demo/`, `examples/`, and `tools/`.

| Symbol | Location | External callers | Disposition |
| --- | --- | --- | --- |
| `Agent.formatParentHandoffs` | `internal/engine/agent_prompt.go:270` | `agent_test.go` only | Delete; test re-points to `formatParentOutputs` |
| `readParentHandoffsFromUpstream` | `internal/engine/agent_prompt.go:281` | `formatParentHandoffs` only | Delete with above |
| `WithRunnerPathRule` | `internal/engine/orchestrator.go:96` | none | Delete; inline `DefaultPathRule` everywhere |
| `LooksLikeChannelMarkdown` | `internal/engine/runner/channel_markdown.go:5` | none (file is one wrapper line) | Delete file; callers already use `isRunnerChannelMarkdown` |
| `WithTrustedResourceRoots` | `internal/engine/runner/options.go:84` | none | Delete option + `trustedResourceRoots` field + the always-empty loops in `workspace_binding.go` |
| `IsTerminal` | `internal/engine/runner/view.go:54` | none (`IsRemovable` is the one that is used) | Delete |
| `persistence.None` + `noneBackend` | `internal/engine/runner/persistence/none.go` | conformance test only | Move into `conformance_test.go` as an unexported fixture, delete the production file |
| `WithReplayFrom`, `WithOverflowPolicy` | `internal/engine/stream/subscription.go:40,76` | tests only, but part of documented Subscribe API | Keep; document as test-supported public surface |
| `detectProvider` | `internal/llm/client.go:259` | `client_test.go` only | Inline into `detectProviderWithLookup`; tests use the latter |
| `ApproverFunc.Approve` | `internal/llm/conversation.go:208` | tests only, but is the canonical Approver adapter | Keep; this is the documented adapter pattern |
| `SkillCapability` (façade) | `internal/llm/tool_config.go:89` | engine runner | Folds into PR D-6 when façade collapses |
| `ResolveWorkspacePath` | `internal/llm/workspace.go:170` | `workspace_test.go` only | Delete; tests call unexported `resolveWorkspacePath` (already in scope) |
| `newBash`, `withAllowlist`, `withoutAllowlist` | `internal/llm/internal/builtin/{bash,builtin}.go` | builtin tests only | Inline into tests or keep unexported; revisit when flattening (PR D-6) |
| `OpenFileSink` | `internal/logging/logging.go:84` | none | Delete; callers own file sinks per PR A-2's mandate |
| `From` | `internal/logging/logging.go:103` | none | Delete; obsolete safety helper from pre-PR-A logging |
| `Component` | `internal/logging/logging.go:115` | none | Delete with `From` |
| `Start` | `internal/mcpclient/mcpclient.go:96` | none (`StartWithTransport` covers tests) | Collapse to single `Start(ctx, spec, transport)` — see PR D-2 |
| `listAllTools`, `coerceSchema`, `objectSchema` | `internal/mcpclient/mcpclient.go:143,174,192` | internal callers only | Keep (deadcode flag is incorrect because of conditional reachability through `Start`); re-verify after `Start` collapse |
| `runtime.DefaultConfig` | `internal/store/runtime/runtime.go:40` | `runtime_test.go` only | Delete; `Config{}` is already the documented zero value |
| `Renderer.RenderSubscription` | `internal/streamrender/renderer.go:188` | tests only; example uses `RenderSubscriptionObserved` | Delete; tests pass `nil` observer through the Observed entry point (or merge into a single `Render`) |
| `Renderer.FormatEvent` | `internal/streamrender/renderer.go:259` | tests only | Demote to package-private `formatEvent`; tests live in the same package |

### 4.2 Façade and wrapper layers

| Target | Why it can go |
| --- | --- |
| `internal/llm/tool_config.go` | Pure re-export of `internal/llm/internal/builtin` + `internal/llm/internal/toolpolicy`. Both inner packages are imported only from inside `internal/llm`, so the double-internal nesting and the façade exist to hide nothing. Collapse the file after flattening (PR D-6). |
| `internal/llm/internal/builtin/tool_config.go` | Mirrors of the above inside the inner package. Re-evaluate after flatten. |
| `internal/engine/types.go` | 15-line file re-exporting three runner types (`CoarsePlan`, `CoarsePlanNode`, `PlanReview`). Inline into the engine file that produces each value or drop and update callers to import `runnerpkg` directly. |
| `mcpclient.Start` vs `mcpclient.StartWithTransport` | Two public entry points distinguished only by an optional transport. Collapse to `Start(ctx, spec ServerSpec, transport mcp.Transport)` where `nil` means "spawn the command". `startWithTransport` then disappears. |
| `streamrender.Renderer.RenderSubscription` | One-line wrapper around `RenderSubscriptionObserved` that always passes `nil`. Tests are inside the package, so they can call the Observed entry point directly. |
| `internal/llm/workspace.go:ResolveWorkspacePath` | Re-export of unexported `resolveWorkspacePath`. The docstring promises a "custom tool" extension point that no public Skill API consumes. |

### 4.3 Unused customization plumbing

These are wired through the codebase but never adjusted by any caller,
so the plumbing itself is dead weight even though it compiles.

- `Orchestrator.runnerPathRule` field + `WithRunnerPathRule` +
  `runner.WithRootPathRule` + `Runner.rootPathRule` field. Every runner
  is constructed with `persistence.DefaultPathRule`. Collapse to a
  package constant or call the helper inline; the option-and-field
  chain goes away.
- `Runner.trustedResourceRoots` + `WithTrustedResourceRoots` + the two
  loops in `workspace_binding.go`. The slice is always empty in
  production and tests; the loops contribute nothing.
- `runner.IsTerminal`. Never read.
- `persistence.None` backend (covered above) — kept only for one
  conformance-test fixture.

### 4.4 Empty / scaffold directories

- `internal/prompts/` — single `doc.go`, no Go files, no importers.
  Delete the directory.
- `internal/engine/builtin/` — only contains the `instructions/`
  subpackage plus a `SKILL.md`. Move `instructions/` to
  `internal/engine/instructions/` and delete the empty parent. Update
  the embed reference in `agent_prompt.go`.
- `cmd/add.go`, `cmd/run.go` — Cobra scaffold leftovers that print
  `"add called"` / `"run called"`. Delete; the root command registers
  only `serve` and `version` (and any future real subcommand).

### 4.5 File organization

Reorganization targets, grouped by the PR that will deliver them.

**Engine top-level (PR D-4):**

- `internal/engine/types.go` — inline three type aliases into the file
  that surfaces each (`orchestrator.go` or `request.go`) or drop them
  and let callers import `runnerpkg`.
- `internal/engine/orchestrator_skill.go` (67 lines) and
  `orchestrator_tools.go` (80 lines) — merge into `orchestrator.go` or
  into a single `orchestrator_setup.go`. Both are short helpers
  exclusively used by orchestrator initialization.
- `internal/engine/agent_stage.go` (186) and `agent_stage_output.go`
  (111) — merge into `agent_stage.go`. The split tracks an
  implementation detail rather than a distinct package responsibility.
- `internal/engine/agent_prompt.go` — after removing
  `formatParentHandoffs` / `readParentHandoffsFromUpstream`, the file
  drops from 324 to ~250 lines. No further split.
- `internal/engine/doc.go` (7 lines) — keep, package entry point.

**Runner package (PR D-5):**

- `internal/engine/runner/channel_markdown.go` — deleted in PR D-2.
- `internal/engine/runner/types.go` (179 lines) — split:
  - Move `Node`, `executionResult`, `PlanNodeBuilder` to `runner.go`
    (these are runner internals).
  - Move `PlanReview` to `plan.go` (alongside `CoarsePlan`).
  - Move `ErrRunnerAlreadyStarted`, `ErrInvalidPhase`, `ErrPlanRequired`
    next to the methods that return them, or to a small `errors.go`.
  - Keep `Agent` interface, `EventType`+constants, `RunnerEvent`,
    `RunnerSnapshot`, `RunnerStatus`, `RunnerState`, `RunnerPhase`,
    `SkillSummary` in `types.go` as genuinely cross-cutting.
- `internal/engine/runner/plan.go` (36) + `plan_parse.go` (117) —
  merge into `plan.go`.
- `internal/engine/runner/view.go` — after removing `IsTerminal`, the
  file holds `RunnerView` + snapshot helpers + `IsRemovable`. Keep, but
  consider moving the snapshot helpers next to `Runner.GetSnapshot`.
- `internal/engine/runner/persistence/path_rule.go` (10 lines) and
  `none.go` (after fixture move) — merge `path_rule.go` into
  `backend.go`.

**Stream package (PR D-5):**

- `internal/engine/stream/channel_messages.go` (21) +
  `channel_payloads.go` (51) — merge into `channel.go`. Both define
  channel-event helpers used by the same tests.

**LLM package — flatten (PR D-6):**

- Move `internal/llm/internal/builtin/` → `internal/llm/builtin/`.
- Move `internal/llm/internal/toolpolicy/` → `internal/llm/toolpolicy/`.
- Delete `internal/llm/internal/` directory.
- Re-evaluate `internal/llm/tool_config.go` afterward: most aliases can
  collapse into a one-line `import builtin` chain in callers; only the
  symbols that callers reach today through `llm.X` need to stay
  re-exported, and even those should be re-considered against the
  in-branch-API-compat rule.

**LLM package — split (PR D-7):**

- `internal/llm/conversation.go` (1061 lines) — split along the seams
  the existing top-of-file already telegraphs:
  - `turn.go` — `Role`, `Tier`, `Turn`, `TokenUsage`, `RoleUsage`,
    related JSON helpers.
  - `summarizer.go` — `Summarizer` interface, `SummarizerFunc`,
    `DefaultSummarizerFunc`, `summarizeTurn`.
  - `tool_call.go` — `ToolSpec`, `ToolCall`, `ToolCallPayload` if
    they're not already next to dispatch logic.
  - `conversation.go` keeps the `Conversation` struct, config types
    (`ConversationConfig`, `AutoShrinkConfig`), and core methods.
  - `Approver` / `ApproverFunc` move next to dispatch (`conversation_run.go`).

**Streamrender package (PR D-8):**

- `internal/streamrender/renderer.go` (1198 lines) — split:
  - `renderer.go` — `Renderer` struct, `New`, `RenderSubscriptionObserved`.
  - `event_format.go` — per-event-type formatters (the long tail of
    `switch event.Type` arms).
  - `style.go` — lipgloss/tty setup, color decisions, and the
    construction-time renderer wiring.
- Demote `FormatEvent` to package-private `formatEvent`; tests are in
  the same package.

**Store package (PR D-9):**

- `internal/store/event_log.go` (24) + `event_log_json.go` (172) —
  merge into `event_log.go`. The split currently is "interface here,
  JSON encoding there" with no third implementation.
- `internal/store/cursor.go` (33) + `cursor_json.go` (128) — merge
  similarly.
- `internal/store/striped_locks.go` (35) — keep, it's a self-contained
  primitive.

## 5. Per-PR delivery

The PRs below are ordered so each rests on a green tree from the
previous one. Each PR ends with `go build ./...`, `go vet ./...`,
`go test ./...`, and `~/go/bin/deadcode ./...` showing strictly fewer
findings than before.

Branch naming follows the established
`refactor/pr-d-<slug>` pattern (see
`.github/instructions/branch-and-pr-workflow.instructions.md`).

### PR D-1 — Dead-code purge (mechanical)

**Scope.** Symbols and files that have zero callers anywhere (production
or test) or whose only callers are themselves.

**Files touched.**

- `internal/logging/logging.go` — remove `OpenFileSink`, `From`,
  `Component`.
- `internal/logging/doc.go` — drop paragraphs that documented the
  removed helpers.
- `internal/logging/logging_test.go` — remove the corresponding tests.
- `internal/engine/runner/view.go` — remove `IsTerminal`.
- `internal/engine/runner/options.go` — remove `WithTrustedResourceRoots`.
- `internal/engine/runner/runner.go` — remove `trustedResourceRoots`
  field.
- `internal/engine/runner/workspace_binding.go` — remove the two loops
  that iterated over the always-empty slice.
- `internal/engine/agent_prompt.go` — remove `formatParentHandoffs` and
  `readParentHandoffsFromUpstream`.
- `internal/engine/agent_test.go` — re-point the one test from
  `formatParentHandoffs` to `formatParentOutputs` (matching the live
  code path in `agent_stage.go`).
- `internal/llm/workspace.go` — remove `ResolveWorkspacePath`.
- `internal/llm/workspace_test.go` — switch tests to
  `resolveWorkspacePath`.
- `internal/store/runtime/runtime.go` — remove `DefaultConfig`.
- `internal/store/runtime/runtime_test.go` — replace `DefaultConfig()`
  with `Config{}`.
- `internal/llm/client.go` — inline `detectProvider` into
  `detectProviderWithLookup`.
- `internal/llm/client_test.go` — re-point tests if needed.
- `cmd/add.go`, `cmd/run.go` — delete files.
- `internal/prompts/` — delete directory.

**Tests.** All existing test files in the touched packages must pass
unmodified except for the explicit re-points above. No new tests.

**Acceptance.** `deadcode ./...` no longer reports the symbols listed
in §4.1 that are scheduled for this PR. `go test ./...` passes.

### PR D-2 — Wrapper collapse

**Scope.** Single-line wrappers and parallel entry points that exist
because the underlying function once had a different signature.

**Files touched.**

- `internal/engine/runner/channel_markdown.go` — delete file. Verify no
  caller references `LooksLikeChannelMarkdown`.
- `internal/mcpclient/mcpclient.go` — collapse `Start` and
  `StartWithTransport` into `func Start(ctx context.Context, spec
  ServerSpec, transport mcp.Transport) (*Server, error)`. `nil`
  transport falls through to `exec.CommandContext`. Delete
  `startWithTransport`.
- `internal/mcpclient/mcpclient_test.go`, `internal/llm/mcp_test.go`,
  `internal/engine/orchestrator_mcp_test.go`,
  `internal/engine/integration_neartermplan_test.go` — update the three
  call sites that pass a transport to use the new signature.
- `internal/streamrender/renderer.go` — remove `RenderSubscription`;
  demote `FormatEvent` to `formatEvent`. Tests in
  `renderer_test.go` switch to `RenderSubscriptionObserved(..., nil)`
  and `formatEvent`.

**Tests.** No new tests; existing ones are re-pointed.

**Acceptance.** `deadcode ./...` reports no entry for the symbols
removed in this PR. Examples (`examples/honshu_groundwater`) still
build and run their golden tests.

### PR D-3 — Unused customization plumbing

**Scope.** Options and their backing fields that customize behavior
nobody customizes.

**Files touched.**

- `internal/engine/orchestrator.go` — remove `WithRunnerPathRule`, drop
  `runnerPathRule` field, inline `persistencepkg.DefaultPathRule` into
  the runner-construction site in `request.go`.
- `internal/engine/request.go` — pass `persistencepkg.DefaultPathRule`
  directly to `runnerpkg.WithRootPathRule`, *and* remove the runner
  option in the same PR if no test requires per-runner override (verify
  by grepping `WithRootPathRule` outside engine).
- `internal/engine/runner/options.go` + `runner.go` — if the previous
  step proves `WithRootPathRule` has no diverse callers, delete the
  option and the `rootPathRule` field; runners call
  `persistencepkg.DefaultPathRule` directly.
- `internal/engine/runner/persistence/none.go` — delete file; move the
  no-op backend into `conformance_test.go` as an unexported
  `noopBackend` struct.

**Tests.** `internal/engine/runner/persistence/conformance_test.go`
keeps the `none-noop` case, now backed by a local struct. Runner and
orchestrator tests verify default workspace paths unchanged.

**Acceptance.** Workspace path layout is bit-identical before and after
(snapshot the conformance test's wantRunnerRoot). `deadcode ./...`
reports nothing in this area.

### PR D-4 — Engine package file reorganization

**Scope.** Merge tiny/awkward engine files and inline the
`internal/engine/types.go` re-exports.

**Files touched.**

- `internal/engine/types.go` — delete; inline the three aliases
  (`CoarsePlan`, `CoarsePlanNode`, `PlanReview`) at the call sites that
  surface them, or update those call sites to use `runnerpkg.X`.
  Document the choice in the PR description.
- `internal/engine/orchestrator_skill.go` + `orchestrator_tools.go` —
  merge into `orchestrator.go` (preferred if `orchestrator.go` is still
  manageable after PR D-3, which already removed lines) or into a new
  `orchestrator_setup.go`. Pick whichever leaves the most cohesive
  file.
- `internal/engine/agent_stage.go` + `agent_stage_output.go` — merge
  into `agent_stage.go`.

**Tests.** Existing engine tests pass without modification (file moves
don't change behavior). Add no new tests.

**Acceptance.** `git diff --stat` shows only file moves and merges; no
exported symbol changes name or signature.

### PR D-5 — Runner & stream package file reorganization

**Scope.** Resolve the runner `types.go` mixed-placement issue and
merge the small stream channel-event files.

**Files touched.**

- `internal/engine/runner/types.go` — move `Node`, `executionResult`,
  `PlanNodeBuilder` to `runner.go`; move `PlanReview` to `plan.go`;
  consider an `errors.go` for the three sentinel errors.
- `internal/engine/runner/plan.go` + `plan_parse.go` — merge into
  `plan.go`.
- `internal/engine/runner/persistence/path_rule.go` — merge into
  `backend.go`.
- `internal/engine/stream/channel_messages.go` +
  `channel_payloads.go` — merge into `channel.go`.

**Tests.** Unchanged.

**Acceptance.** `runner/types.go` is shorter and contains only
cross-cutting domain types. No package-public symbol is renamed.

### PR D-6 — Flatten `internal/llm/internal` and collapse the façade

**Scope.** Largest blast radius PR. Touches every importer of
`internal/llm/internal/builtin` and `internal/llm/internal/toolpolicy`.

**Files touched.**

- Move `internal/llm/internal/builtin/` → `internal/llm/builtin/`.
- Move `internal/llm/internal/toolpolicy/` → `internal/llm/toolpolicy/`.
- Delete the now-empty `internal/llm/internal/` directory.
- Update import paths in:
  - `internal/llm/builtin_tools.go`
  - `internal/llm/skill.go`
  - `internal/llm/tool_config.go`
  - `internal/llm/tool_policy.go`
  - `internal/llm/conversation_run.go`
  - `internal/llm/conversation_run_test.go`
  - `internal/llm/builtin/bash.go` (internal back-reference to
    `toolpolicy`)
  - `internal/llm/builtin/tool_config.go`
  - `internal/llm/builtin/bash_test.go`
- Re-evaluate `internal/llm/tool_config.go`:
  - Replace each `type X = builtin.X` with the direct import where
    callers use it.
  - Replace each `func X(...) Y { return builtin.X(...) }` with a
    direct call in the one or two caller files (verify count via
    grep).
  - If every alias has exactly one caller, delete `tool_config.go`
    outright. If a few aliases have multiple callers, keep only those
    and rename the file to something more descriptive (e.g.,
    `tool_policy_api.go`) if the surface is small enough to justify.

**Tests.** All llm tests pass without modification. Run
`go vet ./...` to catch any missed imports.

**Acceptance.** `internal/llm/internal/` is gone. `internal/llm/`
imports use the flatter path. No public symbol renamed; only import
paths change.

**Risk.** This PR touches many files. Land it on its own branch with
nothing else stacked. Reviewers verify by `git grep
"internal/llm/internal"` returning empty.

### PR D-7 — Split `internal/llm/conversation.go`

**Scope.** Pure file split. No symbol renamed, no behavior changed.

**Files touched.** Inside `internal/llm/`:

- New `turn.go` — `Role`, `Tier`, `Turn`, `TokenUsage`, `RoleUsage`,
  related JSON helpers.
- New `summarizer.go` — `Summarizer`, `SummarizerFunc`,
  `DefaultSummarizerFunc`, `summarizeTurn`.
- `conversation.go` — keeps `Conversation`, `ConversationConfig`,
  `AutoShrinkConfig`, their defaults, and core lifecycle methods.
- `conversation_run.go` (existing) — receives `Approver`,
  `ApproverFunc`, and `ApproverFunc.Approve` (Approver lives next to
  dispatch).
- `ToolSpec`, `ToolCall`, `ToolCallPayload` — move into the file where
  the type is most heavily used; verify with `grep -c` before deciding.

**Tests.** Existing `conversation_*_test.go` files keep their names and
locations; no test code moves in this PR.

**Acceptance.** `conversation.go` drops below ~600 lines. No
package-public symbol moves between packages or changes signature.

### PR D-8 — Split `internal/streamrender/renderer.go`

**Scope.** Pure file split of the 1198-line renderer.

**Files touched.** Inside `internal/streamrender/`:

- New `event_format.go` — the per-event-type formatter switch arms.
- New `style.go` — lipgloss/tty/renderer wiring.
- `renderer.go` — keeps `Renderer` struct, `New`,
  `RenderSubscriptionObserved`, and the top-level event loop.

**Tests.** `renderer_test.go` already exercises the full surface; keep
intact. If the test file itself exceeds ~500 lines, split along the
same seams (`event_format_test.go`, `style_test.go`) but treat that
as in-scope only if it falls out cleanly.

**Acceptance.** Each new file is independently readable. The example
binary (`examples/honshu_groundwater`) still builds and renders
identically (snapshot the example's output before and after).

### PR D-9 — Store package merges

**Scope.** Merge the small interface/JSON pairs in `internal/store/`.

**Files touched.**

- `internal/store/event_log.go` + `event_log_json.go` → `event_log.go`.
- `internal/store/cursor.go` + `cursor_json.go` → `cursor.go`.

**Tests.** Existing `*_test.go` files (event_log_json_test.go, etc.)
are renamed alongside their source if they exist, or stay where they
are if they already match the merged file's basename.

**Acceptance.** Same external API. `go test ./internal/store/...`
passes.

### PR D-10 — Empty/scaffold cleanup and memo update

**Scope.** Last sweep + record the outcome in the tracker memo.

**Files touched.**

- Move `internal/engine/builtin/instructions/` →
  `internal/engine/instructions/`. Update the embed import in
  `internal/engine/agent_prompt.go`. Delete the empty
  `internal/engine/builtin/` directory (and any orphaned `SKILL.md`
  inside it).
- Trim any remaining `doc.go` files whose body still references
  removed APIs (especially `internal/logging/doc.go`, already touched
  in PR D-1).
- Update `docs/deferred-refactor-tracker-memo.md`:
  - Change the PR D section heading from "Deferred" to "Completed".
  - List PR D-1 through D-10 with their PR numbers (mirroring PR A's
    "Delivered sub-PRs" block).
  - Move "Decision criteria for later review" into a "Resolved
    decisions" block.

**Tests.** No new tests; one import path change is verified by
existing instruction tests in
`internal/engine/builtin/instructions/instructions_test.go` (renamed
with the move).

**Acceptance.** `find internal cmd -type d -empty` returns nothing.
`deadcode ./...` output matches the curated baseline (a small set of
intentionally exported test-supporting helpers documented in §6).

## 6. Acceptance baseline

After PR D-10 lands, the curated `deadcode ./...` baseline contains
only:

- `WithReplayFrom`, `WithOverflowPolicy`, `WithSubscriberBuffer` and
  related stream subscribe options — kept as documented public API
  even though only tests exercise them today.
- `ApproverFunc.Approve` — the documented adapter method for the
  Approver interface.
- `mcpclient` schema helpers if the deadcode tool still flags them
  conditionally; they are reachable in production through `Start`.

Any other deadcode finding either was missed during PR D and should be
folded into a follow-up PR, or represents an extension point that needs
an explicit "Keep" decision recorded here.

## 7. Risk notes and rollout

- **PR D-6 is the highest-risk PR** because it moves package import
  paths. Land it alone, do not stack PR D-7+ on it until merged. After
  merging, run the full `examples/honshu_groundwater` golden test to
  confirm no runtime behavior shifted.
- **PR D-3 removes options that examples might pass.** Before
  publishing, grep `examples/`, `demo/`, and `tools/` for
  `WithRunnerPathRule` and `WithTrustedResourceRoots` (they are not
  used as of 2026-05-30, but re-verify at PR time).
- **PR D-7 and PR D-8 are large diffs but mechanical.** They are split
  out of PR D-4 / PR D-5 so reviewers do not have to reason about both
  reorganization and merge in the same review.
- **No coordination with downstream consumers is required.** The dango
  module has no external Go importers at this point; everything we
  touch lives inside this repo. If that changes before PR D starts
  shipping, the in-branch-API-compat instructions still apply: prefer
  updating call sites over adding shims.

## 8. Out of scope (explicit deferrals)

- **PR E (streamrender extraction and terminal UI refactor)** stays
  deferred. PR D-8 only splits the file; it does not move the package
  out of `internal/`.
- **Post-alpha § 2.3 structural mitigations** and `12b` interactive
  approver — both remain gated as recorded in the tracker memo's
  "Near-Term Plan" section.
- **Renaming `Orchestrator` / `Runner` / `Agent`** — name-level
  refactors are explicitly outside PR D. If a name no longer fits, log
  it in a follow-up memo.
- **OpenTelemetry / structured-event bridge** — already deferred under
  PR A; PR D does not touch it.
- **`internal/store` schema changes / migration order** — out of scope.
  Only the in-package file merges in PR D-9 are touched.
