# 21 — `artifact_catalog` Builtin Tool (code, wave 1)

Kind: code. Coverage memo § 3.5. Wave 1 — ships against the current
`coreTools` / `Tools()` surface; no security-model dependency.

**Prerequisite.** None. Lands after `20` and before `22` in number
order to keep `coreTools` merges clean. `11` later re-registers this
tool through the new config as part of its retrofit step.

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
5. Register into the current core tool set. (`11`'s retrofit moves this
   registration to the new config; nothing to do here for that.)

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

## Honshu observation

The tool adds a new user-facing output (the artifact table). After
tests pass, observe via honshu whether the table surfaces the right
columns at the right verbosity for a real downstream-handoff summary —
too sparse, too noisy, or right. Record adjustments. UX signal, not a
gate.

## Verifiable acceptance

- New and existing tests pass; `go test ./...` green.
- The tool returns a table with the documented columns on the test
  fixtures.
