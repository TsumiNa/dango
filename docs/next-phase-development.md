# Next Phase Development Plan

This document records the next-phase development priorities for dango. It intentionally treats dango as a contract-first coordination and evaluation kernel for long-running scientific agent workflows, not as a general-purpose agent framework competing with community graph or workflow runtimes.

## Positioning

dango should focus on the parts that are not already solved well by general agent frameworks:

- contract-first agent-to-agent handoff;
- structured whiteboard state for scientific work;
- constrained context projection and tool/resource boundaries;
- replayable execution records for evaluation and audit;
- failure-to-intervention traces for long-running research tasks.

The project should avoid expanding into a generic graph builder, provider abstraction layer, UI platform, or large bundled tool ecosystem unless those features directly support the research and evaluation goals above.

## Priority 0: Finish the research kernel

These items are the highest-priority work because they turn the existing runtime into a reproducible evaluation platform.

### 1. Contract schema v1

Define a machine-validatable handoff contract for runner nodes and skills. The current exchange and handoff markdown documents already provide the right data-plane shape; the next step is to make trusted handoff explicit and testable.

Minimum fields:

```yaml
kind: handoff
version:
runner_id:
from_node:
to_nodes:
intent:
task_objective:
allowed_tools:
forbidden_tools:
allowed_resource_roots:
expected_artifacts:
evidence_required:
uncertainty_required:
failure_schema:
human_review_required:
created_at:
````

Why this matters:

* separates dango from ordinary chat-style handoff;
* makes downstream node responsibilities explicit;
* enables validation, replay, and evaluation;
* provides the foundation for tool and data boundary enforcement.

Suggested next steps:

* add a versioned contract schema;
* add validation before node execution;
* record validation failures as structured runner events;
* add tests for missing fields, invalid tool scope, and invalid artifact declarations.

### 2. Whiteboard schema v1

Clarify the semantics of exchange, memo, and handoff documents as a structured scientific whiteboard rather than generic memory.

Minimum sections:

```yaml
objective:
constraints:
assumptions:
decisions:
evidence:
artifacts:
failures:
invalidated_claims:
open_questions:
next_actions:
human_feedback:
```

Why this matters:

* supports task resumption;
* preserves evidence and decision history;
* reduces goal and context drift;
* makes scientific workflow state inspectable and reusable.

Suggested next steps:

* document the whiteboard model in `architecture/` or `docs/`;
* map existing markdown channel documents to whiteboard sections;
* define which sections are writable by orchestrator, runner, agent, and skill stages;
* add a minimal parser or normalizer if needed.

### 3. Run bundle export

Add a single export artifact for each completed or interrupted request so runs are easy to reproduce, inspect, and evaluate.

Suggested bundle layout:

```text
request.json
plan.json
runner_records.jsonl
request_event_log.jsonl
exchange/
handoff/
memo_archive/
artifacts/
skill_registry_snapshot.json
tool_registry_snapshot.json
metrics.json
```

Why this matters:

* enables benchmark-style evaluation;
* makes results shareable in papers and reports;
* separates live observation from replay and audit;
* provides a stable input for external evaluators.

Suggested next steps:

* implement `export-run` or an equivalent API endpoint;
* include enough metadata to reproduce the run configuration;
* document which sensitive fields may need redaction before sharing.

### 4. Minimal evaluation harness

Every run should be able to emit basic metrics. Keep the first evaluator simple and framework-level rather than task-specific.

Initial metrics:

1. task success;
2. node success;
3. invalid handoff rate;
4. inappropriate tool call rate;
5. evidence completeness;
6. artifact completeness;
7. replan or rerun count;
8. cost: token usage, tool calls, wall time, and agent messages.

Why this matters:

* turns dango from a runtime into a research testbed;
* supports centralized versus semi-decentralized comparisons;
* supports clean versus noisy tool-environment experiments;
* keeps development grounded in observable behavior.

Suggested next steps:

* define a metrics schema;
* compute framework-level metrics from existing stream and persistence records;
* allow task-specific evaluators to add extra fields later.

### 5. Context projection and resource boundary

Make per-node visibility and resource scope explicit. Each node should receive only the whiteboard projection, artifacts, and tools allowed by its contract.

Minimum boundary controls:

```yaml
visible_exchange:
visible_handoffs:
visible_artifacts:
allowed_dirs:
allowed_tools:
forbidden_tools:
redacted_fields:
```

Why this matters:

* prevents unnecessary context leakage;
* makes data and tool boundaries inspectable;
* supports trusted handoff;
* provides a practical safety boundary for scientific and enterprise settings.

Suggested next steps:

* add a context projection step before node execution;
* record the projection metadata in the run bundle;
* test that forbidden artifacts and tools are not visible to the target node.

### 6. Stable review, replan, rerun, and reject protocol

The runner lifecycle already includes review and replan phases. The next step is to standardize user and orchestrator interventions as explicit intents.

Suggested intent vocabulary:

```yaml
intent:
  - review
  - continue
  - summarize
  - rerun_previous
  - revise_constraints
  - request_human_review
  - reject_handoff
