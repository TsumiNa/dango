# 90 — Near-Term Plan Closeout (docs)

Kind: documentation. Runs after the code subtasks in this folder have
shipped (the MCP implementation files may still be in flight; close out
what is done and note what remains).

## Goal

Record what the near-term plan delivered and confirm no cross-subtask
regressions.

## Scope

1. Update `docs/builtin-tools-coverage-memo.md`:
   - § 2.4 → record the delivered security model (`10`–`12a`), URL
     allowlist (`30`), and instrumentation (`60`) PR numbers.
   - § 3.1 → record MCP availability once `50`'s implementation lands.
   - § 3.4 / § 3.5 → record `20` / `21` / `22` PR numbers.
2. Add a short "delivered" entry to
   `docs/deferred-refactor-tracker-memo.md`, mirroring how PR C closed
   out in PR C-6.
3. Add per-subtask "completed" status lines to the relevant files in
   this folder.

## Cross-subtask integration check

Two separable checks, kept distinct on purpose.

**Engineering (a real gate).** A Go-level integration test (or a
scripted run asserting on exit status and emitted events) confirms the
delivered subtasks compose without conflict: no `coreTools` ordering
breakage, no tool-name collision between an MCP tool and a Go builtin,
the audit category present on tool-call events, a non-empty URL
allowlist enforced. This is pass/fail.

**Honshu observation (UX signal, not a gate).** Run the honshu example
with the new capabilities active — `git` available, an MCP server
mounted, the security policy in effect — and observe whether the
composite behavior matches user intuition: is MCP activity surfaced at
the right level, does the `12a` `need_approve` interim signal read as
useful or noisy, do the new tool outputs help. Record adjustment
opinions and feed them back into the relevant subtask files. Honshu
does not pass or fail this closeout; it tunes it.

## Verifiable acceptance

The coverage memo and tracker reflect the delivered work, the
engineering integration check passes, and the honshu observation has
been run with its adjustment opinions recorded (whatever they are).
