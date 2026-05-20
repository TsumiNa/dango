# 22 — `structured_preview` Builtin Tool (code)

Kind: code. Coverage memo § 3.5.

**Prerequisite.** `11` merged. Lands after `21` in number order to keep
tool-registry merges clean.

## Goal

Provide a token-frugal shape preview of JSON, JSON Lines, and YAML
files so the model can decide whether to read a file in full, without
`jq` / `yq` boilerplate.

## Scope

1. New file `internal/llm/internal/builtin/structured_preview.go` with
   `newStructuredPreview(ws workspace) tool`.
2. JSON schema:
   - `path` (string, required), resolved via `ws.ResolvePath`.
   - `format` (enum `auto`/`json`/`jsonl`/`yaml`, default `auto`):
     inferred from extension; unknown extension under `auto` errors and
     asks the caller to set `format`.
   - `max_keys_per_level` (int, default 20).
   - `max_depth` (int, default 3).
   - `sample_rows` (int, default 5, jsonl only).
3. Behavior:
   - JSON: parse with `encoding/json`; walk to `max_depth` emitting an
     indented sketch (`object{keys:[...], truncated:N}` /
     `array[len=N, elem:<type>]`).
   - JSONL: scan up to `sample_rows`; report a union schema (key → seen
     types, `null_rate`); report total scanned rows.
   - YAML: parse with `gopkg.in/yaml.v3` into `interface{}`, reuse the
     JSON walker. Promote `yaml.v3` to a direct dependency in `go.mod`.
   - All caps emit `(truncated)` markers.
4. Register into the core tool set per the post-`11` config shape.

## Tests (`structured_preview_test.go`)

- `TestStructuredPreviewJSONObject`, `...JSONArray`.
- `TestStructuredPreviewJSONLSchemaInference` (three rows, one missing
  key, reports `null_rate`).
- `TestStructuredPreviewYAMLObject`.
- `TestStructuredPreviewRespectsMaxDepth`, `...RespectsMaxKeysPerLevel`.
- `TestStructuredPreviewAutoFormatUnknownExtension`.
- `TestStructuredPreviewMalformedInput`.
- `TestStructuredPreviewPathEscapeRejected`.

## Out of scope

- No transformation/projection/filtering (those stay with `jq`/`yq`).

## Verifiable acceptance

- New and existing tests pass; `go test ./...`, `go vet ./...`,
  `go build ./...` green.
- If `yaml.v3` promoted, `go mod tidy` produces no diff.
