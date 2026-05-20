# 11 — Builtin Extras Enum + Tool Config Contract (code)

Kind: code. Foundation. Implements the availability axis from `10`,
defines the tool-config contract every other subtask registers
through, and reshapes the tool-assembly surface.

**Prerequisite.** `10` design accepted.

## Goal

Move builtin extras from stringly-typed `[]string` to a typed enum, and
reshape the `Tools(...)` construction path so availability (allow/deny)
is expressed once, in a typed config, that `12a` extends with execution
policy and that the wave-1 tools (`20`/`21`/`22`/`30`) and MCP register
into.

Wave-1 tools ship *before* this subtask against the current `Tools()`
signature. This subtask therefore includes a **retrofit step**:
re-register `21`/`22`'s tools and `20`'s git allowlist entry through the
new config in the same PR, so there is exactly one reshape, not a
trickle of churn.

## Config-struct contract sketch

This is the contract the whole near-term plan depends on, so pin its
shape here (names are provisional; finalize in code review). It must
hold availability now and have a clean slot for `12a`'s policy without
another call-site churn.

```go
// ToolSetConfig is the single input that determines which capabilities
// a skill's LLM sees and (after 12a) how each runs.
type ToolSetConfig struct {
    // Builtin core is always present and not represented here.

    // Extras: typed, opt-in. Replaces the old []string.
    Extras []ExtraTool

    // Bash knobs carried through unchanged from today.
    BashAllow []string
    BashBlock []string

    // 12a hangs execution policy off the same struct, e.g.:
    //   Policies map[CapabilityRef]ExecPolicy  // passby (default) / need_approve / off
    //   BashCommandPolicies []CommandPattern   // git push -> need_approve, etc.
    // 11 leaves these absent; 12a adds them without reshaping callers.
}
```

The key requirement: adding the `12a` policy fields must be an additive
change to this struct, not a new parameter on `Tools(...)`.

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
4. **Retrofit wave-1 tools.** Re-register `21` (`artifact_catalog`),
   `22` (`structured_preview`), and `20`'s `git` allowlist entry
   through the new `ToolSetConfig`. This consolidates the reshape into
   one PR instead of leaving wave-1 tools on the old surface.

This subtask does **not** add execution policy yet (that is `12a`). It
only reshapes availability into the typed config so `12a` can hang the
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
