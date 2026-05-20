# 10 — Unified Tool / MCP / Skill Security Model (design)

Kind: design doc. No code ships in this subtask; it defines the model
that `11`, `12`, `20`, `30`, and the MCP implementation all build on.

## Goal

Replace the ad-hoc, per-mechanism controls (bash allowlist,
`BuiltinExtras []string`, no MCP gate, no skill gate) with one
abstraction that answers two orthogonal questions for every executable
capability — builtin tool, builtin extra, MCP tool, or skill:

1. **Availability** — is the capability mounted and offerable to the
   model at all?
2. **Execution policy** — when the model calls it, does it run
   automatically, require user approval, or stay disabled?

The model must keep the dango philosophy: permissive defaults, no extra
user work on the happy path, control surfaces available when wanted.

## The two axes

### Axis 1 — Availability (allow/deny)

| Capability class | Availability rule |
| --- | --- |
| Builtin (core) | Always available. Cannot be denied. Matches today. |
| Builtin extra | Allow/deny list. Default deny (opt-in), matching today's `BuiltinExtras`. Representation moves from `[]string` to an enum (see `11`). |
| MCP tool | Allow/deny per server and optionally per tool. Default available once the server is mounted. |
| Skill | Allow/deny list (on/off). Default on. This replaces the previously proposed `TrustedInput` flag — there is no separate trust flag; a skill the user mounted is trusted by default, consistent with the philosophy. |

Builtin immutability is deliberate: the core set is the floor every
skill can rely on.

### Axis 2 — Execution policy (`passby` / `need_approve` / `off`)

Every *available* capability carries an execution policy:

- **`passby`** — runs automatically when the model calls it. Default
  for all builtins, builtin extras, MCP tools, and skills.
- **`need_approve`** — the call is held; an approval request is
  published to the stream (see `12`) and the call proceeds only after
  the top-level caller approves. Used for destructive or
  irreversible operations.
- **`off`** — the capability is mounted/visible but calls are rejected.
  Useful for temporarily disabling something at runtime without
  un-mounting it.

Defaults encode "permissive but not reckless":

- Builtin / builtin extra / MCP / skill → `passby`.
- A small set of **destructive command patterns** inside `bash` →
  `need_approve` (see below). This is how `git push`, `git reset
  --hard`, `git clean`, `git rebase`, etc. are gated without a
  `bashBlock`.

### Sub-tool granularity for bash

`bash` is a single tool but executes arbitrary commands, so a tool-level
policy is too coarse. The model adds a **command-pattern policy list**:
ordered (command-head, optional subcommand/flag predicate) → policy.
Examples seeded by later subtasks:

- `git push` → `need_approve`
- `git reset --hard` → `need_approve`
- `git clean` → `need_approve`
- `git rebase` → `need_approve`

The bash tool consults this list after the existing allowlist and
redirection checks. A match of `need_approve` triggers the same
approval flow as a tool-level `need_approve`. No match → `passby`.

## Runner snapshot and dynamic adjustment

- At runner initialization, the runner **copies** the current preset of
  the availability lists and execution policies into a per-run policy
  set. The preset comes from the app/cmd configuration.
- During the run, the top-level caller may **adjust** the per-run policy
  set (flip a tool to `off`, downgrade a `need_approve` to `passby` for
  the rest of the session, etc.). Adjustments affect only the running
  copy, never the app/cmd preset.
- The adjustment surface is an explicit API on the runner; it is not a
  config mutation. Design the API shape here; `12` implements it.

## MCP-specific posture

- App/cmd-startup MCP servers are **global**: visible to every skill.
- A user mounting their own skills may additionally declare
  **per-skill** MCP servers visible only to that skill. See `50`.
- Risk for user-supplied MCP servers is the user's to own. Dango does
  not attempt to sandbox them. At app/cmd startup, the runtime prints
  the mounted MCP servers and a one-line risk notice; default policy is
  `passby`. Users who want a gate set the server (or specific tools) to
  `need_approve` or `off`.

## Approval flow (design only; `12` implements)

`need_approve` must work for an autonomous, possibly headless run:

- The call is suspended and an approval-request event is published on
  the relevant stream with enough context (capability name, argument
  summary) for the top-level caller to decide.
- The caller responds (approve / deny, optionally "approve for the rest
  of the session" which downgrades the policy to `passby`).
- A denied call returns a typed error the model can react to.
- Timeout behavior and headless defaults (deny vs hold) are an open
  question for `12`; this design only fixes the event contract.

## Open questions (resolve during `11`/`12`, not now)

- Exact enum names and Go types for availability and policy.
- Whether per-MCP-tool availability is needed in v1 or per-server is
  enough.
- Headless `need_approve` default (hold vs auto-deny) and timeout.
- Whether skill on/off lives in app/cmd config, `SkillConfig`, or both.

## Verifiable acceptance

This file exists and pins: the two axes, the default policies, the
bash command-pattern mechanism, the runner snapshot + dynamic-adjust
contract, the MCP posture, and the approval event contract. Subtasks
`11` and `12` cite this file.
