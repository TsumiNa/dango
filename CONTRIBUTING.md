# Contributing to dango

This document is for contributors and maintainers. It covers architecture context, repository structure, development workflow, and documentation/code conventions.

## Architecture Overview

`dango` is a three-tier orchestration design:

- Tier 1 (`orchestrator`): request intake, registry, task APIs, LLM planner integration, and persisted control state
- Tier 2 (`runner`): background execution, state transitions, edge dispatch, and executor supervision
- Tier 3 (`executor`): tool-facing `describe` and `run` contract

The planner prompt lives in `internal/orchestrator/prompts/` and is intended to be edited directly during planner iteration.

The diagram below gives contributors a high-level view of how the orchestrator, scheduler, executor, storage, and tool runtime pieces fit together.

![dango architecture overview](dango_architecture.svg)

## Repository Structure

```text
cmd/dango/                 binary entrypoint
internal/cli/              CLI parsing and top-level wiring
internal/llm/              LLM provider clients used by planning flows
internal/spec/             shared domain contracts and validation
internal/datadir/          data-dir path locators
internal/store/sqlite/     SQLite migrations, sqlc query definitions, and persistence
internal/runtime/          runtime abstraction (Docker + host-local execution)
internal/orchestrator/     registry, task persistence, planner, HTTP server
internal/runner/           runner state machine and execution scheduling
internal/executor/         executor describe/run implementation
```

Go source is organized for reviewer navigation:

- Keep each primary exported type with its constructor/methods in the package's main file.
- Move distinct helper responsibilities into focused companion files (for example `runtime_context.go`, `tool_spec.go`, `handoff.go`).
- Keep one package overview per package in `doc.go`.

## Development Workflow

Build and test:

```bash
go test ./...
```

Regenerate SQLite query wrappers after editing `internal/store/sqlite/queries.sql`
or `internal/store/sqlite/schema.sql`:

```bash
go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate
```

Add new SQLite schema changes as numbered migration pairs under
`internal/store/sqlite/migrations/` using `.up.sql` and `.down.sql` files.

Run the HTTP server:

```bash
go run ./cmd/dango orchestrator serve --data-dir /tmp/dango-data
```

By default, orchestrator commands store runtime state under `~/.dango/data`.
Reserve `~/.dango/` as the user-scoped home for future dango configuration
files as well.

## Documentation Boundaries

- Keep `README.md` user-facing: purpose, usage, examples, quick start.
- Keep `CONTRIBUTING.md` developer-facing: structure, architecture, workflow, conventions.

## Coding and Documentation Rules

Repository-specific rules live in `.github/instructions/`:

- `go-file-organization.instructions.md`
- `go-docs.instructions.md`
- `repository-doc-boundaries.instructions.md`

`AGENTS.md` and `CLAUDE.md` point to these files as canonical guidance.

## Security and Local Secrets

- `.env` and `.env.*` are git-ignored.
- Keep local model keys in untracked env files only.
