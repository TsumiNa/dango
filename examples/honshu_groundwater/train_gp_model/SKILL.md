---
name: train_gp_model
description: Train a Gaussian-process-style groundwater model, create a prediction surface plot, and save prediction CSV artifacts for downstream analysis.
---
You build a groundwater prediction model from enriched observations and
produce CSV + SVG artifacts.

## Polish plan behavior

Polish is feasibility review only — do not call tools. Return an exchange
markdown document for orchestrator review that states:

- this skill needs upstream enriched observations with `latitude`,
  `longitude`, `elevation_m`, and `water_level_m_bgl`;
- execution will run `scripts/train.py` which fits a `scikit-learn`
  `GaussianProcessRegressor` (RBF + WhiteKernel), saves predictions to CSV,
  and saves a surface plot to SVG via `matplotlib`;
- PDF output is out of scope unless the user explicitly asks for it.

If elevation values are missing, recommend an upstream elevation enrichment
step.

## Execution behavior

The canonical execution is **one** bash call that runs `scripts/train.py`
from the source workspace. Do not list this skill's directory, re-read
SKILL.md, or read train.py — they are already loaded. Pass the upstream
exchange markdown verbatim; the script extracts the JSON block itself.

Use this exact command (substitute `<source workspace>` and `<artifacts root>`
from the prompt context, and `<parent markdown>` with the upstream exchange
markdown body):

```sh
uv --directory <source workspace> run python scripts/train.py <<'DANGO_JSON'
{
  "parent_exchange": <parent markdown as a JSON string>,
  "artifacts_dir":   "<artifacts root>/train_gp_model"
}
DANGO_JSON
```

`scripts/train.py` reads `{"parent_exchange": "...", "artifacts_dir": "..."}`
from stdin and prints a JSON summary to stdout:

```
{
  "model": "...", "kernel": "...",
  "observation_count": <int>, "prediction_count": <int>,
  "csv_path": "...", "plot_path": "...",
  "resources": [{"path", "type", "description"}, ...],
  "mean_predicted_water_level_m_bgl": <float>,
  "validation_summary": "...",
  "downstream_reminder": "..."
}
```

`mkdir -p` for `artifacts_dir` is handled by the script. **Do not write
ad-hoc training glue code** unless the task description requires a model or
output shape `train.py` cannot produce.

In your exchange markdown:

- Paste the script's stdout JSON inside a ```json fenced block as the
  Handoff body.
- Copy the `resources` array from the script output into your front matter
  `resources` so the runner exposes the CSV and SVG paths to downstream
  skills.
