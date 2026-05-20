# Front Matter Parser Memo

Last updated: 2026-05-20.
Status: Design memo only. No implementation is committed by this document.

## Purpose

Dango currently uses `github.com/adrg/frontmatter` in several places to
split markdown front matter from the body and unmarshal the metadata into
Go structs. That package is small and straightforward, but it keeps
`gopkg.in/yaml.v2` in the dependency graph.

This memo records a minimal in-repo replacement plan so Dango can own
this behavior directly and later remove the external front matter
dependency.

## Current in-repo usage

Today the package is used for repository-owned markdown parsing in these
paths:

- `internal/llm/skill.go`
- `internal/engine/runner/handoff_doc.go`
- `internal/engine/runner/exchange_doc.go`
- `internal/engine/runner/memo.go`
- `internal/engine/runner/channel_document_parse.go`
- `internal/llm/internal/builtin/artifact_catalog.go`

These call sites all use the same narrow shape:

1. Read markdown from an `io.Reader` or string.
2. Parse optional leading front matter.
3. Unmarshal the front matter into a caller-provided struct.
4. Return the remaining markdown body.

Dango does not currently need the full generality of a multi-format
front matter library.

## Recommendation

Implement a small Dango-owned parser with the repository's actual needs
as the contract:

- Support YAML front matter only.
- Support the canonical `---` opening and closing delimiters.
- Parse only a leading front matter block at the top of the document.
- Return the remaining body bytes exactly once the front matter block is
  removed.
- Unmarshal metadata with `gopkg.in/yaml.v3`.

Do not carry over unused general-purpose features from the external
package unless a concrete Dango call site needs them.

## Proposed package shape

Create a small internal package dedicated to this responsibility, for
example `internal/frontmatter`.

Suggested surface:

- `Parse(r io.Reader, v any) ([]byte, error)`
- optionally `ParseString(raw string, v any) (body string, err error)` if
  that materially simplifies current call sites

Suggested behavior:

- If the document does not start with `---` on the first line, treat it
  as having no front matter and return the original body unchanged.
- If the document starts with `---`, scan until the matching closing
  delimiter line `---`.
- Unmarshal the bytes between delimiters into `v`.
- Return the remaining body after the closing delimiter, preserving the
  same body shape current call sites expect after trimming in their own
  logic.
- Return a clear error when a front matter block is started but not
  closed, or when YAML decoding fails.

## Scope boundaries

The initial implementation should stay intentionally small.

In scope:

- YAML only
- top-of-file parsing only
- `io.Reader` input
- decode into structs/maps supplied by the caller
- tests covering current Dango call patterns

Out of scope for the first implementation:

- TOML front matter
- JSON front matter
- custom delimiters such as `---yaml`
- serializer helpers
- format auto-detection beyond the single YAML shape Dango uses today
- attempting to be a drop-in clone of the external library API

## Behavioral notes to preserve

The replacement should match the semantics Dango already relies on, not
the entire external package surface.

Important cases:

- documents with no front matter should still parse successfully
- empty YAML front matter should be accepted if the caller's target type
  allows it
- front matter may be followed by an empty line before the markdown body
- the body may be empty
- the closing delimiter may be followed immediately by EOF
- malformed leading blocks should fail explicitly rather than silently
  turning into body text

## Testing plan

Add focused tests beside the new parser implementation that cover:

- no front matter
- valid YAML front matter with markdown body
- valid YAML front matter ending at EOF
- empty body after front matter
- unclosed front matter block
- invalid YAML
- body content that contains `---` after the initial block and must not
  be reparsed as front matter

After the parser lands, update the existing repository call sites to use
it and run the relevant Go tests for those packages.

## Migration plan

Suggested sequence:

1. Add the new internal parser package and its tests.
2. Switch the current `frontmatter.Parse(...)` call sites to the in-repo
   parser.
3. Re-run targeted package tests, then repository-wide Go tests.
4. Remove `github.com/adrg/frontmatter`.
5. Re-evaluate whether `gopkg.in/yaml.v2` is still needed anywhere; if
   not, finish the migration to `gopkg.in/yaml.v3`.

This keeps the front matter replacement as a narrow, reviewable change
before the broader YAML dependency cleanup.

## Open questions

- Whether the new package should return `[]byte` only, or also expose a
  string helper for the string-heavy runner call sites.
- Whether the parser should preserve body bytes exactly, or normalize the
  first newline after the closing delimiter. Current users often apply
  their own `strings.TrimSpace`, so exact preservation may be simplest.
- Whether any future app/cmd embedding Dango will need TOML/JSON front
  matter support. Unless a real use case appears, keep that out of the
  first implementation.
