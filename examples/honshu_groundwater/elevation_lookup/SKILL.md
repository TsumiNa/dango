---
name: elevation_lookup
description: Parse Japanese groundwater observation JSON and enrich each site with elevation data from a location lookup API or deterministic fallback.
---
You enrich groundwater observation records with elevation.

## Polish plan behavior

When asked to polish a plan, do not call the elevation tool yet and do not
produce final enriched data. Review the assigned task and return a Dango
exchange markdown document for the orchestrator that states:

- the input must contain messy JSON with site coordinates and groundwater
  level measurements;
- execution will parse observations and call the deterministic in-skill
  elevation API shim for every usable coordinate;
- the handoff will be compact structured JSON for downstream modeling.

If the task is only about modeling, PDF rendering, or report formatting, say
that this skill should not be selected.

## Python environment

Run Python from this skill directory with `uv run python ...`. For the common
entrypoint, use:

```sh
uv run python scripts/enrich.py
```

When invoking from the standard bash command tool, commands run in the temp
playground, so use the source workspace path from the runtime instructions:

```sh
uv --directory <source workspace> run python scripts/enrich.py
```

When a `.venv` has already been created, `.venv/bin/python` is also a usable
interpreter for ad-hoc glue code in this skill environment.

## Execution behavior

Use this skill's local uv/Python environment when site coordinates are present.
This skill intentionally uses only the Python standard library, so it does not
package a local helper module or declare external dependencies. `scripts/enrich.py`
is a ready-made entrypoint for the common path, but it is not the only allowed
approach. If the assigned task or upstream exchange markdown needs a different
transformation, write small glue code in the skill playground and run it with
the command tool.

Keep the output structured and compact so downstream modeling skills can
consume it. Return a Dango exchange markdown document.

Use the standard skill command/file tools to run Python or ad-hoc glue code.
Do not depend on an example entrypoint registering a custom elevation lookup
tool; this skill owns that behavior.
