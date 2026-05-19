# Builtin Tools — Near-Term Plan

Status: planned, split across three tracks and multiple PRs per track.
Source memo: `docs/builtin-tools-coverage-memo.md` § 0 (triage) and the
per-subsection track tags in § 2 and § 3.

This document is the canonical implementation plan for the near-term
work that follows the PR C builtin-tools restructure. It owns three
tracks:

- **Track D — VCS and artifact handling.** Coverage memo § 3.4 and
  § 3.5. Go-resident, workspace-bounded, read-oriented.
- **Track E — MCP support.** Coverage memo § 0.2 class 3. Wire dango
  as an MCP client so the four-class tool architecture is real, and
  unblock § 3.1 capabilities like `web_search` and paper fetch via
  published MCP servers.
- **Track F — Security pre-hooks and instrumentation.** Coverage memo
  § 2.4. Land the zero-cost interface stubs and trace data collection
  now so the post-alpha structural security phase has hooks and data.

Tracks run in parallel; PRs within a track are largely independent.
Closeout per track is its own PR.

## Goals

- Land the Go builtin gaps named in coverage memo § 3.4 and § 3.5
  (Track D).
- Stand up first-class MCP client support so the app/cmd cycle can
  ship MCP server configs out of the box (Track E).
- Install the pre-hooks the post-alpha security phase will lean on,
  without changing today's default behavior (Track F).
- Keep every PR independently verifiable; each one ships with
  colocated tests (where code changes) and a clear acceptance check.

## Non-goals

- No Python-dependent research tooling. Those move to app/cmd packaged
  skills per coverage memo § 0.2.
- No structural security mitigations. Env scrubbing UX, default-on URL
  enforcement, argument-level write-target inspection, and resource
  caps wait for the § 0.5 alpha trigger.
- No wrapper around destructive git subcommands (`push`, `reset`,
  `rebase`, `gc`, `clean`). They remain reachable via bash but are
  documented as discouraged for autonomous use.
- No change to the orchestrator, runner, exchange, or handoff layers
  beyond what MCP wiring strictly requires.

## Confirmed decisions (apply to every track)

1. **Default-vs-`BuiltinExtras` placement follows the coverage memo
   § 0.4 rubric** — single-shot, stateless, predictable, general → default;
   narrower or side-effect-heavy → `BuiltinExtras`; runtime-bound →
   not a builtin.
2. **Multi-PR delivery, one verifiable goal per PR.** No PR ships more
   than one capability. Plan-only PRs (no code) are allowed.
3. **In-branch API compatibility rule applies.** Update call sites
   directly. Do not add deprecated aliases. Honor
   `.github/instructions/in-branch-api-compat.instructions.md`.
4. **No new module dependencies unless the PR explicitly calls for it.**
   PR D-3 may promote `gopkg.in/yaml.v3` from indirect to direct; the
   Track E design PR may select an MCP/JSON-RPC library; no other
   dependency changes are expected.

## Shared conventions for every sub-PR

- Branch naming: `feat/builtin-tools-<short-topic>` for Tracks D and F,
  `feat/mcp-<short-topic>` for Track E.
- Each PR must:
  - State its single goal in the PR description, mirroring the "Goal"
    bullet from this file.
  - Ship colocated tests in `<source>_test.go` covering the success
    path and the most likely failure patterns. See
    `.github/instructions/implementation-and-tests.instructions.md`.
  - Run `go test ./...`, `go vet ./...`, and `go build ./...` locally
    and confirm green before merge.
  - Update `internal/llm/internal/builtin/system_instructions.md` only
    when the change is observable to skills (new tool, new default
    capability). Internal refactors do not need a doc update.
  - Follow `.github/instructions/branch-and-pr-workflow.instructions.md`
    for branching and PR creation.

---

# Track D — VCS and Artifact Handling

Source memo entry: `docs/builtin-tools-coverage-memo.md` § 3.4 and § 3.5.

Goal recap: let skills inspect git history without leaving bash;
provide a single-call summary of downstream artifacts that merges
on-disk metadata with handoff front matter; provide a token-frugal
shape preview of JSON / JSONL / YAML.

---

## PR D-1 — Add `git` to the default bash allowlist

**Goal.** Allow `git <subcommand>` invocations through the bash tool so
skills can run read-oriented git inspection (`log`, `diff`, `show`,
`blame`, `status`, `rev-parse`). Destructive subcommands remain
reachable but are documented as discouraged.

