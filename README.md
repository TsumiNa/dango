# dango

`dango` is a Go-based AI orchestration framework shipped as a single binary with two modes:

- `dango orchestrator` for registration, scheduling, and task serving
- `dango executor` for in-tool `describe` and `run` entrypoints

## Quick Start

Run the local demo pipeline:

```bash
go run ./cmd/dango orchestrator demo-run \
  --data-dir ./.dango-demo \
  --request "Write a short project status update"
```

This command will register built-in toy tools, execute a demo task, and write outputs under `./.dango-demo/tasks/<task_id>/`.

## CLI Usage

```text
dango orchestrator serve
dango orchestrator register <image:tag> [--override path]
dango orchestrator unregister <tool_name>
dango orchestrator list-tools
dango orchestrator demo-run --request "..."

dango executor describe [--format yaml|json]
dango executor run --task-id <uuid> [--sub-task path]
```

Non-demo orchestrator commands default `--data-dir` to `~/.dango/data`.

## Logging

All commands support:

```text
--log-level   debug|info|warn|error
--log-format  text|json
--log-file    optional log file path
--log-source  include source locations
```

Environment fallbacks:

```text
DANGO_LOG_LEVEL
DANGO_LOG_FORMAT
DANGO_LOG_FILE
DANGO_LOG_SOURCE
```

## Tool Runtime Notes

- `orchestrator register` accepts container images and `host://<tool-dir>` for local demo tools.
- `executor describe` searches `tool.yaml` in `DANGO_TOOL_YAML`, `/opt/tool/tool.yaml`, then `./tool.yaml`.
- `executor run` uses `DANGO_TOOL_RUN`, `/opt/tool/run`, or `/opt/tool/bin/run` when present.
- If no run hook exists, executor writes scaffold artifacts and a valid `_handoff.md`.

## For Contributors

Developer-facing architecture, project structure, workflow, and coding/documentation conventions are in [CONTRIBUTING.md](CONTRIBUTING.md).
