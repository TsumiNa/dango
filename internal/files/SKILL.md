---
name: data-viz
description: "Create charts, graphs, and data visualizations from structured data. Use when the user asks for plots, charts, histograms, scatter plots, heatmaps, or any visual representation of data."
---

# Data Visualization

## Overview

Generate publication-quality charts and plots from data files.

## Chart Selection Guide

| Data Pattern | Recommended Chart |
|-------------|------------------|
| Trend over time | Line chart |
| Category comparison | Bar chart (horizontal if >5 categories) |
| Distribution | Histogram or box plot |
| Correlation | Scatter plot |
| Composition | Stacked bar or pie (≤5 slices) |
| Spatial | Heatmap or contour |

## Style Rules

- Use colorblind-safe palettes (viridis, cividis)
- Label all axes with units
- Title should state the insight, not just the data
- Prefer SVG output for reports, PNG for presentations