**Scope.**

1. In `internal/llm/internal/builtin/allowlist.go`:
   - Add a new comment block for version control and append `"git"` to
     `defaultAllowlist`.
   - Extend the package comment to mention that `git` is allowed for
     read-oriented inspection and that destructive subcommands should be
     handled by the skill author rather than the runtime today.
2. Update the bash tool `Description()` string in `bash.go` only if
   needed to mention `git` as one of the inspection commands; prefer
   leaving the description generic.
3. Update `internal/llm/internal/builtin/system_instructions.md`
   "Built-in tools" / bash bullet to list `git log`, `git diff`,
   `git show`, `git status` as standard read-oriented uses, and
   explicitly recommend the skill avoid `git push`, `git reset --hard`,
   `git rebase`, `git clean`, `git gc` unless the task says otherwise.

**Tests** (in `internal/llm/internal/builtin/allowlist_test.go` or
`bash_test.go`, whichever already exercises allowlist membership).

- `TestDefaultAllowlistIncludesGit` — `git` appears in the resolved
  default allowlist set.
- `TestBashAllowsGitVersion` — `git --version` runs and prints a
  version line.
- `TestBashAllowsGitLogInsideWorkspace` — initialize a temp git repo
  inside the workspace, make a commit, run `git -C <ws.Root> log -1`,
  and assert the commit subject appears in the output.
- `TestBashRejectsGitOutsideWorkspaceTarget` — confirm that a bash
  invocation that tries to redirect git output outside the workspace
  (`git log > /tmp/escape`) is still rejected by the redirection check
  from PR C-1. This guards the regression boundary, not git itself.

**Out of scope.** No `git_*` wrapper tool. No subcommand-level
restriction. No change to redirection or allowlist machinery.

**Verifiable acceptance.**

- New tests pass; existing `bash_test.go` and `allowlist_test.go` still
  pass.
- `go test ./internal/llm/...` is green.
- A run of the honshu example still completes; skills that did not use
  git see no observable change.

---

## PR D-2 — `artifact_catalog` builtin tool

**Goal.** Provide a single-call summary of a task's downstream
artifacts. The tool reads the on-disk contents of an artifacts
directory and, when available, the handoff front-matter `artifacts:`
list, returning a typed combined view so the model does not have to
chain `list_dir` + `read_file` + YAML parsing each turn.

**Prerequisite.** None. PR D-1 is independent.

**Scope.**

1. New file `internal/llm/internal/builtin/artifact_catalog.go` with
   `newArtifactCatalog(ws workspace) tool`.
2. JSON schema:
   - `path` (string, optional, default `downstream/artifacts`):
     directory to summarize. Resolved via `ws.ResolvePath`.
   - `handoff_path` (string, optional, default `downstream/handoff.md`):
     handoff document whose front matter contributes descriptions.
     Resolved via `ws.ResolvePath`. Skipped silently if absent.
   - `max_entries` (int, optional, default 50): cap on returned
     entries to keep output bounded.
3. Behavior:
   - Walk `path` (non-recursive by default; one level is enough for
     downstream artifacts). For each entry record relative path, kind
     (`file` or `dir`), size in bytes (files only), and mod time
     truncated to seconds.
   - If `handoff_path` exists, parse only its YAML front matter and
     extract the `artifacts:` list. Match by the front-matter `path`
     field against the entry's relative path under the workspace.
     Carry over `type` and `description` from the manifest.
   - Disk entries not declared in the manifest are returned with
     `manifest: unlisted`. Manifest entries not present on disk are
     returned with `manifest: declared, on_disk: missing`.
   - Output is a small markdown table with columns
     `path | kind | size | type | description | status`. Truncated
     rows produce a trailing `(N more, truncated)` line.
4. Front-matter parsing lives in this file as a small local helper;
   do not import `internal/engine/runner` from the builtin package
   (layering boundary). The helper only needs to recognize the
   `---\n...\n---\n` envelope and decode the `artifacts:` block using
   `gopkg.in/yaml.v3`. Other fields are ignored.
5. Register in `coreTools` between `file_excerpt` and the extras hook.
   Update `Tools(...)` ordering documentation if it changes.

**Tests** (in `artifact_catalog_test.go`).

- `TestArtifactCatalogReturnsDiskAndManifestMerge` — directory with
  two files and a matching handoff front-matter manifest; output table
  contains both files with declared types and descriptions.
