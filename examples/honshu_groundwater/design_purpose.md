# Honshu Groundwater Model Example

This example is a purpose-driven integration test for the orchestrator, runner,
executor, and skill runtime.

The user supplies messy JSON describing groundwater measurements collected at
several locations on Honshu. The expected behavior is:

1. The orchestrator plans only the skills needed for the user request.
2. The elevation skill parses the messy observations and enriches each site
   with a deterministic elevation lookup through its own uv/Python project.
3. The GP-model skill consumes the upstream markdown exchange document, trains
   a Gaussian process regressor through its own uv/Python project and
   `scikit-learn`, then writes prediction artifacts with `matplotlib`.
4. The markdown-to-PDF skill is available as a distractor and must not be used
   unless the user explicitly asks for a PDF. It still has a working local
   `markdown-pdf` dependency so the distractor is realistic.

The executable example loads one LLM client from `.env` through
`llm.NewClientFromEnv` and reuses that client for the orchestrator planner and
all registered skills. Individual skills own their uv/Python execution
environment, dependency set, and scratch playground, but they do not own
separate LLM service settings.

The `main.go` entrypoint is deliberately thin: open startup-owned runtime
persistence under `artifacts/persistence/dango.db`, configure the orchestrator,
register the three skills, submit the user request, stream runner updates
until the runner settles, and wait for the request event log to persist the
terminal runner state before exit. Business assertions about selected skills,
artifacts, and persistence behavior live in tests rather than in the
executable path.

Generated files live under this example's `artifacts/` directory. Individual
skills decide their own subdirectory layout under that root. The fixed Python
scripts included in each skill are runnable entrypoints for common paths and
tests; they are not the system contract. At runtime, a skill is expected to read
its assigned task and upstream exchange markdown, decide what glue code is
needed, write temporary code in its playground when helpful, and run package/API
calls through the available command and filesystem tools.

The example also keeps two distinct debugging artifacts under that root: a
renderer-aligned JSONL stream archive in `artifacts/debug/stream_events.jsonl`
and the durable SQLite runtime persistence database in
`artifacts/persistence/dango.db`. The JSONL file is for inspection; the SQLite
database is the source of truth for startup-owned request persistence.

When a skill produces files for downstream use, it declares those paths in the
Dango exchange markdown front matter as `resources`; the runner parses that
machine-readable metadata and makes the containing directories available to
planned downstream skills.

Each `SKILL.md` also describes its Polish plan behavior. During the runner's
managed lifecycle, skill-specific polish calls should preview requirements and
handoffs without running execution tools or creating final artifacts.

This is not intended to validate hydrology. The model uses a real
`scikit-learn` Gaussian process API, but the data and elevation lookup remain
deterministic so the example stays reproducible. The test target is the system
shape: planning, skill selection, tool-calling, markdown handoff, downstream
consumption, and final artifacts.
