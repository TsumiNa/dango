# PR C — Builtin Tools Restructure Plan

Status: planned, split across multiple PRs.
Source memo entry: `docs/deferred-refactor-tracker-memo.md` § "PR C - Builtin Tools Restructure".

This document is the canonical implementation plan for the builtin tool
restructure. It is written so a coding agent can pick up any single PR
below and complete it without rereading the conversation that produced
this plan.

## Goals

- Lean the default builtin tool set around `bash` plus a few high-value
  Go-implemented helpers, deferring rarely-used helpers to an opt-in
  extras list.
- Keep destructive or workspace-boundary-sensitive operations
  (`write_file`, `edit_file`, `delete_file`, `move_file`) in Go so they
  always go through `workspace.ResolvePath`.
- Add static safety checks for `bash` redirection targets so we can
  responsibly remove redundant Go wrappers without losing workspace
  containment.
- Investigate, then (only if justified by real traces) add a small
  number of wrapper tools for repeated `cat | grep | sed` style
  pipelines, on the same `ResolvePath`-bounded contract.

## Non-goals

- Do not change the orchestrator, runner, exchange, or handoff layers.
- Do not redesign the bash allowlist contents (add/remove allowed
  binaries). Keep `defaultAllowlist` as-is unless a sub-PR explicitly
  calls for a change.
- Do not add cross-branch compatibility shims for in-progress builtin
  APIs (see `.github/instructions/in-branch-api-compat.instructions.md`).

## Confirmed decisions

These were settled before splitting into PRs. Follow them in every
sub-PR below.

1. **Redirection safety = "relatively strict, not maximally strict."**
   - Strict for execution-style redirections: dynamic targets
     (`> $VAR`, `> $(...)`), absolute paths outside the resolved
     workspace roots, and unresolved relative escapes must be rejected.
   - Lenient for "write a new doc" patterns: static relative paths and
     static absolute paths that resolve inside the workspace roots are
     accepted. Heredocs (`<<TAG`, `<<-TAG`, `<<<word`) are accepted
     because they do not target a host file.
2. **`list_dir` and `pwd` move to an opt-in extras list.** Default
   builtin set is `bash`, `read_file`, `write_file`, `edit_file`,
   `delete_file`, `move_file`, `grep`. Skills that still want
   `list_dir` / `pwd` request them via a new `BuiltinExtras` config
   field.
3. **Multi-PR delivery, one verifiable goal per PR.** Wrapper tools, if
   any, ship in dedicated PRs after a trace-driven justification step.
4. **In-branch API compatibility rule applies.** Update call sites
   directly. Do not add deprecated aliases. Honor
   `.github/instructions/in-branch-api-compat.instructions.md`.

## Open follow-ups (not committed)

- Should the `defaultAllowlist` be trimmed for token/cognitive load?
  Tracked separately; do not address here.
- Long-term measurement: per-task bash call count and token totals
  before vs after wrapper introduction. Tooling and dashboards are
  deferred; sub-PRs that introduce wrappers must at minimum record a
  before/after count from one honshu trace.

---

## Shared conventions for every sub-PR

- Branch naming: `refactor/builtin-tools-<short-topic>` (e.g.
  `refactor/builtin-tools-bash-redirection-check`).
- Each PR must:
  - State its single goal in the PR description, mirroring the "Goal"
    bullet from this file.
  - Ship colocated tests in `<source>_test.go` covering the success
    path and the most likely failure patterns
    (see `.github/instructions/implementation-and-tests.instructions.md`).
  - Run `go test ./internal/llm/...`, `go vet ./...`, and `go build ./...`
    locally and confirm green before merge.
  - Update `internal/llm/internal/builtin/system_instructions.md` only
    when the change is observable to skills (default tool set, new
    safety rule, new wrapper). Do not document refactors that are
    invisible to skills.
  - Follow `.github/instructions/branch-and-pr-workflow.instructions.md`
    for branching and PR creation.

---

## PR C-1 — Bash redirection static safety check

**Goal.** Make `bash` reject redirections whose target escapes the
workspace or is dynamic, while continuing to accept static
inside-workspace targets and heredocs. This is a hard prerequisite for
later PRs that lean more heavily on `bash`.

