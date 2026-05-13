# dango

dango is a decentralized AI agent framework for long-running scientific research tasks. It breaks complex research work into observable, persistent, handoff-friendly execution units so multiple agents and skills can collaborate around the same research goal instead of forcing all context, tool calls, and intermediate artifacts into a single short-lived conversation.

Scientific research tasks are rarely one-prompt workflows. They often span data cleaning, literature or context gathering, model training, experiment iteration, result review, visualization, and report writing. Each stage has its own tools, environment, artifacts, and quality criteria. dango provides a runtime structure across those stages: it plans work, schedules skills, passes context forward, records process state, exposes progress, and preserves enough traceability for tasks that run for a long time or need to be revisited later.

## Design Goals

- **Decentralized collaboration**: the orchestrator plans a task as runner nodes, and each node binds to an independent skill runtime. Skills own their tool environments and execution details, while the framework handles scheduling, observation, and handoff.
- **Long-running execution**: runners have explicit lifecycle phases, including plan polish, review, replan, execute, report, and settle. This fits research workflows that need multiple rounds of planning, review, and execution.
- **Standardized handoff**: nodes pass memo, reasoning, handoff instructions, and resource declarations through markdown exchange documents so upstream artifacts can be consumed by downstream skills.
- **Observable and replayable state**: request streams, runner streams, event logs, and runner stores record runtime state so external systems can subscribe to progress, query snapshots, and rebuild views from persisted records.
- **Real toolchain support**: skills can own their execution environments, dependencies, and scratch spaces for scripts, model training, visualization, and research artifact generation.

## Use Cases

dango is designed for scientific and engineering research tasks that need multiple specialized agents to collaborate over time, such as:

- extracting structured inputs from messy observations and progressively enriching the surrounding context;
- combining data processing, statistical modeling, visualization, and report generation;
- preserving intermediate memos, resource paths, model outputs, and audit records during execution;
- letting different skills work independently toward the same research objective while continuing through standard handoffs.

## Current Status

This repository is actively evolving around the orchestrator, runner, executor, skill runtime, stream, and persistence layers. The current implementation includes request-level orchestration, runner lifecycle management, skill binding, exchange markdown, event-stream observation, and local persistence.

This README intentionally does not include concrete usage instructions yet. More detailed development and architecture notes are available in `CONTRIBUTING.md` and `architecture/`.
