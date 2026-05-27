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

1. **Go builtin gaps (first wave, ship immediately).** `git` inspection,
   `artifact_catalog`, `structured_preview`. These are small,
   independent, and have real value now; they build against the
   *current* tool-config surface and are not blocked on the security
   model. See `20`, `21`, `22`.
2. **Bash egress opt-in (first wave).** Opt-in URL allowlist for
   `curl` / `wget`, against the current bash option shape. See `30`.
3. **Unified tool/MCP/skill security model (foundation, in parallel).**
   A single abstraction for what is available (allow/deny) and how it
   runs (`passby` / `need_approve` / `off`), applied uniformly to
   builtin tools, builtin extras, MCP tools, and skills. Split so the
   parts with a real consumer land now and the approval round-trip
   waits for an approver. See `10`, `11`, `12a`, `12b`.
4. **Skill alias and conflict handling.** See `40`.
5. **MCP support.** Its own cycle-magnitude effort; design-first, then
   schedule separately. See `50`.
6. **Instrumentation.** Audit-tagging tool-call events and a trace
   analyzer for future security-design data. See `60`.

### Why this ordering (value-first, not foundation-first)

An earlier draft gated the three small Go-builtin wins behind the
security-model foundation "to avoid re-churning the registration code."
That traded real, immediate value for a minor saving. Corrected: the
first-wave subtasks (`20`–`22`, `30`) ship against the current
`Tools()` / bash-option surfaces. When the foundation (`11`, `12a`)
lands, a small, explicit retrofit re-registers them through the new
config and upgrades `git` destructive subcommands and the URL allowlist
to the policy layer. The retrofit is cheap; the early value is not.

## Goals

- Ship the small Go-builtin wins immediately, decoupled from the
  security foundation.
- Establish one security abstraction that tools, MCP, and skills all
  flow through, with permissive defaults, building only the parts that
  have a real consumer today.
- Keep every code subtask independently verifiable with colocated
  tests.

## How honshu is used (not an engineering gate)

The honshu example (`examples/honshu_groundwater`) is an *observational
UX test*: it shows whether dango's behavior matches user intuition —
what should be surfaced to the user, what the user should be able to
intervene on, and what should stay hidden. The direction is one-way:
dango changes are observed through honshu, and honshu feeds back UX
adjustment opinions. Honshu does not drive dango, and "honshu still
completes" is **not** a regression gate.

Consequences for every subtask below:

- Engineering correctness is proven by Go tests, never by honshu.
- When a subtask changes *user-facing behavior* (a `need_approve`
  pause, an MCP call event, destructive-command gating, a new tool's
  output), it carries a **Honshu observation** note: after the tests
  pass, run honshu to judge whether the right amount is surfaced /
  gated / hidden, and record adjustment opinions. This is a UX signal,
  not a pass/fail check.
- Pure internal refactors with no user-facing change need no honshu
  observation.

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

Two waves run concurrently. The merge hazard (several subtasks touch
`builtin.go` `coreTools` / `Tools(...)`, `builtin_tools.go`,
`skill.go`) is handled not by serializing everything behind the
foundation, but by keeping the first wave small and landing the
foundation's reshape (`11`) as one deliberate retrofit point.

**Wave 1 — immediate value (against current surfaces).**

1. **`20`** — add `git` to the allowlist (read-oriented; no policy
   gating yet).
2. **`21`** — `artifact_catalog`.
3. **`22`** — `structured_preview`.
4. **`30`** — opt-in curl/wget URL allowlist (extract + reject; no
   policy hook yet).

`20`–`22` each touch `coreTools`, so they land in number order to keep
merges clean. None depends on the security model.

**Wave 2 — security foundation (in parallel).**

5. **`10`** (design) → **`11`** (extras enum + tool-config reshape;
   includes a provisional config-struct sketch) → **`12a`** (policy
   data model + `passby`/`off` enforcement + command-pattern
   classification + runner snapshot/adjust). When `11`/`12a` land, a
   small retrofit re-registers wave-1 tools through the new config and
   *records* the `git` destructive patterns as `need_approve` (inert
   until `12b`).
6. **`12b`** — approval round-trip (`need_approve` suspend/event/wait),
   the only thing that turns `need_approve` into real protection.
   **Deferred** until an interactive approver exists (app/cmd cycle);
   there is no consumer for it before then. Decision (b): until `12b`,
   destructive commands have no runtime gate, only prompt guidance.

**Independent (any time).**

7. **`40`** — skill alias/conflict; touches skill import, not the tool
   registry.
8. **`60`** — instrumentation; touches event emission.

**Own cycle.**

9. **`50`** — MCP design; implementation is cycle-magnitude and
   scheduled separately after its design lands.

10. **`90`** — closeout. Last.

## File index

| File | Subtask | Kind | Depends on | Status |
| --- | --- | --- | --- | --- |
| `20-git-allowlist.md` | `git` inspection (allowlist only) | code | — | Delivered (#87) |
| `21-artifact-catalog.md` | `artifact_catalog` tool | code | — | Delivered (#88) |
| `22-structured-preview.md` | `structured_preview` tool | code | — | Delivered (#89) |
| `30-bash-url-allowlist.md` | curl/wget egress opt-in | code | — | Delivered (#94) |
| `10-tool-security-model.md` | Unified security model | design | — | Accepted (consumed by 11/12a) |
| `11-builtin-extras-enum.md` | Extras enum + config contract | code | 10 | Delivered (#92) |
| `12a-policy-enforcement.md` | Policy model + passby/off | code | 11 | Delivered (#93) |
| `12b-approval-flow.md` | `need_approve` round-trip | code | 12a | Deferred — waits for an interactive approver |
| `40-skill-alias-and-conflicts.md` | Skill alias + conflict | code | — | Delivered (#96) |
| `50-mcp-design.md` | MCP support design | design | 10 | Accepted (consumed by 51/52/53) |
| `51-mcp-client.md` | MCP client wrapper | code | 50 | Delivered (#97) |
| `52-mcp-adapter.md` | MCP tool adapter + stream event | code | 51 | Delivered (#97) |
| `53-mcp-config-visibility.md` | MCP global / per-skill visibility | code | 52 | Delivered (#97) |
| `60-instrumentation.md` | Audit tag + trace analyzer | code | — | Delivered (this PR) |
| `90-closeout.md` | Memo closeout | docs | all | Delivered (this PR) |

Retrofit (after `11`/`12a`): re-register `20`–`22` through the new
config; *record* `git push` / `reset --hard` / `clean` / `rebase` as
`need_approve` command patterns (inert until `12b`). The `30` URL
allowlist keeps rejecting unlisted URLs; the softer `need_approve` path
waits for `12b`.

## Where to start (开工顺序)

Solo development with no reference system: do not try to nail every
core API and struct up front. Let the first real PR teach the
interfaces.

1. **Start with `20`** (add `git` to the allowlist). It is roughly a
   half-day, zero-dependency change and the cheapest real signal that
   the plan holds. Ship it, get Go tests green.
2. **Draft `10` in parallel** as a design, but do **not** let the `10`
   design review block `20` or the rest of wave 1.
3. **Let wave-1 learnings settle `11`'s config struct.** The struct
   sketch in `11` is a direction, not a contract; finalize it in code
   once `20`–`22`/`30` have shown what the registration surface
   actually needs. Expect it to change.
4. **Then `11` → `12a`**, doing the wave-1 retrofit in `11`.
5. `40`, `60` whenever convenient; `50` design when ready; `12b` and
   the structural security phase only when their consumers exist.

This order is value-first and learning-first: real code before frozen
abstractions.
