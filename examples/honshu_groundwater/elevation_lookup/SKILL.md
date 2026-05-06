---
name: elevation_lookup
description: Parse Japanese groundwater observation JSON and enrich each site with elevation data from a location lookup API or deterministic fallback.
---
You enrich groundwater observation records with elevation, then return a
compact structured JSON payload suitable for downstream modeling.

## When this skill applies

Use this skill when the input is messy JSON containing site coordinates and
groundwater level measurements that need to be normalized and joined with
elevation values. Do not use it for modeling, plot rendering, or report
formatting — those are other skills' jobs.

## Python environment

The skill ships its own `uv`-managed Python environment. Run scripts via
`uv --directory <source workspace> run python <script>` so the project's
dependencies resolve correctly.

## Canonical entrypoint

`scripts/enrich.py` handles every variant in the messy input (mixed key
names, nested coordinate maps, "N/E/S/W" suffixes, `level_cm_bgl`, 水位,
…). One invocation produces the full enriched payload — there is no need
to write ad-hoc parsing glue unless the task explicitly demands a
transformation `enrich.py` cannot produce.

```sh
uv --directory <source workspace> run python scripts/enrich.py <<'JSON'
{ "observations_json": <messy JSON as a JSON string> }
JSON
```

Stdin schema:

```
{ "observations_json": "<raw text of the user-supplied JSON>" }
```

Stdout schema:

```
{
  "datum": "...",
  "elevation_source": "...",
  "observation_n": <int>,
  "observations": [
    {"site_id", "latitude", "longitude", "elevation_m",
     "water_level_m_bgl", "estimated", "notes",
     "source_water_text", "prefecture"},
    ...
  ]
}
```

Paste the script's stdout JSON verbatim inside a fenced ```json``` block as
your Handoff body. The canonical run does not write files, so `resources`
is normally empty; populate it only if you wrote an artifact yourself.
