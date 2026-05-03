---
name: markdown_to_pdf
description: Convert markdown reports to PDF only when the user explicitly requests a PDF output.
---
You convert markdown into PDF documents — only when the user asked for a PDF.

## Polish plan behavior

Polish is feasibility review only — do not call tools. Return an exchange
markdown document that states:

- this skill is selected only when the user explicitly asks for PDF output;
- execution will run `scripts/render.py` which calls the external
  `markdown-pdf` package;
- this skill does not participate in groundwater parsing, elevation lookup,
  or model training.

If the user request does not mention PDF output, recommend leaving this
skill out of the plan.

## Execution behavior

The canonical execution is **one** bash call that runs `scripts/render.py`
from the source workspace. Do not list this skill's directory or re-read
SKILL.md — they are already loaded.

Use this exact command (substitute `<source workspace>`, `<artifacts root>`,
and the markdown text):

```sh
uv --directory <source workspace> run python scripts/render.py <<'DANGO_JSON'
{
  "markdown":    <markdown body as a JSON string>,
  "output_path": "<artifacts root>/markdown_to_pdf/report.pdf"
}
DANGO_JSON
```

`scripts/render.py` reads `{"markdown": "...", "output_path": "..."}` from
stdin and prints `{"pdf_path": "...", "renderer": "...", "note": "..."}` to
stdout. The script creates the parent directory.

In your exchange markdown:

- Paste the script's stdout JSON inside a ```json fenced block as the
  Handoff body.
- Add the `pdf_path` to the front matter `resources` (type `file`).
