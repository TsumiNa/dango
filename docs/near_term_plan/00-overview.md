# Near-Term Plan — Overview

Status: planned, split into per-subtask files in this folder.
Source memo: `docs/builtin-tools-coverage-memo.md` § 0 (triage).

This folder is the canonical near-term implementation plan that follows
the PR C builtin-tools restructure. Each subtask is its own file; the
numeric filename prefix encodes execution order. Files with the same
leading digit form a phase; lower numbers land first.

The plan is intentionally not exhaustive. Distant or speculative work
is omitted. Files are added and refined as development proceeds; do not
treat the current file set as final.

## Design philosophy (controls every decision below)

Dango exists to liberate users through reliable, long-running
automation so they can spend their attention on high-value decision
making, not on babysitting tooling. Two consequences run through this
plan:

- **Security must not push work onto the user.** Defaults are
  permissive (`passby`) so that the common path runs unattended. Users
  who bring their own tools, MCP servers, or skills are opting in and
  accept the associated risk; dango does not try to second-guess that
  choice. We provide control surfaces (allow/deny, `passby` /
  `need_approve` / `off`) but never force the user to operate them for
  the happy path.
- **Do not cripple capable tools for safety.** Where a tool is already
  powerful and the model is fluent with it (e.g. `curl`), we keep it
  intact and gate risk through the policy layer, rather than replacing
  it with a narrow reimplementation.

## Concerns in scope right now

1. **Unified tool/MCP/skill security model.** A single abstraction for
   what is available (allow/deny) and how it runs (`passby` /
   `need_approve` / `off`), applied uniformly to builtin tools, builtin
   extras, MCP tools, and skills. Foundation for everything else. See
   `10`, `11`, `12`.
2. **Go builtin gaps.** `git` inspection, `artifact_catalog`,
   `structured_preview`. See `20`, `21`, `22`.
3. **Bash egress opt-in.** Opt-in URL allowlist for `curl` / `wget`.
   See `30`.
4. **Skill alias and conflict handling.** See `40`.
5. **MCP support.** Design-first; implementation files added after the
   design lands. See `50`.
6. **Instrumentation.** Audit-tagging tool-call events and a trace
   analyzer for future security-design data. See `60`.

## Goals

- Establish one security abstraction that tools, MCP, and skills all
  flow through, with permissive defaults.
- Land the Go builtin gaps named in coverage memo § 3.4 and § 3.5.
- Stand up MCP client support so the app/cmd cycle can ship MCP
  configs out of the box.
- Keep every code subtask independently verifiable with colocated
  tests.

## Non-goals

- No Python-dependent research tooling. Those are app/cmd packaged
  skills per coverage memo § 0.2.
- No structural security mitigations that the post-alpha phase owns
  (env scrubbing UX, default-on egress enforcement, resource caps).
  See coverage memo § 2.3.
- No far-future planning. Refine this folder as work proceeds.

## Confirmed decisions (apply to every subtask)

1. **Placement rubric** follows coverage memo § 0.4: single-shot,
   stateless, predictable, general → default builtin; narrower or
   side-effect-heavy → builtin extra; runtime-bound → not a builtin.
2. **Multi-PR delivery, one verifiable goal per file.** Design-only
   files (no code) are allowed and expected for `10` and `50`.
3. **In-branch API compatibility rule applies.** Update call sites
   directly; no deprecated aliases. Honor
   `.github/instructions/in-branch-api-compat.instructions.md`.
4. **No new module dependencies unless the subtask explicitly calls
   for it.** `22` may promote `gopkg.in/yaml.v3` to direct; `50` may
   select an MCP client library.

## Shared conventions for every code subtask

- Branch naming: `feat/<short-topic>` (for example
  `feat/tool-security-model`).
- State the single goal in the PR description, mirroring the file's
  "Goal".
- Ship colocated `<source>_test.go` covering the success path and the
  most likely failure patterns. See
  `.github/instructions/implementation-and-tests.instructions.md`.
- Run `go test ./...`, `go vet ./...`, `go build ./...` green before
  merge.
- Update `internal/llm/internal/builtin/system_instructions.md` only
  when the change is observable to skills.
- Follow `.github/instructions/branch-and-pr-workflow.instructions.md`.

## Execution order and collision avoidance

The single biggest merge hazard is that several subtasks touch the same
tool-assembly code (`builtin.go` `coreTools` / `Tools(...)`,
`builtin_tools.go`, `skill.go`). To avoid repeated conflicts, the
security-model foundation lands first and changes those signatures
once; everything else registers into the post-refactor shape.

Order:

1. **`10` → `11` → `12`** (foundation). `10` is design only. `11`
   migrates builtin-extras to an enum and reshapes `Tools(...)` into
   the availability+policy config. `12` implements the
   `passby`/`need_approve`/`off` runtime, the bash command-pattern
   approval list, and the runner snapshot + dynamic-adjust interface.
   No later code subtask may start until `11` has reshaped the tool
   config, because they all register into it.
2. **`20` → `21` → `22`** (Go builtins). Each adds to the post-`11`
   registry. They still touch the registry, so they land in number
   order to keep merges clean rather than truly in parallel.
3. **`30`** (bash URL allowlist). Independent of `20`–`22`; depends on
   `12` for the policy hook it plugs into.
4. **`40`** (skill alias/conflict). Independent; touches skill import,
   not the tool registry.
5. **`50`** (MCP design) then its implementation files (added later).
   MCP tools register through the same `11`/`12` surfaces.
6. **`60`** (instrumentation). Independent; touches event emission.
7. **`90`** (closeout). Last.

## File index

| File | Subtask | Kind | Depends on |
| --- | --- | --- | --- |
| `10-tool-security-model.md` | Unified security model | design | — |
| `11-builtin-extras-enum.md` | Extras enum + tool config reshape | code | 10 |
| `12-execution-policy.md` | passby/need_approve/off runtime | code | 11 |
| `20-git-allowlist.md` | `git` inspection | code | 12 |
| `21-artifact-catalog.md` | `artifact_catalog` tool | code | 11 |
| `22-structured-preview.md` | `structured_preview` tool | code | 11 |
| `30-bash-url-allowlist.md` | curl/wget egress opt-in | code | 12 |
| `40-skill-alias-and-conflicts.md` | Skill alias + conflict | code | — |
| `50-mcp-design.md` | MCP support design | design | 10 |
| `60-instrumentation.md` | Audit tag + trace analyzer | code | — |
| `90-closeout.md` | Memo closeout | docs | all |