**Scope.**

1. In `internal/llm/internal/builtin/bash.go`, add a
   `checkRedirections(command string, ws workspace) error` helper that:
   - Parses the command with `mvdan.cc/sh/v3/syntax` (already a
     dependency) and walks every `*syntax.Redirect` node.
   - Skips heredoc forms (`<<`, `<<-`) and here-strings (`<<<`); they
     do not touch host filesystem paths beyond the script body.
   - For each remaining redirect (`>`, `>>`, `<`, `&>`, `>&`, `2>`,
     `2>>`, `&>>`, etc. — anything whose `Word` names a host path):
     - If the target word is dynamic (any non-literal/non-quoted-literal
       part, including `$var`, `$(...)`, backticks) → reject with a
       descriptive error.
     - If the target word is static, build its string via the existing
       `staticWordValue`. Call `ws.ResolvePath(target)`; if that
       returns an error, reject with that error wrapped.
2. Call `checkRedirections` inside the existing handler in
   `newBashWithConfig`, after the allowlist check, before invoking
   `/bin/bash`.
3. Do **not** attempt to validate redirection-like patterns that appear
   as arguments to commands (e.g., `tee /etc/foo`, `cp x /etc/foo`,
   `dd of=/etc/foo`). Document explicitly in the bash tool description
   and `system_instructions.md` that argument-level write targets are
   not validated and that writes should go through redirections or the
   Go file tools when workspace containment matters.

**Tests** (in `internal/llm/internal/builtin/bash_test.go`).

- `TestBashRedirectionRejectsAbsoluteEscape` — `echo x > /tmp/foo`
  is rejected before bash runs.
- `TestBashRedirectionAllowsRelativeInsideWorkspace` —
  `echo x > out.txt` succeeds and writes inside the temp workspace.
- `TestBashRedirectionAllowsAbsoluteInsideWorkspace` —
  `echo x > <ws.Root>/out.txt` succeeds.
- `TestBashRedirectionRejectsDynamicTarget` — `echo x > $OUT` is
  rejected with a "dynamic" error.
- `TestBashRedirectionRejectsCommandSubstitutionTarget` —
  `echo x > $(echo path)` is rejected.
- `TestBashRedirectionAllowsHeredoc` —
  `cat <<'EOF'\nhello\nEOF` succeeds and prints `hello`.
- `TestBashRedirectionAllowsAppend` — `echo a >> out.txt` succeeds.
- `TestBashStderrRedirectionChecked` — `cmd 2> /tmp/escape` is
  rejected.

**Docs.**

- Update the bash tool `Description()` string in `bash.go` to mention:
  "Redirection targets must be static and resolve inside the workspace;
  argument-level write targets (e.g. `tee /etc/foo`) are not
  validated."
- Update the "Built-in tools" section of
  `internal/llm/internal/builtin/system_instructions.md` with one
  sentence stating the same rule and recommending `write_file` /
  `edit_file` for non-trivial inside-workspace writes.

**Out of scope.** No change to the allowlist, no change to the default
tool set, no new tools.

**Verifiable acceptance.**

- All new tests above pass.
- Existing bash tests in `bash_test.go` still pass.
- A run of the honshu example completes without regressions. If any
  pre-existing skill command relied on a now-rejected redirection,
  capture it in this file under a new "PR C-1 incidents" sub-bullet
  rather than relaxing the check.

---

## PR C-2 — Default tool set restructure (extras opt-in)

**Goal.** Remove `list_dir` and `pwd` from the default builtin tool
set. Expose them through an opt-in `BuiltinExtras` config field so
existing skills can re-enable them when needed. Update documentation
to describe the new default set.

**Prerequisite.** PR C-1 merged.

**Scope.**

1. In `internal/llm/internal/builtin/builtin.go`:
   - Split `Tools(...)` into:
     - `coreTools(ws, cfg) []tool` — `bash`, `read_file`, `write_file`,
       `edit_file`, `delete_file`, `move_file`, `grep` (in that order).
     - `extraTool(ws, name) (tool, bool)` — returns the constructed
       tool for `"list_dir"` or `"pwd"`, or `(nil, false)`.
   - Replace the public `Tools(ws, bashAllow, bashBlock)` with
     `Tools(ws, bashAllow, bashBlock, extras []string)`. Iterate
     `extras` in caller order, appending each known extra exactly
     once; unknown names return an error so typos surface early.