```

Why this matters:

* makes long-running task control predictable;
* enables recovery from poor handoff or execution;
* supports experiments on task resumption and drift;
* avoids hiding important control behavior inside prompts.

Suggested next steps:

* formalize allowed lifecycle interventions;
* record all interventions as structured events;
* add tests for rerun and reject behavior.

### 7. User-facing quickstart and canonical example

Add one reliable quickstart and one canonical scientific workflow example. The goal is not broad documentation coverage; the goal is to make the current kernel runnable and reviewable.

Suggested quickstart shape:

```bash
dango serve --port 8080
dango run examples/honshu_groundwater/request.yaml
dango describe <request_id>
dango export-run <request_id> ./runs/demo
```

Why this matters:

* lets new users validate the project quickly;
* turns the existing example into a stable regression target;
* makes the project easier to evaluate for grants, papers, and collaborators.

Suggested next steps:

* keep README user-facing;
* keep developer details in CONTRIBUTING and architecture docs;
* avoid documenting placeholder commands as supported workflows until implemented.

## Priority 1: Support the core experiments

These items are important after the kernel can export and evaluate runs.

### 1. Centralized baseline mode

Add a configuration mode that approximates a centralized orchestrator baseline. This does not need to be a separate product feature; it exists to support architectural comparison.

Why this matters:

* enables credible evaluation of dango-native semi-decentralized execution;
* avoids comparing against an undefined baseline;
* supports papers and internal research reports.

### 2. Clean and noisy tool-registry modes

Support task-relevant-only and noisy tool environments.

Suggested shape:

```yaml
tool_environment:
  mode: noisy
  relevant_tools:
  irrelevant_tools:
  decoy_tools:
```

Why this matters:

* tests tool-noise robustness;
* reflects realistic scientific platforms with many available tools;
* supports experiments on contract-scoped allowed tools.

### 3. Minimal external skill SDK

Provide the smallest possible way to connect external scientific scripts, models, or tools.

Possible structure:

```text
skill.yaml
run.sh
input_schema.json
output_schema.json
```

Why this matters:

* lets scientific users attach existing tools without learning dango internals;
* keeps dango focused on coordination rather than bundled tools;
* supports property prediction, structure generation, and materials-analysis wrappers.

### 4. Scientific task adapters

Once the SDK and evaluation bundle are stable, add a small number of canonical scientific task adapters.

Recommended first adapters:

* literature and evidence-collection task;
* MatTools or pymatgen-style material tool task;
* property-prediction skill wrapper;
* crystal-structure or artifact-processing skill wrapper.

## Deprioritized work

The following items should not receive major investment until the research kernel and evaluation workflow are stable.

### 1. Generic graph DSL or visual workflow editor

Reason: this competes directly with existing graph and workflow frameworks and does not strengthen dango's contract-first scientific coordination value.

Recommendation: keep simple describe or visualization output for debugging, but do not build a full graph editor.

### 2. Broad LLM provider support

Reason: provider abstraction consumes engineering time while adding little to the core research question.

Recommendation: keep one stable backend first. Add adapters later only when required by experiments or users.

### 3. Complex UI or dashboard

Reason: run export, logs, and evaluation records matter more than visual polish at this stage.

Recommendation: keep terminal or API-based observation until the evaluation workflow is mature.

### 4. Large built-in tool library

Reason: dango should coordinate tools, not become a tool marketplace.

Recommendation: ship only a few canonical tools or skills needed for examples and tests.

### 5. Enterprise multi-tenant permission system

Reason: data boundaries matter, but full enterprise RBAC, billing, and organization management are premature.

Recommendation: implement local-first resource boundaries, allowed tools, artifact visibility, and redaction controls first.

### 6. Full adapters for every agent framework

Reason: adapters can become an open-ended maintenance burden.

Recommendation: use external frameworks as baselines or execution backends only where necessary. Keep dango's protocol layer runtime-neutral.

### 7. VLA or autonomous laboratory interfaces

Reason: these are important future targets but will distract from the current scientific-agent evaluation kernel.

Recommendation: keep them as roadmap items and ensure today's contract and whiteboard abstractions can support them later.

### 8. Complex vector memory or RAG subsystem

Reason: dango's distinctive memory model should be the structured whiteboard. Generic RAG should remain a skill or tool, not part of the runtime core.

Recommendation: avoid turning dango into another memory-heavy agent framework.

## Suggested development sequence

### Phase 1: Make the runtime evaluable

* contract schema v1;
* whiteboard schema v1;
* run bundle export;
* minimal metrics;
* canonical quickstart.

### Phase 2: Enable architectural experiments

* centralized baseline mode;
* clean versus noisy tool registry;
* context projection and resource boundary enforcement;
* stable review, replan, rerun, and reject protocol.

### Phase 3: Add scientific task coverage

* minimal external skill SDK;
* MatTools or pymatgen task wrapper;
* property prediction skill wrapper;
* crystal-structure or artifact-processing skill wrapper;
* small benchmark scripts.

## Guiding principle

Do not optimize dango to be a general-purpose agent framework. Optimize it to answer a narrower and more valuable question:

> Under what conditions can contract-first, whiteboard-mediated, semi-decentralized coordination make long-running scientific agent workflows more stable, auditable, and recoverable?
