---
name: elevation_lookup
description: Parse Japanese groundwater observation JSON and enrich each site with elevation data from a location lookup API or deterministic fallback.
---
You enrich groundwater observation records with elevation, then hand off
compact structured JSON for downstream modeling.

## Polish plan behavior

Polish is feasibility review only — do not call tools. Return an exchange
markdown document for orchestrator review that states:

- the input must be messy JSON containing site coordinates and groundwater
  level measurements;
- execution will run `scripts/enrich.py` which parses observations and calls
  the deterministic in-skill elevation API shim for every usable coordinate;
- the handoff will be a JSON object suitable for downstream modeling.

If the task is only about modeling, PDF rendering, or report formatting, say
this skill should not be selected.

## Execution behavior

The canonical execution is **one** bash call that runs `scripts/enrich.py`
with the messy JSON on stdin from the source workspace. Do not list this
skill's directory, re-read SKILL.md, or read enrich.py — they are already
loaded.

Use this exact command (substitute `<source workspace>` from the Workspace
access block above and `<messy JSON>` with the JSON the user supplied or the
upstream handoff):

```sh
uv --directory <source workspace> run python scripts/enrich.py <<'DANGO_JSON'
{ "observations_json": <messy JSON as a JSON string> }
DANGO_JSON
```

`scripts/enrich.py` reads `{"observations_json": "<raw text>"}` from stdin and
prints the enriched payload as JSON to stdout:

```
{
  "datum": "...",
  "elevation_source": "...",
  "observation_n": <int>,
  "observations": [{"site_id", "latitude", "longitude", "elevation_m",
                    "water_level_m_bgl", "estimated", "notes",
                    "source_water_text", "prefecture"}, ...]
}
```

The script already handles every variant in the messy input (mixed key names,
nested coordinate maps, "N/E/S/W" suffixes, level_cm_bgl, 水位, etc.).
**Do not write ad-hoc parsing glue code** unless the task description
explicitly demands a transformation `enrich.py` cannot produce.

In your exchange markdown:

- Paste the script's stdout JSON inside a ```json fenced block as the Handoff
  body. Downstream skills extract it from there.
- Set the front matter `resources` to any files you wrote to the artifacts
  root (the canonical run does not need to write files; passing JSON inline
  is enough).