2. In `internal/llm/skill.go`:
   - Add `BuiltinExtras []string` to `SkillConfig`.
   - Persist it on `Skill` next to `bashAllow` / `bashBlock`.
   - Carry it through `copy()` and any `WithXxx`-style propagation that
     already exists for the bash fields.
3. In `internal/llm/builtin_tools.go`:
   - Pass the new extras slice through `builtin.Tools(...)`.
   - Update `isBuiltinToolName` to keep recognizing `list_dir` and
     `pwd` (they are still builtin, just not default).
4. Update callers that previously assumed `list_dir` / `pwd` were
   always present:
   - `demo/skill/main.go`: if it still relies on either name, switch
     to setting `BuiltinExtras: []string{"list_dir", "pwd"}`. If it
     does not need them, leave it on the new default set.
   - `examples/honshu_groundwater/...`: same audit. Grep for
     `list_dir` and `pwd` under `examples/` and `demo/` before deciding.

**Tests.**

- `internal/llm/internal/builtin/builtin_test.go`:
  - Replace / update `TestToolsReturnsExpectedNames` so the default set
    is exactly the seven core tools, in the documented order.
  - Add `TestToolsAppendsExtras` covering
    `extras=[]string{"list_dir", "pwd"}` returns the seven core tools
    followed by the two extras in caller order.
  - Add `TestToolsRejectsUnknownExtra` for `extras=[]string{"nope"}`.
- `internal/llm/builtin_tools_test.go`:
  - Add `TestBuiltinToolsRespectsBuiltinExtras` for the new field
    propagation.
- `internal/llm/skill_test.go`:
  - Add `TestNewSkill_CarriesBuiltinExtras`.

**Docs.**

- Update `system_instructions.md` "Built-in tools" section:
  - List the seven default tools.
  - State that `list_dir` and `pwd` are available only when the host
    skill opts in via `BuiltinExtras`, and that `ls` / `pwd` via bash
    cover the same ground when they are not opted in.
  - Reaffirm that the workspace bootstrap block already names the
    relevant paths so `pwd` is normally redundant.

**Out of scope.** No new tools, no allowlist changes, no wrapper tools.

**Verifiable acceptance.**

- New tests pass and existing tests still pass.
- `grep -rn '"list_dir"\|"pwd"' demo examples` shows either no
  occurrences or matches that are now gated behind `BuiltinExtras`.
- Honshu example still runs end-to-end.

---

## PR C-3 — Wrapper-tool trace investigation (decision PR, no tool code)

**Goal.** Decide, from real traces, whether any wrapper tool is worth
adding, and record the decision. This PR ships only the methodology,
captured data, and the conclusion. It must not add new wrapper tools.

**Prerequisite.** PR C-2 merged so the trace data reflects the new
default set.

**Scope.**

1. Add a short methodology section to this plan file (`pr-c-builtin-tools-restructure-plan.md`)
   under a new heading "PR C-3 results" describing:
   - Which honshu run was used (commit SHA, command, artifact path).
   - How tool calls were extracted. Default approach: parse
     `artifacts/persistence/workspace/<task>/exchange/*.md` and
     `.../debug/*.json` for `tool_call` events and tally by tool name
     plus, for `bash`, by the first command head.
2. Record:
   - Top 10 `bash` command heads by frequency.
   - Top 5 multi-stage pipelines (commands containing `|`) by exact
     string after collapsing whitespace.
   - Count of total bash calls vs total Go-tool calls.
3. Apply the decision rule:
   - If a single pipeline pattern appears `>= 5` times in one run and
     can be safely re-implemented in Go via `ResolvePath`, schedule a
     dedicated PR for it (PR C-4, C-5, ... below).
   - Otherwise, conclude "no wrapper required at this time" and close
     the wrapper track. Future traces can reopen it.
