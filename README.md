# dango

`dango` is a three-tier AI agent orchestration framework built as a single Go binary with two modes:

- `dango orchestrator`: registry, task state, scheduler, HTTP server
- `dango executor`: tool self-description and runtime entrypoint inside containers

## Current scope

This initial implementation focuses on the framework skeleton and the first operational path:

- Review-friendly package layout with clear boundaries
- SQLite-backed tool/task/edge/log state store
- Tool registration flow via container runtime abstraction
- Dev/demo `host://` runtime for local tool directories
- `tool.yaml` override merge
- `_handoff.md` frontmatter parsing/rendering
- Minimal orchestrator HTTP server
- Demo planner and sequential execution engine
- Generic executor runtime with optional `/opt/tool/run` hook
- Local scheduler skeleton for container execution

The AI planning layer and remote object storage are intentionally left as extension points rather than hard-coded now.

## Package layout

```text
cmd/dango/                 binary entrypoint
internal/cli/              command parsing and top-level wiring
internal/spec/             pure domain types and validation
internal/layout/           filesystem/data-dir layout helpers
internal/store/sqlite/     SQLite schema and persistence
internal/runtime/          container runtime abstraction and Docker CLI impl
internal/orchestrator/     registry, tasks, scheduler, HTTP server
internal/executor/         executor describe/run logic
```

Each package now keeps its primary exported type in the package-named file
where practical, with helper logic moved into responsibility-focused companion
files such as `runtime_context.go`, `tool_spec.go`, or `handoff.go`. Package
overview docs live in `doc.go` so `go doc` and human review both start from the
same entrypoint.

## Implemented commands

```text
dango orchestrator serve
dango orchestrator register <image:tag> [--override path]
dango orchestrator unregister <tool_name>
dango orchestrator list-tools
dango orchestrator demo-run --request "write a short Japanese status report"

dango executor describe [--format yaml|json]
dango executor run --task-id <uuid> [--sub-task path]
```

## Logging

All commands now support structured logging controls:

```text
--log-level   debug|info|warn|error
--log-format  text|json
--log-file    optional path for teeing logs to a file
--log-source  include source locations
```

Environment fallbacks are also supported:

```text
DANGO_LOG_LEVEL
DANGO_LOG_FORMAT
DANGO_LOG_FILE
DANGO_LOG_SOURCE
```

Logs are written to stderr by default so command JSON/YAML outputs on stdout remain script-friendly.

## Demo

The local demo path uses built-in toy tools that are materialized under the chosen data directory at runtime. They are not intended to be source-controlled.

Run the full demo pipeline:

```bash
go run ./cmd/dango orchestrator demo-run \
  --data-dir ./.dango-demo \
  --request "用日语写一个简短的项目状态更新 demo"
```

This command will:

- materialize built-in toy tools under `./.dango-demo/_builtin_demo_tools/`
- create a task and generate a linear DAG
- execute each tool sequentially with the shared env contract
- write artifacts under `./.dango-demo/tasks/<task_id>/`

The terminal artifact is stored in the last edge output directory, and the synthesized orchestration result is written to `result.md`.

## Notes

- `executor describe` reads `tool.yaml` from:
  1. `DANGO_TOOL_YAML`
  2. `/opt/tool/tool.yaml`
  3. `./tool.yaml`
- `executor run` supports an optional tool hook:
  1. `DANGO_TOOL_RUN`
  2. `/opt/tool/run`
  3. `/opt/tool/bin/run`

If no hook exists, the executor writes a scaffold output and a valid `_handoff.md`, which keeps the orchestration pipeline testable before tool-specific logic is added.

- `orchestrator register` also accepts `host://<tool-dir>` for local dev/demo tools. Those tool directories must contain `tool.yaml` and an executable `run` file.
- `.env` and `.env.*` are ignored to reduce the risk of leaking local secrets such as OpenRouter API keys.
