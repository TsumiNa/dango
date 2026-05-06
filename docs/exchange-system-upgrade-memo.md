# Exchange System Upgrade Memo

Last updated: 2026-05-03

This memo tracks a future PR for evolving exchange documents into a stronger
agent artifact bus.

## Working Model

The stream system is the runtime state bus: it carries compact progress,
lifecycle, status, token, tool, memo, and artifact-reference events.

The exchange system is the generated-artifact bus between orchestrator, runner,
and executor layers. Executors and skills produce exchange markdown documents
with front matter plus `Memo`, `Reasoning`, and `Handoff` sections. Those
documents carry state, summaries, handoffs, and resource references across
agent boundaries.

Together, the two buses should support long-running multi-agent execution with
clear safety boundaries:

- orchestrator-level AI can coordinate work without seeing executor-level core
  skill implementation details;
- orchestrator-level AI can avoid high-privacy input that only executor-level
  skills need to process;
- runner/executor layers can pass durable outputs and summaries upward or
  downstream through explicit exchange documents;
- stream subscribers can observe progress without requiring full artifact
  payloads inline.

## Upgrade Questions

- Should exchange documents get stricter schemas for resources, privacy labels,
  recipient visibility, or redaction policy?
- Should `Memo`, `Reasoning`, and `Handoff` have explicit size, sensitivity, or
  audience rules?
- How should exchange resources express access permissions for downstream
  skills?
- Should exchange documents be indexed separately from stream archives?
- How should orchestrator review consume exchange summaries without accidentally
  reading executor-private data?

## Non-Goals For The Current Branch

- Do not expand the exchange schema beyond the current stream refactor needs.
- Do not add privacy labels or access-control policy yet.
- Do not merge stream persistence and exchange artifact storage into one system.