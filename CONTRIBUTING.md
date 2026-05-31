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

The detailed architecture notes under `architecture/` are the best starting point for understanding the current orchestrator/runner/agent shape:

- `architecture/README.md` gives the high-level map.
- `architecture/control-plane.md` describes request intake, planning, runner creation, and describe replay.
- `architecture/runner-lifecycle.md` describes managed runner phases and state transitions.
- `architecture/data-plane.md` describes exchange markdown, stream merge, event logs, and runner records.
- `architecture/orchestrator-agent_interative.md` gives a compact orchestrator/agent interaction view.

## Repository Structure

dango is structured as a reusable library plus one application that consumes it.
The public library packages live at the module root so a downstream module can
import them for secondary development; `cmd/` is dango's own CLI application built
on top of those packages. Anything under an `internal/` directory (the top-level
`internal/` or a package-local `<pkg>/internal/`) is dango-private and cannot be
imported by other modules.

```text
main.go                         CLI entrypoint (delegates to cmd.Execute)
cmd/                            Cobra commands; serve is the current API server command
cmd/server/                     HTTP and Unix socket API server lifecycle and routes

# Public library packages (importable by downstream modules)
orchestrator/                   request orchestration, planning, queues, describe replay (primary entrypoint)
orchestrator/builtin/           embedded orchestrator skill and planning instructions
agent/                          per-node execution proxy that runs one skill for a runner
runner/                         runner lifecycle, task graph execution, exchange documents
runner/persistence/             persistence Backend interface and markdown mirror backend
llm/                            OpenAI Responses API client, conversations, tools, skills, workspaces
llm/internal/builtin/           built-in tool implementations (private)
llm/internal/toolpolicy/        tool capability policy (private)
store/                          persistence abstractions and lightweight JSON fallbacks
store/runtime/                  startup-owned persistence wiring (Open, Config, Persistence)
store/internal/sqlite/          SQLite migrations, sqlc queries, and durable store (private)
store/internal/postgres/        Postgres durable stores for runtime persistence (private)
store/internal/backend/         concrete SQLite/Postgres persistence backends (private)
stream/                         replayable stream, subscription, filtering, merge, and framing
streamrender/                   terminal renderer for stream subscriptions
logging/                        shared logging setup

# dango-private helpers (not importable by downstream modules)
internal/instructions/          embedded agent stage markdown notes (used by agent)
internal/frontmatter/           YAML/markdown frontmatter parsing
internal/mcpclient/             MCP SDK client isolation wrapper

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

Postgres durable stores live under `internal/store/postgres/`; keep backend-specific migrations and store behavior aligned with the shared persistence contracts in `internal/store/` and `internal/runner/persistence/`.

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
