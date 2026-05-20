# 40 — Skill Alias and Name-Conflict Handling (code)

Kind: code.

**Prerequisite.** None. Touches skill import, not the tool registry, so
it can run in parallel with the other tracks.

## Goal

Let a user assign an alias to a mounted skill, and detect name
conflicts when the user's own skills collide with dango-provided (or
other already-mounted) skills. On conflict, warn and recommend an
alias; if the user does not alias, the user's own skill wins.

This keeps the dango philosophy: the conflict resolves automatically in
the user's favor without forcing the user to act, while still surfacing
the collision so they can alias if they want both.

## Scope

1. **Alias support.** Add an alias field to the skill mount/config so a
   skill can be registered under a name other than its intrinsic name.
   The alias is the name the orchestrator and other skills route to.
2. **Conflict detection at mount time.** When assembling the mounted
   skill set, detect duplicate effective names (intrinsic name or
   alias). Detection runs once at startup/import, not per task.
3. **Resolution policy on an unaliased conflict:**
   - Emit a warning that names the colliding skills and recommends
     assigning an alias to disambiguate.
   - Resolve in favor of the **user-imported** skill (user-supplied
     wins over dango/app-provided). Document the precedence so it is
     predictable.
   - If both colliding skills are user-imported (no clear precedence),
     this is an error the user must resolve with an alias — fail at
     mount with a clear message rather than guessing.
4. **Aliased case.** When an alias removes the collision, both skills
   are mounted under distinct names with no warning.

## Tests

- `TestSkillMountAliasRoutesUnderAlias`.
- `TestSkillConflictPrefersUserSkillAndWarns`.
- `TestSkillConflictBothUserSuppliedIsError`.
- `TestSkillAliasResolvesConflictNoWarning`.

## Out of scope

- No alias for tools or MCP (those use the `10` availability lists).
- No runtime re-aliasing after mount.

## Verifiable acceptance

- New and existing tests pass; `go test ./...` green.
- Mounting two skills with the same name (one user, one provided)
  produces the documented warning and routes to the user's skill;
  adding an alias silences the warning and mounts both.