4. Update this file with the final list of approved wrapper PRs (or
   the explicit "none approved" conclusion).

**Tests.** None — this PR ships documentation only.

**Verifiable acceptance.**

- The "PR C-3 results" section in this file is filled in with real
  numbers and an explicit decision.
- If wrappers are approved, each one has a stub sub-PR section
  appended below (use the PR C-4 template).

---

## PR C-4 (conditional) — `pipeline_search_replace` wrapper

Only ship this PR if PR C-3 approves it.

**Goal.** Provide a Go-implemented equivalent of
`sed -i 's/find/replace/g' path` that goes through `ResolvePath`.

**Scope.**

1. New file `internal/llm/internal/builtin/pipeline_search_replace.go`
   with `newPipelineSearchReplace(ws workspace) tool`.
2. JSON schema:
   - `path` (string, required)
   - `pattern` (string, required) — substring by default
   - `replacement` (string, required)
   - `regex` (bool, default false) — interpret pattern as Go regex
   - `max_replacements` (int, optional, default 0 = all)
3. Resolve `path` via `ws.ResolvePath`. Read file, perform replacement
   in-memory, write back with the same mode. Return a one-line summary
   (`"replaced N occurrence(s) in <path>"`).
4. Register it inside `coreTools` from PR C-2 and update the documented
   default set accordingly.

**Tests** (in `pipeline_search_replace_test.go`).

- Primary path: literal substring replacement.
- Regex mode: replaces all matches; with `max_replacements=1` only the
  first match changes.
- Path escapes are rejected (relative escape and absolute outside).
- File not found is reported.
- Zero matches is a no-op success with a clear summary.

**Docs.** One bullet in `system_instructions.md` under "Built-in tools"
naming the wrapper and the pipeline it replaces.

**Verifiable acceptance.** New tests pass; honshu example still runs.

---

## PR C-5 (conditional) — `file_excerpt` wrapper

Only ship this PR if PR C-3 approves it.

**Goal.** Provide a token-frugal equivalent of `grep -A N -B M path`
that returns matching regions of a file without the model having to
chain grep + read_file.

**Scope.**

1. New file `internal/llm/internal/builtin/file_excerpt.go`.
2. JSON schema:
   - `path` (string, required) — resolved via `ws.ResolvePath`
   - `anchor_pattern` (string, required)
   - `before` (int, default 0)
   - `after` (int, default 0)
   - `regex` (bool, default false)
   - `max_matches` (int, default 5)
3. Behavior mirrors `grep` but always returns the surrounding window
   and never the whole file. Reuses the existing `grep` matching
   helpers where possible; do not duplicate regex/substring logic.

**Tests** (in `file_excerpt_test.go`). Mirror the failure patterns
listed in PR C-4.

**Docs.** One bullet in `system_instructions.md`.

**Verifiable acceptance.** New tests pass; honshu example still runs.

---

## PR C-6 — Final cleanup and memo update

**Goal.** Close out the PR C track in the deferred-refactor tracker.

**Scope.**

1. Update `docs/deferred-refactor-tracker-memo.md` § "PR C - Builtin
   Tools Restructure":
   - Mark status as "Completed" (or "Completed with deferred wrapper
     follow-ups" if PR C-3 deferred them).
   - Replace the "Open questions" block with the resolved answers from
     this file's "Confirmed decisions" section.
2. Keep this plan file (`pr-c-builtin-tools-restructure-plan.md`) as
   the historical record; do not delete it.

**Tests.** None — documentation only.

**Verifiable acceptance.** The deferred tracker no longer lists PR C
as pending work, and a reader following the memo link lands on this
file for the historical detail.

---

## PR sequencing summary

1. **PR C-1** — bash redirection safety (mandatory, foundational).
2. **PR C-2** — default tool set restructure with extras opt-in.
3. **PR C-3** — trace investigation; produces decisions for C-4/C-5.
4. **PR C-4 / C-5** — conditional wrappers, one per PR.
5. **PR C-6** — close out memo.

PRs C-1, C-2, C-3, and C-6 are always in scope. PRs C-4 and C-5 ship
only if PR C-3 approves them.