- `TestArtifactCatalogFlagsUnlistedDiskEntry` — disk has a file not
  declared in the manifest; output marks it `unlisted`.
- `TestArtifactCatalogFlagsMissingManifestEntry` — manifest declares
  a file that is not on disk; output marks it `missing`.
- `TestArtifactCatalogMissingHandoffIsSilent` — handoff document does
  not exist; tool still returns the on-disk listing without error.
- `TestArtifactCatalogMissingDirectoryReturnsError` — `path` does not
  exist; tool returns a descriptive error.
- `TestArtifactCatalogPathEscapeRejected` — `path` is absolute and
  outside the workspace; rejected by `ws.ResolvePath`.
- `TestArtifactCatalogRespectsMaxEntries` — directory with 60
  entries and `max_entries=10` returns 10 entries plus the
  truncated-rows footer.

**Docs.** One bullet in `system_instructions.md` "Built-in tools"
naming the tool and describing the typical use case (summarize a
downstream handoff's artifact set in one call).

**Out of scope.** No write semantics. No recursion into subdirectories
beyond depth 1. No support for non-handoff manifests.

**Verifiable acceptance.**

- All new tests pass; existing builtin tests still pass.
- `go test ./internal/llm/...` is green.
- A run of the honshu example still completes; a quick spot check
  on one execute-stage trace confirms the tool, if invoked, returns
  a table with the documented columns.

---

## PR D-3 — `structured_preview` builtin tool

**Goal.** Provide a token-frugal shape preview of JSON, JSON Lines, and
YAML files. The tool reports the top-level structure and a bounded
schema sketch so the model can decide whether to read the file in full,
without `jq` / `yq` boilerplate.

**Prerequisite.** None. PR D-2 is independent; this PR may land before
or after it.

**Scope.**

1. New file `internal/llm/internal/builtin/structured_preview.go` with
   `newStructuredPreview(ws workspace) tool`.
2. JSON schema:
   - `path` (string, required): resolved via `ws.ResolvePath`.
   - `format` (string, optional, enum `auto`, `json`, `jsonl`, `yaml`,
     default `auto`): inferred from the file extension when `auto`.
     `.json` → `json`; `.jsonl`, `.ndjson` → `jsonl`;
     `.yaml`, `.yml` → `yaml`. Unknown extension under `auto` returns
     a descriptive error so the caller picks one explicitly.
   - `max_keys_per_level` (int, optional, default 20): per-object key
     cap. Excess keys collapse into a `(... N more)` summary.
   - `max_depth` (int, optional, default 3): walk depth cap.
   - `sample_rows` (int, optional, default 5, jsonl only): how many
     leading rows to scan when inferring a union schema.
3. Behavior:
   - JSON: parse with `encoding/json`. Walk the value to `max_depth`,
     emitting an indented sketch (`object{keys: [...], truncated: N}`
     or `array[len=N, elem: <type-sample>]`).
   - JSONL: read up to `sample_rows` lines, parse each, and report a
     union schema (key → set of seen JSON types and a `null_rate` if
     any row was missing it). Report total scanned rows.
   - YAML: parse with `gopkg.in/yaml.v3` into `interface{}` and reuse
     the JSON walker. Promote `yaml.v3` to a direct dependency in
     `go.mod` in this PR.
   - All caps emit a clear `(truncated)` marker so the model knows the
     preview is partial.
4. Register in `coreTools` adjacent to `artifact_catalog`. Update
   ordering documentation accordingly.

**Tests** (in `structured_preview_test.go`).

- `TestStructuredPreviewJSONObject` — small object; sketch lists keys.
- `TestStructuredPreviewJSONArray` — small array; sketch reports
  length and a representative element type.
- `TestStructuredPreviewJSONLSchemaInference` — three rows with one
  missing key; output reports the union schema and `null_rate` for
  the partially-present key.
- `TestStructuredPreviewYAMLObject` — small YAML map; output matches
  the JSON object form.
- `TestStructuredPreviewRespectsMaxDepth` — nested object; nodes
  past `max_depth` collapse to a `(truncated)` placeholder.
- `TestStructuredPreviewRespectsMaxKeysPerLevel` — object with 30
  keys and `max_keys_per_level=5` returns 5 keys plus a
  `(... 25 more)` line.
- `TestStructuredPreviewAutoFormatUnknownExtension` — `.txt` input
  with `format=auto` returns a clear error pointing the caller to
  set `format` explicitly.
- `TestStructuredPreviewMalformedInput` — invalid JSON; error names
  the line/column when the parser provides it.
- `TestStructuredPreviewPathEscapeRejected` — absolute path outside
  workspace; rejected by `ws.ResolvePath`.

**Docs.** One bullet in `system_instructions.md` "Built-in tools"
naming the tool and noting it is the lighter alternative to chaining
`jq` / `yq` with `head` for shape inspection.

**Out of scope.** No transformation, projection, or filtering features
(those belong with `jq`/`yq` if a future trace justifies wrappers).

**Verifiable acceptance.**

- All new tests pass; existing builtin tests still pass.
- `go test ./internal/llm/...`, `go vet ./...`, and `go build ./...`
  are green.
- If `gopkg.in/yaml.v3` was promoted to a direct dependency, `go mod
  tidy` produces no diff after the change.

---

## PR D-4 — Track D closeout and memo update

**Goal.** Close out Track D in the coverage memo and the deferred-
refactor tracker after PRs D-1 through D-3 ship.

**Prerequisite.** PR D-1, PR D-2, and PR D-3 merged.

**Scope.**

1. Update `docs/builtin-tools-coverage-memo.md` § 3.4 and § 3.5 to
   record the delivered PR numbers and link back to this file.
2. Add a one-line "Track D completed" status note at the top of this
   file.
3. Append a short "Track D delivered" entry to
   `docs/deferred-refactor-tracker-memo.md` (or its successor section
   if the file structure has changed) noting the closeout, mirroring
   how PR C was closed out in PR C-6.

**Tests.** None — documentation only.

**Verifiable acceptance.** A reader following the coverage memo's
§ 3.4 or § 3.5 reference lands on this file's "Track D completed"
status and the specific PR numbers.

---

# Track E — MCP Support

Source memo entry: `docs/builtin-tools-coverage-memo.md` § 0.2 class 3
and § 3.1.

Goal recap: wire dango as an MCP client so the four-class tool
architecture in coverage memo § 0.2 becomes real. This unblocks § 3.1
research capabilities (`web_search`, paper fetch, citation handling)
by consuming the published MCP ecosystem rather than reimplementing
each tool.

**Note on staging.** Track E is the largest of the three tracks
because it adds a new external integration surface (process lifecycle,
transport, tool adapter, config plumbing). PR E-0 owns the detailed
design and produces a dedicated plan file; PR E-1 through PR E-4 are
implementation sketches that may be refined when the design lands.

## PR E-0 — MCP support design doc

**Goal.** Produce a detailed design document, `docs/mcp-support-plan.md`,
that locks in protocol version, transport (stdio vs HTTP),
configuration shape, lifecycle ownership, error handling, and the
security posture for an MCP server (which runs as an external
subprocess with arbitrary permissions).

**Prerequisite.** None.

**Scope.**

1. Survey existing Go MCP client implementations and pick a
   dependency (or justify writing a thin client). Capture the choice
   and pinned version in the design doc.
2. Specify the dango-side config surface for declaring MCP servers
   (per-app/cmd config; whether skills can opt in/out individually;
   how `BuiltinExtras` interacts with MCP names).
3. Specify how MCP tool calls appear to the LLM (naming convention to
   disambiguate collisions with Go builtins; argument schema
   normalization; result truncation policy mirroring the bash 16 KiB
   cap).
4. Specify lifecycle ownership (who starts and stops the server, how
   the orchestrator and runner observe failures, what timeouts apply
   per tool call).
5. Specify the security posture: MCP servers are treated as trusted
   subprocess collaborators today. Coverage memo § 2 hardening
   eventually applies to them; the design doc must call this out so
   the post-alpha phase has a concrete starting point.
6. Refine PR E-1 / E-2 / E-3 scopes below if the design says they
   should split differently.

**Tests.** None — documentation only.

**Verifiable acceptance.** `docs/mcp-support-plan.md` exists, names
the chosen MCP client dependency, specifies config shape and
lifecycle, and the subsequent PRs cite this file in their PR
descriptions.

---

## PR E-1 — MCP client core

**Goal.** Implement the MCP client per the PR E-0 design: connection,
tool listing, tool invocation, and error propagation. No integration
with the dango tool surface yet.

**Prerequisite.** PR E-0 merged.

**Scope.**

1. New package (likely `internal/mcp` — final placement decided in
   PR E-0).
2. Implement: spawn / connect to an MCP server using the selected
   transport, perform initialization handshake, expose `ListTools`,
   `CallTool`, and a `Close` lifecycle hook.
3. Treat the client as a low-level building block; do not yet plug it
   into `internal/llm`. Other packages cannot import this yet.

**Tests** (in `<source>_test.go` files inside the new package).

- `TestMCPClientLifecycleAgainstStub` — spin up a stub MCP server
  that echoes a fixed tool list; assert the client connects, lists,
  and closes cleanly.
- `TestMCPClientCallToolReturnsResult` — stub server replies with a
  canned result; client returns it.
- `TestMCPClientCallToolPropagatesError` — stub server returns an
  RPC error; client surfaces a typed error.
- `TestMCPClientHandshakeTimeout` — stub server hangs on handshake;
  client returns a timeout error within the configured bound.

**Verifiable acceptance.** New tests pass. `go test ./...`,
`go vet ./...`, `go build ./...` are green. The new dependency (if
any) is pinned in `go.mod` with no other diff.

---

## PR E-2 — MCP tool adapter

**Goal.** Expose MCP tools through the dango `tool` interface so the
llm package can offer them alongside Go builtins.

**Prerequisite.** PR E-1 merged.

**Scope.**

1. Add an adapter in `internal/llm` that wraps a `mcp.Client`'s
   advertised tools as `internal/llm/internal/builtin`-shaped tools.
   The adapter handles name disambiguation, argument schema
   forwarding, and result string conversion per the PR E-0 design.
2. Surface a single internal helper that returns the merged tool list
   (Go builtins + adapted MCP tools) for a given skill construction.
   Do not change the public `Tools(...)` signature in this PR; route
   MCP tools through a sibling helper so the diff stays bounded.
3. No skill config plumbing yet — that lands in PR E-3.

**Tests** (in the llm package).

- `TestMCPAdapterListsTools` — given a fake `mcp.Client` exposing
  two tools, the adapter returns two `tool` values with the expected
  names.
- `TestMCPAdapterInvokesUnderlyingClient` — `Execute` on the
  adapted tool calls `CallTool` with the right arguments.
- `TestMCPAdapterDisambiguatesNameCollision` — adapted name
  collides with a Go builtin; adapter applies the rule from PR E-0.

**Verifiable acceptance.** New tests pass; existing tests pass.
`go test ./...` green.

---

## PR E-3 — Skill config plumbing for MCP servers

**Goal.** Let an app/cmd declare a set of MCP servers and let
`SkillConfig` opt skills in or out of them, so the whole pipeline runs
end-to-end.

**Prerequisite.** PR E-1 and PR E-2 merged.

**Scope.**

1. Extend `SkillConfig` with the MCP server reference list per the
   PR E-0 design (name, transport details, allowed-tool subset).
2. Plumb the configured servers through `builtin_tools.go` so MCP
   tools are appended to the skill's tool list in a deterministic
   order.
3. Update a demo (`demo/skill/main.go` or `examples/...`) to
   demonstrate one MCP server hookup against a stub.

**Tests** (in `skill_test.go` and `builtin_tools_test.go`).

- `TestNewSkill_CarriesMCPServers` — config propagation.
- `TestBuiltinToolsAppendsMCPTools` — MCP tools appear after Go
  builtins in the merged list.
- `TestSkillRejectsUnknownMCPServer` — typo surfaces as a build-time
  error.

**Verifiable acceptance.** All new tests pass; existing tests pass.
The demo run hits the stub MCP server end-to-end.

---

## PR E-4 — Track E closeout

**Goal.** Close out Track E in the coverage memo and the deferred-
refactor tracker after PRs E-1 through E-3 ship.

**Prerequisite.** PR E-1, PR E-2, PR E-3 merged.

**Scope.**

1. Update coverage memo § 3.1 to record that `web_search` and paper
   fetch are now reachable via MCP, and point to specific recommended
   MCP servers (if any are nominated).
2. Add a "Track E completed" line to this plan file and to the
   MCP design doc.
3. Append a Track E entry to `docs/deferred-refactor-tracker-memo.md`.

**Tests.** None — documentation only.

**Verifiable acceptance.** Coverage memo § 3.1 and § 0.2 cross-reference
the delivered MCP support; an app/cmd embedding dango can declare an
MCP server and have its tools appear in a skill's LLM tool list.

---

# Track F — Security Pre-Hooks and Instrumentation

Source memo entry: `docs/builtin-tools-coverage-memo.md` § 2.4.

Goal recap: install the four mitigations and instrumentation items
that the post-alpha structural security phase will lean on. None of
these changes default behavior; they exist so the structural phase
does not need to retrofit interfaces or rebuild trace data.

---

## PR F-1 — `WithBashURLAllowlist([]string)` opt-in

**Goal.** Add a per-skill / per-app bash option that, when set,
restricts `curl` and `wget` to a configured list of URLs (or URL
prefixes). When unset (default), behavior is unchanged.

**Prerequisite.** None.

**Scope.**

1. In `internal/llm/internal/builtin/bash.go` (and `allowlist.go` or
   a new `url_allowlist.go` if cleaner): extend the parsed-AST walker
   to find `curl` and `wget` invocations and extract their URL
   arguments (positional and `--url ...`).
2. Add a `withBashURLAllowlist([]string) option` next to the existing
   bash options. Persist a parsed list on `config`. Empty (or nil)
   means "no restriction" so the current behavior is preserved.
3. When non-empty, reject any `curl` / `wget` call whose URL is not a
   prefix match of any list entry. Reject calls whose URL is dynamic
   (variable / command substitution) — same rule as the redirection
   check, for the same reason.
4. Surface the option through the public `Tools(...)` constructor as
   a new optional field (or a `WithBashURLAllowlist(...)` exported
   option that mirrors the redirection-check shape).

**Tests** (in `bash_test.go` or `url_allowlist_test.go`).

- `TestBashURLAllowlistEmptyAllowsAnyURL` — default behavior
  unchanged.
- `TestBashURLAllowlistAllowsListedURL` — `curl https://allowed/x`
  passes when `https://allowed` is on the list.
- `TestBashURLAllowlistRejectsUnlistedURL` — `curl https://other/x`
  rejected with a descriptive error.
- `TestBashURLAllowlistRejectsDynamicURL` — `curl $URL` rejected.
- `TestBashURLAllowlistAppliesToWget` — symmetric check for wget.

**Out of scope.** No application to pip / npm / cargo URL fetches;
those follow their own resolvers and are deferred. No change to
default policy; the option is opt-in.

**Verifiable acceptance.** New tests pass; existing tests pass.
Honshu example still runs (since the option is opt-in).

---

## PR F-2 — `TrustedInput bool` flag on `SkillConfig`

**Goal.** Add a declarative flag to `SkillConfig` that hints whether
the skill's input may originate from an untrusted source (user-typed
task, external webhook payload, scraped content). The flag has no
behavior gate today; it exists as a stable hook later mitigations can
consult without breaking the config surface.

