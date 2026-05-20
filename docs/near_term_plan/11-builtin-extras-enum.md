# 11 — Builtin Extras Enum + Tool Config Reshape (code)

Kind: code. Foundation. Implements the availability axis from `10` and
reshapes the tool-assembly surface so later subtasks register into it.

**Prerequisite.** `10` design accepted.

## Goal

Move builtin extras from stringly-typed `[]string` to a typed enum, and
reshape the `Tools(...)` construction path so availability (allow/deny)
is expressed once, in a typed config, that `12` extends with execution
policy and that `20`/`21`/`22`/MCP register into.

## Scope

1. In `internal/llm/internal/builtin`:
   - Define an exported enum type for builtin extras (for example
     `ExtraTool` with `ExtraListDir`, `ExtraPwd`). Provide
     parse/string helpers for config round-tripping.
   - Replace the `extras []string` parameter of `Tools(...)` with the
     typed form. Unknown values become a compile-time or
     parse-time error rather than a runtime string typo.
   - Keep `coreTools` as the always-available floor; extras remain
     opt-in. Do not change the core set membership here.
2. In `internal/llm/skill.go` and `internal/llm/builtin_tools.go`:
   - Carry the enum through `SkillConfig`, `Skill`, `copy()`, and the
     `builtin.Tools(...)` call site.
   - Update `isBuiltinToolName` to recognize extras by enum-derived
     names.
3. Update existing callers (`demo/skill/main.go`,
   `examples/...`) that set extras to the new enum form.

This subtask does **not** add execution policy yet (that is `12`). It
only reshapes availability into the typed config so `12` can hang the
policy fields off the same struct without a second churn of every call
site.

## Tests

- `internal/llm/internal/builtin/builtin_test.go`:
  - Update `TestToolsAppendsExtras` to the enum form.
  - `TestToolsRejectsUnknownExtra` becomes a typed/parse error test.
- `internal/llm/skill_test.go`:
  - `TestNewSkill_CarriesExtrasEnum`.
- `internal/llm/builtin_tools_test.go`:
  - `TestBuiltinToolsRespectsExtrasEnum`.

## Out of scope

- No execution policy (passby/need_approve/off).
- No MCP. No new tools.

## Verifiable acceptance

- New and existing tests pass; `go test ./...`, `go vet ./...`,
  `go build ./...` green.
- No remaining `[]string` extras path; `grep` shows extras flow through
  the enum only.
