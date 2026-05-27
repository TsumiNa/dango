# dango

dango is a **contract-first coordination kernel for long-running scientific agents**.

It is not a general-purpose agent framework, graph builder, or workflow automation SDK. dango focuses on a narrower problem: how to make multi-agent scientific workflows **handoff-safe, stateful, auditable, recoverable, and evaluable** when tasks span multiple tools, skills, artifacts, and rounds of human review.

Scientific research tasks are rarely one-shot conversations. They often involve literature or context gathering, data processing, model execution, artifact generation, result review, failure recovery, and report writing. Each stage may use different tools, data formats, assumptions, and quality criteria. dango provides a runtime structure for coordinating these stages without forcing all context, tool calls, and intermediate artifacts into one monolithic agent conversation.

## Why dango exists

Most agent frameworks make it easy to build common agent workflows: tool calling, graph routing, chat-style handoff, supervisor-agent patterns, or fixed state-machine execution.

dango is built for a different question:

> Under what conditions can contract-first, whiteboard-mediated, semi-decentralized coordination make long-running scientific agent workflows more stable, auditable, and recoverable?

The core design goal is not to hide complexity behind a convenient interface. The goal is to expose the right control surfaces for research workflows:

- what each agent is allowed to see;
- which tools and resources it may use;
- what artifacts it is expected to produce;
- what evidence and uncertainty it must report;
- how handoff failures are detected;
- how intermediate state is preserved and replayed;
- how runs can be evaluated after execution.

## Core ideas

### Contract-first handoff

In dango, handoff is not just a message from one agent to another. A handoff should be treated as a verifiable contract.

A handoff contract may specify:

- task objective;
- upstream context;
- allowed and forbidden tools;
- allowed resource roots;
- expected artifacts;
- evidence requirements;
- uncertainty requirements;
- failure reporting schema;
- human review requirements.

This makes downstream execution explicit, inspectable, and testable.

### Whiteboard state, not generic memory

dango does not treat long-term state as ordinary chat history or vector memory.

Instead, it organizes workflow state through structured channel documents such as exchange, handoff, and memo records. These records act as a scientific whiteboard for:

- objectives;
- constraints;
- assumptions;
- decisions;
- evidence;
- artifacts;
- failures;
- invalidated claims;
- open questions;
- next actions;
- human feedback.

The whiteboard is designed to support task resumption, evidence tracing, and post-hoc evaluation.

### Semi-decentralized execution

dango separates request-level orchestration from runner-owned execution and skill-owned tool environments.

The orchestrator plans and manages request-level progress. Runners own execution lifecycles. Agents act as one-to-one proxies for skill runtimes. Skills own their prompts, tool environments, scratch spaces, accessible directories, and execution details.

This structure avoids forcing every tool call, artifact, and intermediate decision through a single long-lived conversation.

### Observable and replayable runs

dango records request streams, runner streams, event logs, runner records, exchange documents, handoff documents, memos, and artifacts so that executions can be observed live and inspected later.

The target use case is not only “make the agent finish the task,” but also:

- reconstruct what happened;
- inspect why a handoff occurred;
- detect where a task drifted;
- compare centralized and semi-decentralized execution;
- evaluate tool selection, evidence completeness, recovery behavior, and cost.

### Tool and resource boundaries

Scientific workflows often involve private data, local files, model outputs, scripts, and external tools. dango is designed around explicit tool and resource boundaries.

Each node should receive only the context projection, artifacts, directories, and tools allowed by its contract. This makes the execution boundary visible and supports safer scientific and engineering workflows.

## What dango is not

dango is not trying to replace mature community frameworks.

It is not primarily:

- a graph workflow builder;
- a visual agent editor;
- a general-purpose tool-calling framework;
- a provider abstraction layer;
- a chat app framework;
- a vector-memory or RAG framework;
- a marketplace of built-in tools.

Those components may be used around dango or behind dango. The purpose of dango is to provide the coordination, handoff, state, boundary, and evaluation layer needed for long-running scientific agent workflows.

## Intended use cases

dango is designed for scientific and engineering research tasks that require multiple specialized agents or skills to collaborate over time, such as:

- collecting and organizing literature or domain context;
- transforming messy observations into structured inputs;
- running data processing, modeling, visualization, and reporting stages;
- connecting property prediction, structure generation, or simulation tools as skills;
- preserving intermediate memos, resource paths, model outputs, and audit records;
- comparing centralized and semi-decentralized agent execution;
- evaluating tool-noise robustness, handoff quality, evidence tracing, and task recovery.

## Current status

This repository is actively evolving around the orchestrator, runner, agent, skill runtime, stream, persistence, and exchange-document layers.

The current implementation includes:

- request-level orchestration;
- runner lifecycle management;
- skill binding;
- exchange markdown documents;
- event-stream observation;
- local persistence.

The next development phase focuses on turning dango into an evaluable scientific-agent research kernel:

- contract schema v1;
- whiteboard schema v1;
- run bundle export;
- minimal evaluation harness;
- context projection and resource boundaries;
- stable review, replan, rerun, and reject protocol;
- canonical scientific workflow example.

More detailed development and architecture notes are available in `CONTRIBUTING.md`, `architecture/`, and `docs/`.

## Guiding principle

dango should not be optimized to become another general-purpose agent framework.

It should be optimized to answer a narrower and more valuable question:

> How can long-running scientific agent workflows be coordinated so that handoff is explicit, state is inspectable, tools are bounded, failures are recoverable, and results are evaluable?