# 30 — Bash Egress Opt-In: `curl` / `wget` URL Allowlist (code, wave 1)

Kind: code. Coverage memo § 2.4. Wave 1 — ships against the current
bash option surface; the policy-layer integration is a retrofit.

**Prerequisite.** None for wave 1. The retrofit step depends on `12a`.

## Decision: keep `curl`, do not build a Go wrapper

Recorded judgment (delegated): `curl` and `wget` stay as bash commands.
We do **not** replace them with a narrow Go-implemented fetch tool.

Rationale:

- Egress risk is controllable without crippling the tool: the opt-in
  URL allowlist (this subtask) plus the `off` policy now, and the
  `need_approve` gate once `12b` lands. A Go wrapper would manage risk
  by amputation instead.
- A Go wrapper permanently lags curl's real surface (auth, headers,
  retries, multipart, resume) and discards the model's existing
  fluency with curl, which contradicts the dango philosophy of
  leveraging reliable automation rather than re-teaching constrained
  tools.

## Goal

Add an opt-in URL allowlist that, when set, restricts `curl` / `wget`
to listed URL prefixes. Default empty = no restriction = current
behavior unchanged.

## Scope

1. In `internal/llm/internal/builtin` (new `url_allowlist.go` if
   cleaner): extend the parsed-AST walker to find `curl` / `wget`
   invocations and extract their target URLs.
2. Add `withBashURLAllowlist([]string)` and a public surface mirroring
   the existing bash option shape. Empty/nil → no restriction.
3. When non-empty, enforce **fail-closed**:
   - Accept positional URL arguments and `--url <u>` whose value is a
     static prefix match of a list entry.
   - Reject when the URL is dynamic (`$VAR`, `$(...)`, backticks) — same
     reasoning as the redirection check.
   - Reject the whole invocation when the URL is not statically
     extractable: `-K`/`-K-` config files, URLs read from stdin, and
     `@file` / `--data-urlencode` URL embedding. The error tells the
     caller to rewrite the command with an explicit URL argument.
   - Following redirects (`-L`) is not statically analyzable and is out
     of scope; document that the allowlist only constrains the initial
     URL.

## Tests (`url_allowlist_test.go`)

- `TestBashURLAllowlistEmptyAllowsAnyURL`.
- `TestBashURLAllowlistAllowsListedURL`, `...RejectsUnlistedURL`.
- `TestBashURLAllowlistRejectsDynamicURL`.
- `TestBashURLAllowlistRejectsConfigFileForm` (`curl -K cfg`).
- `TestBashURLAllowlistAppliesToWget`.

## Retrofit (after `12b`, not `12a`)

A softer path — an unlisted URL resolving to `need_approve` so the user
can wave through a one-off rather than editing config — requires a real
approval gate, which only exists once `12b` ships. Until then the
wave-1 behavior stands: a non-empty allowlist rejects unlisted URLs
outright. Do not wire a `need_approve` URL path against `12a`, because
`12a` does not gate (decision (b)) and it would silently pass.

## Out of scope

- No application to pip / npm / cargo fetches (own resolvers, deferred).
- No default-on enforcement (that is the post-alpha structural phase).

## Honshu observation

The allowlist only changes user-facing behavior when set non-empty
(default is unchanged). When exercised, observe via honshu whether a
blocked URL produces an error the model can recover from gracefully or
a dead end. Record adjustments. UX signal, not a gate.

## Verifiable acceptance

- New and existing tests pass; `go test ./...` green.
- Default-empty leaves current behavior unchanged.
