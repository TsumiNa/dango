# 90 — Near-Term Plan Closeout (docs)

Kind: documentation. Runs after the code subtasks in this folder have
shipped (the MCP implementation files may still be in flight; close out
what is done and note what remains).

## Goal

Record what the near-term plan delivered and confirm no cross-subtask
regressions.

## Scope

1. Update `docs/builtin-tools-coverage-memo.md`:
   - § 2.4 → record the delivered security model (`10`–`12`), URL
     allowlist (`30`), and instrumentation (`60`) PR numbers.
   - § 3.1 → record MCP availability once `50`'s implementation lands.
   - § 3.4 / § 3.5 → record `20` / `21` / `22` PR numbers.
2. Add a short "delivered" entry to
   `docs/deferred-refactor-tracker-memo.md`, mirroring how PR C closed
   out in PR C-6.
3. Add per-subtask "completed" status lines to the relevant files in
   this folder.

## Cross-subtask integration check

Run the honshu example end-to-end with, in one configuration:

- `git` allowlisted and a `git push` confirmed to pause for approval;
- one MCP server mounted (global) with its call event visible on the
  stream and its result absent from the stream;
- a non-empty bash URL allowlist;
- the audit category present on every tool-call event.

This single run proves the security model, Go builtins, MCP, and
instrumentation do not conflict (e.g. no `coreTools` ordering breakage,
no tool-name collision between an MCP tool and a Go builtin).

## Verifiable acceptance

The coverage memo and tracker reflect the delivered work, and the
integration run passes with no regressions.
