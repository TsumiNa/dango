---
name: geotech-analysis
description: "Analyze geotechnical data including borehole logs, soil test results, rock strength measurements, SPT/CPT data, and geological survey CSVs. Use when the user mentions soil, rock, borehole, SPT, CPT, geological, geotechnical, ground investigation, or subsurface data."
---

# Geotechnical Data Analysis

## Overview

Analyze geotechnical investigation data from CSV, Excel, or structured text files.

## Supported Data Types

| Data Type | Key Columns | Analysis |
|-----------|-------------|----------|
| SPT (Standard Penetration Test) | depth, N-value, soil_type | N-value profile, liquefaction potential |
| CPT (Cone Penetration Test) | depth, qc, fs, u2 | Soil classification (Robertson), strength |
| Borehole Log | depth, layer, description | Stratigraphy summary |
| Unconfined Compression | specimen_id, qu, strain | Strength distribution |

## Workflow

1. **Load**: Read the data file, identify column structure
2. **Validate**: Check for missing values, outliers, unit consistency
3. **Analyze**: Compute statistics per layer/depth interval
4. **Visualize**: Generate depth profiles, histograms, cross-sections
5. **Report**: Summarize findings with engineering interpretation

## Key Rules

- Always state the coordinate reference system if location data is present
- Flag any N-values > 50 as refusal (report as ">50" not as integer)
- Depth axis should always point downward in plots
- Use JGS (Japanese Geotechnical Society) classification when appropriate
