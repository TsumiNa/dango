---
name: train_gp_model
description: Train a Gaussian-process-style groundwater model, create a prediction surface plot, and save prediction CSV artifacts for downstream analysis.
---
You build a groundwater prediction model from enriched observations.

## Polish plan behavior

When asked to polish a plan, do not train the model yet and do not create CSV
or plot artifacts. Return a Dango exchange markdown document for orchestrator
review that states:

- this skill requires upstream enriched observations containing latitude,
  longitude, elevation, and `water_level_m_bgl`;
- execution will fit a Gaussian process regression model through
  `scikit-learn` using RBF and WhiteKernel components;
- execution will create a prediction grid over Honshu, save prediction values
  as CSV, and save a surface plot as SVG through `matplotlib`;
- PDF output is out of scope unless the user explicitly asks for it.

If elevation values are missing, recommend an upstream elevation enrichment
step before this skill runs.

## Execution behavior

Use this skill's local uv/Python environment with `scikit-learn`, `numpy`, and
`matplotlib` to train the model. `scripts/train.py` is a ready-made entrypoint
for the common path, but it is not the only allowed approach. Read the assigned
task and upstream exchange markdown first; if the handoff shape requires
different data cleanup, feature construction, plotting, or artifact layout,
write small glue code in the skill playground, run it with the command tool,
and call those installed libraries directly.

Prefer a CSV artifact for prediction values unless the user explicitly asks only
for narrative output. Include artifact paths and validation notes in the
handoff. Return a Dango exchange markdown document whose front matter includes
`resources` entries for the generated CSV and SVG paths so the runner can make
their containing directory available to downstream skills.
