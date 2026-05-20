# 60 — Audit Tagging + Trace Analyzer (code)

Kind: code. Coverage memo § 2.4. Two related instrumentation
deliverables; they may ship as one PR or split if they grow.

**Prerequisite.** None for the audit tag. The analyzer depends on the
audit tag landing first.

## 60a — Audit-tag the tool-call stream events

**Goal.** Mark `llm.tool_call.started` / `llm.tool_call.completed` as
the canonical audit-log source so the post-alpha hardening phase relies
on a stable schema instead of a separate audit pipeline.

**Scope.**

1. Locate tool-call event emission in the llm runtime (PR C-3 cited
   `llm.tool_call.started`).
2. Add a stable `category: "audit"` field (final name chosen in
   implementation) and confirm the event carries: tool name, argument
   summary, result summary (truncated to a documented cap), timestamps,
   skill ID, request/runner IDs. Add any missing field needed to make
   the event self-contained.
3. Write the audit schema to `docs/tool-call-audit-schema.md` (field
   list, types, truncation rules) so downstream consumers have a
   stability contract.

**Tests.**

- `TestToolCallStartedEventCarriesAuditCategory`.
- `TestToolCallCompletedEventCarriesAuditCategory`.
- `TestToolCallEventTruncatesLargeArguments`.

## 60b — Trace-analysis utility

**Goal.** Automate the PR C-3 manual analysis so each example run
produces the dataset the post-alpha hardening phase needs.

**Scope.**

1. Add a Go program (default `tools/analyze-tool-traces/main.go`)
   consuming `artifacts/debug/stream_events.jsonl`, emitting a markdown
   report plus a JSON sidecar.
2. Report: bash command-head distribution, captured inner-command
   bodies of Turing-complete heads (`python -c`, `bash -c`,
   `xargs <cmd>`, `make`, `awk` system-calls), per-skill tallies, and
   `curl` / `wget` URL frequencies.
3. Add a `make analyze-traces` entrypoint.
4. This utility **supersedes** the hand-rolled methodology in
   `docs/builtin-tools-restructure-plan.md` § "PR C-3 results"; its
   output must reproduce those numbers when run on the same artifact.

**Tests.**

- `TestAnalyzerSummarizesBashHeads` (fixture jsonl).
- `TestAnalyzerCapturesInnerCommandBodies`.
- `TestAnalyzerCountsURLsByHost`.

## Out of scope

- No separate audit storage, no dashboards, no automatic gating.

## Verifiable acceptance

- Tests pass; `go test ./...` green.
- A fresh `stream_events.jsonl` shows the audit category on every
  tool-call event; the analyzer reproduces the PR C-3 findings on the
  C-3 sample artifact.
