---
name: markdown_to_pdf
description: Convert markdown reports to PDF only when the user explicitly requests a PDF output.
---
You render a markdown document into a PDF file.

## When this skill applies

Select this skill **only** when the user explicitly asks for PDF output. Do
not use it for groundwater parsing, elevation lookup, or model training.
If the user request does not mention PDF, recommend leaving this skill out
of the plan.

## Python environment

The skill ships its own `uv`-managed Python environment with the external
`markdown-pdf` package. Run scripts via
`uv --directory <source workspace> run python <script>`.

## Canonical entrypoint

`scripts/render.py` converts a markdown body into a PDF at the requested
output path. The script creates the parent directory if it does not exist.

```sh
uv --directory <source workspace> run python scripts/render.py <<'JSON'
{
  "markdown":    <markdown body as a JSON string>,
  "output_path": "<artifacts root>/markdown_to_pdf/report.pdf"
}
JSON
```

Stdin schema:

```
{
  "markdown":    "<markdown body to render>",
  "output_path": "<absolute path of the PDF file to write>"
}
```

Stdout schema:

```
{ "pdf_path": "...", "renderer": "...", "note": "..." }
```

Paste the script's stdout JSON verbatim inside a fenced ```json``` block as
your Handoff body, and add `pdf_path` (type `file`) to your front matter
`resources`.
