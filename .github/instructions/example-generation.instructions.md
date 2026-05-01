---
description: "Use when creating or modifying runnable examples under examples/. Examples must be purpose-driven integration exercises for orchestrator, runner, executor, and skill behavior rather than isolated demos."
applyTo: "examples/**"
---

# Example Generation

Examples under `examples/` are real-world integration exercises for improving
orchestrator, runner, executor, and skill APIs. Design them from an intended
user task first, then add only the runtime hooks needed for that task to work
end to end.

## Structure

Each example must live in its own subdirectory under `examples/` and include:

- `design_purpose.md` describing the real-world task, expected system behavior,
  intentionally available skills, and what the example is meant to pressure-test.
- `main.go` as the executable entrypoint.
- One subdirectory per skill used or offered by the example.
- Realistic sample input data when the task needs data. The sample should be
  large and varied enough to exercise the intended workflow, not a toy payload.
- An `artifacts/` directory under the example root when the workflow writes
  durable outputs. Skills may choose their own subdirectories beneath it.

## Entrypoint Shape

Keep `main.go` close to a real client of the system:

- Load shared configuration, including LLM service settings, through the same
  `.env` path used by the rest of the repo.
- Configure the orchestrator and register the example's skills.
- Submit the user's task through the orchestrator request API.
- Subscribe to runner updates and stream them until the runner settles.
- Use the example root's `artifacts/` directory for durable outputs rather than
  temporary directories in the executable path.

Do not put business outcome simulation, hand-built plans, direct skill calls, or
test assertions in `main.go`. Those belong in tests or skill implementations.
The executable should demonstrate the system wiring, not bypass it.

## Skill Realism

A skill directory must be executable as a real skill environment, not just a
short `SKILL.md` description.

- `SKILL.md` must describe both Polish plan behavior and execution behavior.
- Polish plan instructions must tell the skill how to preview requirements and
  handoffs without running execution tools or creating final artifacts.
- Execution instructions must identify the real local tool, package, API,
  command, or script entrypoint the skill can use to perform the work.
- If a Python skill is used, give it its own `pyproject.toml` and `uv.lock`.
  Declare real external dependencies when the skill's purpose would naturally
  use them, such as `scikit-learn` for Gaussian process modeling or
  `markdown-pdf` for Markdown-to-PDF rendering. Do not create a local package
  and list it as a dependency just to make `dependencies` non-empty.
- Dependencies may be empty when the skill genuinely needs only the Python
  standard library.
- Put reusable local package/API code in the skill only when it represents a
  meaningful local abstraction, not as ceremony around a script.
- A fixed script entrypoint is useful for common paths and tests, but the skill
  must not be designed as though that script is the only possible execution
  path. It should be able to inspect its assigned task, upstream exchange
  markdown, and accessible resources, then write ad-hoc glue code in its
  playground and run it with command tools when the task needs a different
  shape.
- When scripts are included, they should call package code or external APIs
  appropriate to the skill's stated purpose. Avoid placeholder scripts that
  only echo inputs. Prefer reusable package functions over burying all behavior
  in one fixed script.
- Skills that generate files for downstream use must return a Dango exchange
  markdown document with front matter `resources` entries naming those files or
  directories. The runner uses this metadata to configure downstream resource
  access; downstream skills use the markdown body to understand intent.
- Distractor skills are allowed, but they must still be real and runnable; the
  test should verify they are not selected unless the user asks for them.

When a user task requires a capability that the current orchestrator, runner,
executor, or skill APIs cannot express, extend the relevant API directly enough
to support the example. Do not hide missing system behavior inside example-only
simulation.

## Tests

Treat examples as integration tests that complement unit tests.

- Use tests to assert plan shape, selected skills, runner lifecycle updates,
  handoff content, and generated artifacts.
- Fake LLM services belong in tests, not in the example executable.
- Test skill scripts directly when their package/runtime behavior is important.
- Keep generated artifacts in temporary directories and ignore runtime
  environments such as `.venv/` and `__pycache__/`.
