# Dango Architecture

This document describes the intended control-plane and execution-plane split for dango.

## Goals

- Keep the orchestrator focused on request intake, task lifecycle APIs, and persisted control state.
- Move execution ownership into a runner subsystem that can continue independently after a task starts.
- Treat hooks as the stable abstraction boundary for intent understanding, planning, review, repair, and execution-time generation.
- Ship built-in AI capabilities as first-class hook implementations rather than treating them as external add-ons.
- Make task state observable through persisted task rows, append-only events, edge records, handoff files, and output artifacts.
- Preserve append-only task history so edits after execution start happen through clone-and-revise flows rather than in-place mutation.

## Target Flow

1. A client submits a request to the orchestrator API.
2. The orchestrator invokes its intent-understanding hooks, normalizes the request, persists the initial task record, and starts or resumes a runner.
3. After the runner is started, the orchestrator stops coordinating step-by-step execution and only exposes control operations such as describe, list, cancel, resume, and clone.
4. The runner owns draft planning, review, repair, dispatch, executor supervision, and task finalization.
5. Executors perform stage-local detail planning and execute-time generation through executor hooks, then report progress through runner-managed status transitions and persisted artifacts rather than direct orchestrator callbacks.

## Package Boundaries

### `internal/orchestrator`

The orchestrator package is the control plane.

- HTTP server and request normalization
- intent understanding via AI-backed hooks
- task-oriented APIs and control operations
- request metadata capture
- registry access
- persisted task descriptions for clients

The orchestrator should not contain step-by-step execution mechanics once a runner has been started.

### `internal/runner`

The runner package is the execution plane.

- runner service and background lifecycle management
- draft, review, and repair planning via AI-backed hooks
- execution scheduler and edge dispatch
- executor supervision and status collection
- transition from plan review into execution
- future channel-driven event loop for concurrent edge progress

The runner package should eventually own the code that currently lives in task runner and scheduler implementations.

### `internal/executor`

The executor package is the worker contract.

- stage-local detail planning via AI-backed hooks
- execution mode and execute-time script or glue-code generation
- public and private handoff/output generation

### `internal/runner/runtime`

The runtime package is the transport between runner and executors.

- host runtime
- container runtime
- future long-running execution handles if the runner becomes fully event-driven

### `internal/store/sqlite`

The store package remains the durable state layer.

- task rows
- edge rows
- logs
- append-only event and metadata files via locator-backed artifacts

## AI Hook Model

Hooks are the abstraction boundary for module-specific intelligence and generation.

- A hook may be implemented by built-in AI flows or future external override logic.
- Deterministic code may validate inputs, route control flow, or explain why a task cannot proceed, but it should not fabricate dynamic plans or generated execution outputs for this system.
- LLM planners, VLM-assisted intent understanding, and code-generation executors are concrete hook implementations, not a separate architectural tier.
- The caller for a stage should request a structured result from the relevant hook rather than embedding model-specific prompting logic everywhere in the call graph.
- Hook contracts should prefer structured outputs that can be validated, persisted, and replayed.

## Failure Semantics

This system is dynamic. It does not have a meaningful deterministic planning path.

- If an intent-understanding, planning, review, repair, detail-planning, or execute-generation hook cannot produce a valid result, the deterministic fallback is to stop and explain why the task cannot proceed.
- Deterministic fallback must not synthesize a fake executable plan, placeholder executor detail plan, or scaffold output that pretends the task succeeded.
- Failure explanations should be structured enough to persist in task metadata, events, logs, and user-visible result artifacts.
- Recoverable retries may still occur through later hook invocations or clone-and-revise flows, but not through invented deterministic plans.

## Built-In AI Engine

Dango should ship with built-in AI capabilities for the core control and execution flow.

- Built-in means the repository owns the AI engine integration, function-calling or tool-calling behavior, output validation, and default prompts.
- The orchestrator uses the built-in AI engine for intent understanding and request normalization.
- The runner uses the built-in AI engine for draft plan creation, plan review, and plan repair or fix-up.
- The executor uses the built-in AI engine for stage-local detail planning and execute-time generation of scripts or glue code.
- Default prompt assets for these built-in AI hooks live under `internal/prompts/`.
- If the built-in AI engine is unavailable or returns an invalid result, the default outcome is an explanatory failure, not a deterministic pseudo-plan.

## External Instruction Overrides

User-provided instructions should be modeled as external hook inputs or overrides rather than as the core built-in engine.

- External instructions are expected to be Markdown documents with YAML frontmatter, similar in spirit to repository instruction files and skill-style guidance.
- Their purpose is to adjust or replace the default directives used by orchestrator, runner, or executor AI hooks.
- The exact folder layout, discovery rules, precedence, and schema are intentionally deferred for a later refactor.
- Current refactors should only preserve clear injection points so these external instructions can be layered onto built-in prompts without redesigning the architecture again.

## Synchronization Model

The intended synchronization model is persistence-first.

- The orchestrator reads task status from durable state.
- The runner writes task, edge, log, and handoff updates as execution progresses.
- Executors communicate completion and outputs through filesystem artifacts and runtime exit status.
- Future non-blocking execution should be coordinated inside the runner through channels and select loops, not through the orchestrator HTTP layer.

This means the orchestrator should not need in-memory ownership of an executing DAG beyond starting, canceling, or resuming a runner handle.

## Current State Versus Target State

Today:

- request intake and task control already live in the orchestrator package
- task and edge state is persisted to SQLite and task directories
- runner-owned planning, the scheduler, and the execution state machine live under `internal/runner`
- built-in prompt assets for orchestrator intent understanding, runner planning, and executor AI live under `internal/prompts/`
- executor plan and run first try tool hooks and then fall back to built-in AI detail planning or execute-generation before failing with explanatory cannot-proceed semantics
- server-side task starts are asynchronous for run-style APIs, while the blocking `RunNow` path remains for tests and CLI workflows

Target:

- `internal/runner` owns execution concerns
- built-in AI hooks cover orchestrator intent understanding, runner draft or review or fix planning, and executor detail planning plus execute-time generation
- module prompts are repository-owned assets, with later support for external instruction overrides layered on top
- when hook output is unavailable or invalid, the system fails the task with an explanation instead of manufacturing deterministic plans or placeholder execution artifacts
- task start defaults to background execution rather than synchronous request blocking
- runner internals can supervise executors through a channel-driven state loop
- orchestrator becomes a pure control plane over persisted runner state

## Incremental Refactor Plan

1. Extract the scheduler into `internal/runner`.
2. Update call sites so execution concerns depend on the runner package instead of `internal/orchestrator`.
3. Extract task runner and runner service into `internal/runner` after replacing direct dependencies on orchestrator-local types with explicit interfaces or shared state types.
4. Replace synchronous `RunNow`-style flows with background runner startup for normal task execution.
5. Introduce internal runner event channels for executor state updates and readiness signaling.
6. Introduce shared AI hook interfaces and default module prompts for orchestrator, runner, and executor responsibilities.
7. Replace executor planning and execution fallbacks with built-in AI detail planning and execute-time code generation while keeping hook override points intact.
8. Add instruction injection points for user-provided Markdown overrides without locking in a final discovery layout too early.
9. Remove any remaining deterministic pseudo-plan or scaffold-success paths and replace them with structured cannot-proceed failure reporting.

## Immediate Refactor Rule

When a file is primarily responsible for task execution, edge dispatch, executor supervision, or runtime coordination, it belongs in the runner package rather than the orchestrator package.