# 21 — `artifact_catalog` Builtin Tool (code)

Kind: code. Coverage memo § 3.5.

**Prerequisite.** `11` merged (registers into the post-reshape config).
Independent of `20` and `22`, but lands in number order to keep
tool-registry merges clean.

## Goal

Provide a single-call summary of a task's downstream artifacts by
merging on-disk directory metadata with the handoff front-matter
`artifacts:` list, so the model does not chain `list_dir` + `read_file`
+ YAML parsing each turn.

## Scope

1. New file `internal/llm/internal/builtin/artifact_catalog.go` with
   `newArtifactCatalog(ws workspace) tool`.
2. JSON schema:
   - `path` (string, optional, default `downstream/artifacts`),
     resolved via `ws.ResolvePath`.
   - `handoff_path` (string, optional, default `downstream/handoff.md`),
     resolved via `ws.ResolvePath`; skipped silently if absent.
   - `max_entries` (int, optional, default 50).
3. Behavior:
   - Walk `path` one level deep. Per entry: relative path, kind
     (`file`/`dir`), size (files), mod time to seconds.
   - If `handoff_path` exists, parse only its YAML front matter, extract
     the `artifacts:` list, and match by the front-matter `path` field;
     carry over `type` and `description`.
   - Disk entries absent from the manifest → `unlisted`. Manifest
     entries absent on disk → `missing`.
   - Output: markdown table `path | kind | size | type | description |
     status`, with a `(N more, truncated)` footer when capped.
4. Front-matter parsing is a local helper using `gopkg.in/yaml.v3`; do
   **not** import `internal/engine/runner` (layering boundary).
5. Register into the core tool set per the post-`11` config shape.

## Tests (`artifact_catalog_test.go`)

- `TestArtifactCatalogReturnsDiskAndManifestMerge`.
- `TestArtifactCatalogFlagsUnlistedDiskEntry`.
- `TestArtifactCatalogFlagsMissingManifestEntry`.
- `TestArtifactCatalogMissingHandoffIsSilent`.
- `TestArtifactCatalogMissingDirectoryReturnsError`.
- `TestArtifactCatalogPathEscapeRejected`.
- `TestArtifactCatalogRespectsMaxEntries`.

## Out of scope

- No write semantics, no recursion beyond depth 1, no non-handoff
  manifests.

## Verifiable acceptance

- New and existing tests pass; `go test ./...` green.
- Honshu example completes; if invoked, the tool returns a table with
  the documented columns.
