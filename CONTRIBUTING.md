# Contributing to dango

This document is for contributors and maintainers. It covers the current architecture, repository structure, development workflow, and project-specific conventions. Keep user-facing project purpose and eventual usage examples in `README.md`; keep implementation and workflow guidance here.

## Architecture Overview

dango is organized around a request-level orchestrator, runner-owned execution lifecycle, agent-to-skill bindings, stream-based observation, and durable persistence.

- The `Orchestrator` receives external requests, keeps the skill registry, runs planning through the built-in orchestrator skill, materializes accepted plans into runners, and exposes live query/subscription APIs.
- A `Runner` owns one planned task graph. In managed mode it drives plan polish, review, replan, execute, report, and settle phases.
- An `Agent` is a one-to-one proxy for a single skill runtime inside a runner node. It binds node context, runs polish/execute/report stages, and normalizes stage output into exchange markdown.
- A `Skill` owns its prompt, tool environment, scratch workspace, accessible directories, and LLM conversation.
- Streams are the runtime communication substrate. Request streams aggregate planning and runner events; runner streams aggregate runner, agent, and skill events.
- Persistence stores request event logs, runner records, snapshot cursors, and workspace paths so live state can be replayed or audited later.

The detailed architecture notes under `architecture/` are the best starting point for understanding the current engine shape:

- `architecture/README.md` gives the high-level map.
- `architecture/control-plane.md` describes request intake, planning, runner creation, and describe replay.
- `architecture/runner-lifecycle.md` describes managed runner phases and state transitions.
- `architecture/data-plane.md` describes exchange markdown, stream merge, event logs, and runner records.
- `architecture/orchestrator-agent_interative.md` gives a compact orchestrator/agent interaction view.

## Repository Structure

```text
main.go                         CLI entrypoint
cmd/                            Cobra commands; serve is the current API server command
internal/engine/                request orchestration, planning, agents, queues, describe replay
internal/engine/builtin/        embedded orchestrator skill and planning instructions
internal/engine/runner/         runner lifecycle, task graph execution, exchange documents
internal/engine/runner/persistence/
                                unified persistence backends used by orchestrator and runner
internal/engine/stream/         replayable stream, subscription, filtering, merge, and framing
internal/llm/                   OpenAI Responses API client, conversations, tools, skills, workspaces
internal/store/                 persistence abstractions and lightweight JSON fallbacks
internal/store/sqlite/          SQLite migrations, sqlc queries, and durable store implementation
internal/store/postgres/        Postgres durable stores for runtime persistence
internal/store/runtime/         startup-owned persistence wiring
internal/server/                HTTP and Unix socket API server lifecycle and routes
internal/streamrender/          terminal renderer for stream subscriptions
internal/logging/               shared logging setup
internal/prompts/               repository-owned prompt assets and prompt package docs
demo/                           focused executable demos for orchestration, skills, and streams
examples/honshu_groundwater/    integration example for multi-skill research workflow behavior
docs/                           design memos and implementation notes
.github/instructions/           canonical repository-specific coding and workflow rules
```

Go source is organized for reviewer navigation:

- Keep each primary exported type with its constructor, exported API, methods, and tightly coupled supporting types.
- Move distinct helper responsibilities into focused companion files only when that makes the package easier to scan.
- Keep one package overview per package in `doc.go`.
- Avoid catch-all production files for unrelated shared types.

## Development Workflow

Before editing code or docs, read the relevant instruction files in `.github/instructions/`. `AGENTS.md`, `CLAUDE.md`, and `GEMINI.md` are lightweight entrypoints that point back to those canonical rules.

For ordinary Go changes, run the focused package tests you touched and then the full suite when feasible:

```bash
just test
```

The `just test` recipe runs `go test ./...` with isolated Go caches under `/tmp`. If `just` is unavailable, use the same cache shape directly:

```bash
GOCACHE=/tmp/dango-gocache GOMODCACHE=/tmp/dango-gomodcache go test ./...
```

Run the local API server during development with:

```bash
go run . serve --port 8080
```

The `cmd/add.go` and `cmd/run.go` commands are still Cobra placeholders. Do not document them as supported user workflows until their behavior is implemented.

## Database Workflow

SQLite schema evolution is explicit and reviewable:

- Runtime migrations live under `internal/store/sqlite/migrations/`.
- The current SQLite schema snapshot lives in `internal/store/sqlite/schema.sql`.
- Application SQL lives in `internal/store/sqlite/queries.sql`.
- Generated sqlc wrappers live under `internal/store/sqlite/db/`.

When changing SQLite tables, columns, indexes, constraints, or query shapes:

1. Create a migration pair with `just db-new-migration <name>`.
2. Write the `.up.sql` and `.down.sql` files by hand.
3. Update `internal/store/sqlite/schema.sql`.
4. Update `internal/store/sqlite/queries.sql` if reads or writes changed.
5. Regenerate wrappers with `just db-generate`.
6. Run `go test ./...` or `just test`.

Postgres durable stores live under `internal/store/postgres/`; keep backend-specific migrations and store behavior aligned with the shared persistence contracts in `internal/store/` and `internal/engine/runner/persistence/`.

## Documentation Boundaries

- `README.md` is user-facing: project purpose, audience, eventual install/quick start, CLI/API usage, and concise examples.
- `CONTRIBUTING.md` is developer-facing: architecture context, repository structure, workflow, testing, database process, and contribution conventions.
- `architecture/` holds current architecture walkthroughs for control plane, runner lifecycle, and data plane behavior.
- `docs/` holds design memos and implementation notes that are too detailed or provisional for the README.
- Go API truth belongs in source doc comments and package `doc.go` files.

## Coding Rules

Repository-specific rules live in `.github/instructions/` and should be consulted before relevant work:

- `branch-and-pr-workflow.instructions.md`
- `database-workflow.instructions.md`
- `example-generation.instructions.md`
- `go-docs.instructions.md`
- `go-file-organization.instructions.md`
- `implementation-and-tests.instructions.md`
- `in-branch-api-compat.instructions.md`
- `repository-doc-boundaries.instructions.md`
- `shell-environment.instructions.md`

When changing Go code, keep scope small, prefer direct concrete structures, colocate tests beside the code they exercise, and document exported behavior with idiomatic Go comments.

## Security and Local Secrets

- `.env` and `.env.*` are git-ignored.
- Keep local model keys and provider credentials in untracked env files only.
- Do not commit generated runtime artifacts, local databases, scratch workspaces, or example output directories unless the task explicitly requires a checked-in fixture.