**Prerequisite.** None.

**Scope.**

1. In `internal/llm/skill.go`: add `TrustedInput bool` to
   `SkillConfig` (defaulting to `true` to preserve current behavior).
2. Persist it on `Skill` next to other config-derived fields and
   propagate through `copy()` / option helpers.
3. Document in the field comment that the post-alpha hardening phase
   will use this flag to gate env scrubbing, URL allowlist defaults,
   and resource caps. No gating logic is added in this PR.

**Tests** (in `skill_test.go`).

- `TestNewSkill_DefaultsTrustedInputTrue`
- `TestNewSkill_CarriesTrustedInputFalse`
- `TestSkillCopyPreservesTrustedInput`

**Out of scope.** No behavior change. No use of the flag in any code
path.

**Verifiable acceptance.** New tests pass; existing tests pass.

---

## PR F-3 — Audit-tag the existing tool-call stream events

**Goal.** Mark the `llm.tool_call.started` and `llm.tool_call.completed`
events as the canonical audit-log source so the post-alpha hardening
phase can rely on a stable schema instead of building a separate audit
pipeline.

**Prerequisite.** None.

**Scope.**

1. Locate the event emission for tool calls in the llm runtime
   (`internal/llm/...`; the PR D-3 trace investigation already cited
   `llm.tool_call.started`).
