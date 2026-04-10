# Dango Architecture

This document describes the intended control-plane and execution-plane split for dango.

## Goals

- Keep the orchestrator focused on request intake, task lifecycle APIs, and persisted control state.
- Move execution ownership into a runner subsystem that can continue independently after a task starts.
- Make task state observable through persisted task rows, append-only events, edge records, handoff files, and output artifacts.
- Preserve append-only task history so edits after execution start happen through clone-and-revise flows rather than in-place mutation.

## Target Flow

1. A client submits a request to the orchestrator API.
2. The orchestrator normalizes the request, persists the initial task record, and starts or resumes a runner.
3. After the runner is started, the orchestrator stops coordinating step-by-step execution and only exposes control operations such as describe, list, cancel, resume, and clone.
4. The runner owns planning, review, dispatch, executor supervision, and task finalization.
5. Executors report progress through runner-managed status transitions and persisted artifacts rather than direct orchestrator callbacks.

## Package Boundaries

### `internal/orchestrator`

The orchestrator package is the control plane.

- HTTP server and request normalization
- task-oriented APIs and control operations
- request metadata capture
- registry access
- persisted task descriptions for clients

The orchestrator should not contain step-by-step execution mechanics once a runner has been started.

### `internal/runner`

The runner package is the execution plane.

- runner service and background lifecycle management
- execution scheduler and edge dispatch
- executor supervision and status collection
- transition from plan review into execution
- future channel-driven event loop for concurrent edge progress

The runner package should eventually own the code that currently lives in task runner and scheduler implementations.

### `internal/executor`

The executor package is the worker contract.

- planning mode
- execution mode
- public and private handoff/output generation

### `internal/runtime`

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
- the scheduler and execution state machine live under `internal/runner`
- the planner prompt is stored in Markdown and the top-level draft planner is LLM-backed
- server-side task starts are asynchronous for run-style APIs, while the blocking `RunNow` path remains for tests and CLI workflows

Target:

- `internal/runner` owns execution concerns
- task start defaults to background execution rather than synchronous request blocking
- runner internals can supervise executors through a channel-driven state loop
- orchestrator becomes a pure control plane over persisted runner state

## Incremental Refactor Plan

1. Extract the scheduler into `internal/runner`.
2. Update call sites so execution concerns depend on the runner package instead of `internal/orchestrator`.
3. Extract task runner and runner service into `internal/runner` after replacing direct dependencies on orchestrator-local types with explicit interfaces or shared state types.
4. Replace synchronous `RunNow`-style flows with background runner startup for normal task execution.
5. Introduce internal runner event channels for executor state updates and readiness signaling.

## Immediate Refactor Rule

When a file is primarily responsible for task execution, edge dispatch, executor supervision, or runtime coordination, it belongs in the runner package rather than the orchestrator package.