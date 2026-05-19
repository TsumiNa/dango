# Track D — VCS and Artifact Handling Plan

Status: planned, split across multiple PRs.
Source memo entry: `docs/builtin-tools-coverage-memo.md` § 3.4 and § 3.5.

This document is the canonical implementation plan for adding
version-control inspection and structured-artifact handling to the
dango builtin tool set. It is written so a coding agent can pick up any
single PR below and complete it without rereading the conversation that
produced this plan.

## Goals

- Let skills inspect git history (log, diff, show, blame) on workspace
  or accessible-directory checkouts without leaving the bash tool.
- Provide a single-call summary of a task's downstream artifacts that
  combines on-disk metadata with handoff front-matter descriptions.
- Provide a token-frugal shape preview of JSON / JSON Lines / YAML
  files without forcing the model to chain `jq` / `yq` with `head`.
- Keep every PR independently verifiable; each one ships with
  colocated tests and a clear acceptance check.

## Non-goals

- No Python-dependent research tooling. § 3.1–§ 3.3 of the coverage
  memo move to the external-skill track via the `cmd` program cycle
  and are not addressed here.
- No security envelope changes (deferred per the coverage memo § 0).
  No URL allowlist, no env scrubbing, no argument-level write-target
  inspection in this track.
- No wrapper around destructive git subcommands (`push`, `reset`,
  `rebase`, `gc`, `clean`). They remain reachable via bash but are
  documented as discouraged for autonomous use.
- No change to the orchestrator, runner, exchange, or handoff layers.

## Confirmed decisions

1. **Default placement, with a generality/risk caveat.** All three
   capabilities ship in the default builtin set (`git` on the bash
   allowlist; `artifact_catalog` and `structured_preview` in
   `coreTools`). They are general, workspace-bounded, and read-oriented.
   Higher-risk follow-ups would move behind `BuiltinExtras`; nothing in
   this track does.
2. **Multi-PR delivery, one verifiable goal per PR.** No PR ships more
   than one capability. Plan-only PRs (no code) are allowed.
3. **In-branch API compatibility rule applies.** Update call sites
   directly. Do not add deprecated aliases. Honor
   `.github/instructions/in-branch-api-compat.instructions.md`.
4. **No new module dependencies unless the PR explicitly calls for it.**
   PR D-3 may promote `gopkg.in/yaml.v3` from indirect to direct; no
   other dependency changes are expected.

## Shared conventions for every sub-PR

- Branch naming: `feat/builtin-tools-<short-topic>` (for example
  `feat/builtin-tools-git-allowlist`).
- Each PR must:
  - State its single goal in the PR description, mirroring the "Goal"
    bullet from this file.
  - Ship colocated tests in `<source>_test.go` covering the success
    path and the most likely failure patterns. See
    `.github/instructions/implementation-and-tests.instructions.md`.
  - Run `go test ./internal/llm/...`, `go vet ./...`, and
    `go build ./...` locally and confirm green before merge.
  - Update `internal/llm/internal/builtin/system_instructions.md` only
    when the change is observable to skills (new tool, new default
    capability). Internal refactors do not need a doc update.
  - Follow `.github/instructions/branch-and-pr-workflow.instructions.md`
    for branching and PR creation.

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

**Goal.** Close out the Track D plan in the coverage memo and the
deferred-refactor tracker after PRs D-1 through D-3 ship.

**Prerequisite.** PR D-1, PR D-2, and PR D-3 merged.

**Scope.**

1. Update `docs/builtin-tools-coverage-memo.md` § 3.4 and § 3.5 to
   record the delivered PR numbers and link back to this file.
2. Add a one-line "Completed" status note at the top of this file.
3. Append a short "Track D delivered" entry to
   `docs/deferred-refactor-tracker-memo.md` (or its successor section
   if the file structure has changed) noting the closeout, mirroring
   how PR C was closed out in PR C-6.

**Tests.** None — documentation only.

**Verifiable acceptance.** A reader following the coverage memo's
§ 3.4 or § 3.5 reference lands on this file's "Completed" status and
the specific PR numbers.

---

## PR sequencing summary

1. **PR D-1** — `git` added to the default bash allowlist.
2. **PR D-2** — `artifact_catalog` builtin tool.
3. **PR D-3** — `structured_preview` builtin tool.
4. **PR D-4** — closeout and memo update.

PR D-1 / D-2 / D-3 are independent and may land in any order. PR D-4
must come last.