2. Add a stable `category: "audit"` field (or equivalent — final name
   chosen during implementation) and confirm the event already
   carries: tool name, argument summary, result summary (truncated
   to a documented cap), timestamps, skill ID, and request/runner
   IDs. Add any missing field needed to make the event self-contained
   as an audit record.
3. Document the audit schema in a short section of
   `docs/builtin-tools-coverage-memo.md` § 2.4 or a new dedicated
   `docs/tool-call-audit-schema.md` (pick the lighter option).

**Tests** (next to the event-emission code).

- `TestToolCallStartedEventCarriesAuditCategory`
- `TestToolCallCompletedEventCarriesAuditCategory`
- `TestToolCallEventTruncatesLargeArguments` (if not already
  enforced).

**Out of scope.** No separate audit storage. No new event pipeline.

**Verifiable acceptance.** Tests pass; reading a fresh
`artifacts/debug/stream_events.jsonl` from one example run shows the
audit category on every tool-call event.

---

## PR F-4 — Trace-analysis utility for security design data

**Goal.** Automate the PR C-3-style trace analysis into a small Go
program under `tools/` (or `cmd/internal-tools/...`) so each example
run can produce the dataset the post-alpha hardening phase needs:
bash command-head distribution, captured inner-command bodies of
Turing-complete heads (`python -c`, `bash -c`, `xargs <cmd>`, `make`,
`awk` system-calls), per-skill tallies, and URL frequencies for
`curl` / `wget`.

