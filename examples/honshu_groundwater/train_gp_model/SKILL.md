---
name: train_gp_model
description: Train a Gaussian-process-style groundwater model, create a prediction surface plot, and save prediction CSV artifacts for downstream analysis.
---
You build a groundwater prediction model from enriched observations and
produce CSV + SVG artifacts.

## When this skill applies

Use this skill when upstream has produced enriched observations carrying
`latitude`, `longitude`, `elevation_m`, and `water_level_m_bgl`. If
elevation values are missing, recommend an upstream elevation enrichment
step — this skill will not back-fill them. PDF output is out of scope
unless the user explicitly asked for a PDF.

## Python environment

The skill ships its own `uv`-managed Python environment with `scikit-learn`,
`numpy`, and `matplotlib`. Run scripts via
`uv --directory <source workspace> run python <script>`.

## Canonical entrypoint

`scripts/train.py` fits a `scikit-learn` `GaussianProcessRegressor` (RBF +
WhiteKernel), saves a prediction CSV, and saves a surface plot SVG via
`matplotlib`. Pass the upstream exchange markdown verbatim — the script
extracts the JSON block itself.

```sh
uv --directory <source workspace> run python scripts/train.py <<'JSON'
{
  "parent_exchange": <upstream exchange markdown as a JSON string>,
  "artifacts_dir":   "<artifacts root>/train_gp_model"
}
JSON
```

Stdin schema:

```
{
  "parent_exchange": "<full markdown body of the upstream handoff>",
  "artifacts_dir":   "<absolute directory where artifacts are written>"
}
```

The script creates `artifacts_dir` if it does not exist.

Stdout schema:

```
{
  "model": "...",
  "kernel": "...",
  "observation_count": <int>,
  "prediction_count":  <int>,
  "csv_path":  "...",
  "plot_path": "...",
  "resources": [{"path", "type", "description"}, ...],
  "mean_predicted_water_level_m_bgl": <float>,
  "validation_summary":  "...",
  "downstream_reminder": "..."
}
```

Paste the script's stdout JSON verbatim inside a fenced ```json``` block as
your Handoff body, and copy its `resources` array into your front matter
`resources` so downstream skills can reach the CSV and SVG.
