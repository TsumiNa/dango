# Deferred Refactor Tracker Memo

Last updated: 2026-05-29

This memo records deferred refactor topics discussed before PR B.
PR B is handled separately and should proceed now.

## PR A - Logging Integration Refactor (Completed)

Status: Completed. See
`docs/pr-a-logging-integration-refactor-plan.md` for the shared design
reference (goal, non-goals, API surface, wiring flow, format spec,
sample output, deferred items) and
`docs/pr-a-logging-integration-refactor-steps/` for the per-PR scope
files.

Delivered sub-PRs:

- PR A-1 (#100) — `internal/logging/handler.go` pretty `slog.Handler`
  emitting the preset single-line format
  `HH:MM:SS.mmm  LVL  pkg/file.go:NN  message  k=v ...`. Color is
  decided once at construction via a writer-bound `lipgloss.Renderer`
  (ANSI256 on TTY, Ascii otherwise); attribute handling resolves
  `LogValuer` per the slog contract and CR/LF in messages is escaped
  to keep the single-line invariant.
- PR A-2 (#101) — `internal/logging` API rewrite. `Config` is now
  `{Level slog.Level, Output io.Writer, AddSource bool}`; `New` is
  replaced by `NewLogger(cfg) *slog.Logger` which never returns nil
  and always carries `service=dango`. `OpenFileSink(path)` provides a
  caller-owned append-mode file writer. `Format`, `File`, `BindFlags`,
  and the `DANGO_LOG_*` env reads are removed — any flag/env mapping
  is the caller binary's responsibility.
- PR A-3 (#102) — engine wiring + caller migration.
  `WithOrchestratorLogger` is renamed to `WithLogger`; orchestrator,
  runner, and agent all default their logger field to
  `logging.NewLogger(logging.DefaultConfig())`, so an unconfigured
  service emits no log output. All `e.logger != nil` guards in
  `internal/engine/agent.go` are removed. `demo/orchestrate/main.go`
  and `examples/honshu_groundwater/main.go` build their loggers via
  `logging.NewLogger(logging.Config{...})` and install through
  `orchestrate.WithLogger`.

Resolved decisions (originally tracked as open questions):

- **`init()` vs explicit constructor wiring** — explicit. The library
  never installs anything during init; callers build a `*slog.Logger`
  via `logging.NewLogger(cfg)` and pass it through
  `orchestrate.WithLogger(...)`.
- **Logger config lifecycle (startup-only vs runtime mutation)** —
  startup-only at the orchestrator level. `Config` is consumed inside
  `NewLogger` and not stored; the resulting `*slog.Logger` is owned by
  the caller and threaded down to runners and agents. The caller owns
  the `Output` writer (including closing file sinks). `SetLogger(nil)`
  restores the discard default.
- **Interaction between `WithOrchestratorLogger(...)` and the new
  option** — `WithOrchestratorLogger` is removed outright (no shim)
  and replaced by `WithLogger(...)`. Go's package-scope naming
  constraint prevented the agent option from also becoming
  `WithLogger`, so `WithAgentLogger` keeps its name; the agent doc
  comment explains the constraint. Users wire one logger at the
  orchestrator and the orchestrator threads it everywhere.
- **Backward compatibility for examples/tests** — no shims, no aliases,
  no parallel APIs. All in-tree callers were updated in PR A-3 within
  one commit; per
  `.github/instructions/in-branch-api-compat.instructions.md`, the
  old names are deleted.

Follow-up (out of scope for PR A, tracked for future work):

- **CLI binary integration.** A future `cmd/dango` (or equivalent)
  binary owns the flag/env mapping that produces a `logging.Config`.
- **OpenTelemetry / structured-event bridge.** Existing
  `stream_events.jsonl` and OTLP code paths are untouched. A future
  PR may add a `slog.Handler` that fans out to OTLP.
- **Per-component log levels.** If a noisy package later needs a
  lower level than the root, the right answer is a thin
  `slog.Handler` wrapper, not a new `Config` field.
- **Log rotation.** `OpenFileSink` opens with `O_APPEND` and returns a
  plain file; callers needing rotation wrap the writer themselves.
- **Subtle behavior change to watch:** `logging.From(nil)` and
  therefore `logging.Component(nil, ...)` now go through
  `NewLogger(DefaultConfig())` and carry `service=dango`. The old
  `From(nil)` returned a plain `slog.NewTextHandler(io.Discard)` with
  no base attribute. No engine code asserts on the absence of
  `service`, but downstream fixtures matching log records by exact
  attribute count would observe one extra attribute.

## PR C - Builtin Tools Restructure (Completed)

Status: Completed. See `docs/builtin-tools-restructure-plan.md` for the
full historical plan and sub-PR breakdown.

Delivered sub-PRs:

- PR C-1 (#81) — bash redirection static safety check; rejects dynamic
  targets and absolute escapes while still accepting heredocs and
  workspace-bounded static targets.
- PR C-2 (#82) — default tool set restructure; the seven Go-implemented
  core tools plus `bash` and `grep` ship by default, and `list_dir` /
  `pwd` move behind an opt-in `BuiltinExtras` config field.
- PR C-3 (#83) — wrapper-tool trace investigation; the recorded run had
  three bash calls and no pipeline crossing the ≥5-occurrence threshold,
  so no wrappers were approved on trace evidence.
- PR C-4 / PR C-5 (#84) — bundled `pipeline_search_replace` and
  `file_excerpt` wrappers shipped together as Go-implemented
  workspace-bounded equivalents of `sed -i` and `grep -A N -B M`.
  PR C-3 closed the *trace-driven* wrapper track; these two wrappers
  were added as predeclared, narrowly scoped helpers rather than as
  outputs of that investigation.

Resolved decisions (originally tracked as open questions):

- **Minimal default builtin set vs optional extended set** — default set
  is `bash`, `read_file`, `write_file`, `edit_file`, `delete_file`,
  `move_file`, `grep`, `pipeline_search_replace`, `file_excerpt`.
  `list_dir` and `pwd` are reached through `BuiltinExtras`; skills that
  do not opt in fall back to `ls` / `pwd` via bash.
- **Safety boundaries and allowlist policy after introducing wrappers** —
  every Go-implemented tool routes paths through `workspace.ResolvePath`,
  and bash redirections are statically validated. Argument-level write
  targets (for example `tee /etc/foo`) remain explicitly out of scope and
  are documented as such in the bash tool description and
  `system_instructions.md`.
- **How to measure token/correctness gains before landing broad changes** —
  the PR C-3 methodology stands: extract `llm.tool_call.started` events
  from `artifacts/debug/stream_events.jsonl`, tally bash command heads
  and multi-stage pipelines, and require a single pattern to cross the
  ≥5-occurrence threshold in one honshu run before a wrapper PR is
  scheduled. Future wrapper proposals must rerun that analysis.

Follow-up: see `docs/builtin-tools-coverage-memo.md` for an out-of-scope
security and research/autonomous-experiment coverage review. That memo
records gaps to consider before adopting research-oriented skills; it is
not a commitment to implement anything in this track.

## Near-Term Plan (Completed)

Status: Completed for the items in scope; the structural hardening track
(post-alpha § 2.3 mitigations) and `12b` (interactive approver) remain
deferred per their original gating conditions. See
`docs/near_term_plan/` for the per-subtask files.

Delivered sub-PRs (numeric prefix = `docs/near_term_plan/` file):

- `20` (#87) — `git` added to the default bash allowlist for repo
  inspection workflows.
- `21` (#88) — `artifact_catalog` Go builtin reading
  `downstream/artifacts/` plus handoff front-matter.
- `22` (#89) — `structured_preview` Go builtin for top-level keys +
  value-type counts on JSON/YAML.
- `30` (#94) — opt-in bash URL allowlist for `curl` / `wget`.
- `11` (#92) — extras-enum + tool-config reshape (`ToolSetConfig`).
- `12a` (#93) — execution policy data model with `passby`/`off`
  enforcement and bash command-pattern classification. `12b` remains
  deferred until an interactive approver exists.
- `40` (#96) — skill alias and conflict resolution at mount time.
- `50` (#97) — MCP support: client wrapper, namespaced tool adapter,
  orchestrator global + per-skill visibility, call-only stream event.
- `60` and `90` (this PR) — audit-category tag on tool-call events with
  `docs/tool-call-audit-schema.md` as the stability contract, the
  `tools/analyze-tool-traces` utility (`just analyze-traces …`) for
  per-run reports, and the closeout integration check that exercises
  the delivered subtasks together.

Resolved decisions:

- **MCP packaging.** The `llm.MCPServer` wrapper proposed in `50` was
  dropped in favour of exposing `mcpclient.Server` directly (see PR #97
  refactor commit). Callers hold the raw handle; the `llm` package
  provides only the `MCPTools(*mcpclient.Server) []llm.Tool` adapter.
- **Audit pipeline filter.** Consumers should filter on
  `metadata.category == "audit"` (the stable tag) rather than on an
  event-type allowlist. The trace analyzer in
  `tools/analyze-tool-traces` deliberately reads tool-call events
  regardless of tag so legacy traces from before this PR landed are
  still analyzable.

Follow-up:

- Post-alpha § 2.3 structural mitigations remain open.
- `12b` interactive approver waits for the app/cmd cycle.
- The honshu observation noted in `docs/near_term_plan/90` is
  exercised via the cross-subtask integration test; subjective UX
  observations are recorded in the relevant subtask files as they
  arise.

## PR D - API Cleanup and Compatibility Review (Deferred)

Status: Deferred for dedicated discussion.

Current assessment:

- Multiple APIs flagged by deadcode need explicit keep/remove decisions.
- Some symbols may be intentional extension points despite low in-repo usage.

Decision criteria for later review:

- Keep if needed as near-term public/internal extension points.
- Remove if only test-facing legacy artifacts with no production path.
- Avoid compatibility layers unless explicitly required during that refactor.

## PR E - Streamrender Extraction and Terminal UI Refactor (Deferred)

Status: Deferred to independent package refactor after the immediate exchange/handoff fixes.

Current intent:

- Move `internal/streamrender` out of `internal` and make it a more independent rendering package.
- Use it as the foundation for future `cmd` terminal UI work.
- Separate terminal/UI rendering concerns from durable runtime message storage.
- Avoid treating renderer-captured markdown snippets as canonical exchange files.

Immediate bug context:

- The Honshu example configured `streamrender.ExchangeDir` as `artifacts/exchanges`, creating a second exchange-like directory outside runner persistence.
- `streamrender` currently writes any channel-looking markdown output as `exchange-<sequence>.md`; this captured an orchestrator planning handoff as a misleading `exchange-*` file.
- The current PR should only make a minimal fix: stop creating the separate outer exchanges directory and stop labeling non-exchange channel markdown as canonical exchange storage.

Open questions to resolve later:

- Public package name and API shape once `streamrender` leaves `internal`.
- Whether renderer output should write files at all, or only link to canonical runner persistence artifacts.
- How future terminal UI commands should discover request, runner, exchange, handoff, memo, and debug event paths.
- How much renderer state belongs in stream events versus external UI/session state.

## Immediate Action

- Finish the current exchange/handoff bug-fix PR with only the minimal
  `streamrender` containment fix; defer full renderer extraction and terminal
  UI architecture to PR E.