**Prerequisite.** PR F-3 merged (so the audit-tag is available).

**Scope.**

1. Add a Go program (location decided in this PR; default proposal
   `tools/analyze-tool-traces/main.go`) that consumes
   `artifacts/debug/stream_events.jsonl` and emits a markdown report
   plus a machine-readable JSON sidecar.
2. The report mirrors the PR C-3 results layout (top heads, top
   pipelines, totals) and adds the new dimensions (Turing-complete
   head inner bodies, URL frequencies).
3. Add a `make analyze-traces` (or equivalent) entrypoint so the
   utility is one command away.

**Tests** (in `<source>_test.go` next to the new tool).

- `TestAnalyzerSummarizesBashHeads` against a fixture jsonl.
- `TestAnalyzerCapturesInnerCommandBodies`.
- `TestAnalyzerCountsURLsByHost`.

**Out of scope.** No dashboarding. No persistence beyond the
report/JSON sidecar. No automatic gating on the data.

**Verifiable acceptance.** Tests pass; running the analyzer against
the existing PR C-3 sample artifact produces a report consistent with
the PR C-3 findings.

---

## PR F-5 — Track F closeout

**Goal.** Close out Track F in the coverage memo and the deferred-
refactor tracker after PRs F-1 through F-4 ship.

**Prerequisite.** PR F-1, PR F-2, PR F-3, PR F-4 merged.

