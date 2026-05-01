---
name: markdown_to_pdf
description: Convert markdown reports to PDF only when the user explicitly requests a PDF output.
---
You convert markdown into PDF documents.

## Polish plan behavior

When asked to polish a plan, do not render a PDF yet. Return a Dango exchange
markdown document for orchestrator review that states:

- this skill should be selected only when the user explicitly requests a PDF;
- execution requires markdown text and can optionally accept an output path;
- execution will parse markdown and render a PDF through the external
  `markdown-pdf` package;
- this skill must not participate in groundwater parsing, elevation lookup, or
  GP model training.

If the request does not ask for PDF output, recommend leaving this skill out of
the plan. If upstream markdown exchange documents include front matter
`resources`, use those resource paths only when they are relevant to the PDF
requested by the user.

## Python environment

Run Python from this skill directory with `uv run python ...` so `markdown-pdf`
is available. For the common entrypoint, use:

```sh
uv run python scripts/render.py
```

When invoking from the standard bash command tool, commands run in the temp
playground, so use the source workspace path from the runtime instructions:

```sh
uv --directory <source workspace> run python scripts/render.py
```

When a `.venv` has already been created, `.venv/bin/python` is also a usable
interpreter for ad-hoc glue code in this skill environment.

## Execution behavior

Use this skill's local uv/Python environment and the external `markdown-pdf`
package to render a real PDF artifact. `scripts/render.py` is a ready-made
entrypoint for the common path, but it is not the only allowed approach. If the
user asks for a different report shape, write small glue code in the skill
playground, run it with the command tool, and call the package API directly.

Do not participate in data modeling or groundwater analysis unless the user has
explicitly asked for PDF output.

Use the standard skill command/file tools to run Python or ad-hoc glue code.
Do not depend on an example entrypoint registering a custom PDF rendering tool;
this skill owns that behavior.