**Scope.**

1. Update coverage memo § 2.4 to record the delivered PR numbers.
2. Add a "Track F completed" line to this plan file.
3. Append a Track F entry to `docs/deferred-refactor-tracker-memo.md`.

**Tests.** None — documentation only.

**Verifiable acceptance.** Coverage memo § 2.4 cross-references the
delivered pre-hooks and the analyzer; § 2.3 still lists the deferred
structural items.

---

## PR sequencing summary

Near-term tracks run in parallel; PRs within a track follow the
dependencies stated in each PR's "Prerequisite" block.

Track D (VCS and artifacts):

1. **PR D-1** — `git` added to the default bash allowlist.
2. **PR D-2** — `artifact_catalog` builtin tool.
3. **PR D-3** — `structured_preview` builtin tool.
4. **PR D-4** — Track D closeout.

PR D-1 / D-2 / D-3 are independent and may land in any order. PR D-4
must come after them.

Track E (MCP support):

1. **PR E-0** — MCP design doc.
2. **PR E-1** — MCP client core.
3. **PR E-2** — MCP tool adapter.
4. **PR E-3** — Skill config plumbing.
5. **PR E-4** — Track E closeout.

Track E is strictly sequential.

Track F (Security pre-hooks and instrumentation):

1. **PR F-1** — `WithBashURLAllowlist([]string)` opt-in.
2. **PR F-2** — `TrustedInput bool` flag.
3. **PR F-3** — Audit-tag tool-call stream events.
4. **PR F-4** — Trace-analysis utility (depends on F-3).
5. **PR F-5** — Track F closeout.

PR F-1 / F-2 / F-3 are independent and may land in any order. PR F-4
needs PR F-3. PR F-5 must come last.
