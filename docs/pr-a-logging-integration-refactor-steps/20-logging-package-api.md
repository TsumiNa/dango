# PR A-2 — Logging Package API Rewrite

Kind: code, breaking-but-contained.

**Prerequisite.** PR A-1 merged (the pretty handler must exist for
`NewLogger` to wire it in).

## Goal

Replace the current `internal/logging.Config` / `New` surface with the
explicit-cfg-wiring API described in
[master plan §3.1](../pr-a-logging-integration-refactor-plan.md#31-internalogging).
Format becomes non-configurable; only sink and level are. Default
sink is `io.Discard` so an unconfigured caller produces no log output
(the "redirect to `/dev/null`" default).

## Scope

1. **Rewrite `Config`** to `{Level slog.Level, Output io.Writer, AddSource bool}`.
2. **Replace `New(stderr io.Writer, cfg Config) (*slog.Logger, io.Closer, error)`**
   with `NewLogger(cfg Config) *slog.Logger`. The new function:
   - Never returns nil. Zero `Config` yields a discard logger.
   - Wires the PR A-1 pretty handler with the resolved
     `(Output, Level, AddSource)`.
   - Always annotates the returned logger with `service=dango`.
3. **Add `OpenFileSink(path string) (io.WriteCloser, error)`** — opens
   `path` with `O_CREATE|O_APPEND|O_WRONLY`, `MkdirAll` on the parent,
   returns the `*os.File`. Caller owns the close.
4. **`DefaultConfig()`** returns
   `{Level: slog.LevelInfo, Output: io.Discard, AddSource: true}`.
5. **Keep `From` and `Component`** unchanged in semantics; touch only
   doc comments to mention the new wiring model.
6. **Remove** `Format`, `File`, `BindFlags`, all `DANGO_LOG_*` env
   reads, and `flagBinder`.
7. **Rewrite `doc.go`** to describe: discard default, preset format
   (link to master plan §5), one-call wiring via
   `orchestrate.WithLogger`.

## Files modified

- `internal/logging/logging.go` (heavy rewrite — surface shrinks).
- `internal/logging/logging_test.go` (rewritten alongside).
- `internal/logging/doc.go` (rewritten).

## Files not touched

- `internal/logging/handler.go` / `handler_test.go` — landed by A-1.
- Everything outside `internal/logging/`. No engine, demo, or example
  changes belong in this PR. (Engine still calls `slog.New(...)`
  directly today, so it builds independently of this PR.)

## Tests

`internal/logging/logging_test.go` (rewritten):

- `TestDefaultConfigIsDiscard` —
  `NewLogger(DefaultConfig()).Info("x")` writes nothing observable.
  Verified by replacing `os.Stderr` with a pipe and asserting the read
  side stays empty.
- `TestNewLoggerCarriesServiceAttr` — emit into a buffer sink, assert
  output contains `service=dango`.
- `TestNewLoggerUsesPrettyHandler` — emit into a buffer sink, assert
  output matches the pretty layout regex from A-1 (level token, source
  column, message).
- `TestComponentAddsField` — `Component(l, "ru").Info("x")` contains
  `component=ru`.
- `TestOpenFileSinkCreatesParents` — given
  `filepath.Join(t.TempDir(), "a/b/c.log")`, the call succeeds,
  returns a non-nil closer, the file exists, and writes are visible
  after `Close`.
- `TestOpenFileSinkFailsOnInvalidPath` — passing an empty string
  returns an error and a nil closer.

## Acceptance

- `go test ./internal/logging/...` green.
- `go vet ./internal/logging/...` clean.
- `grep -rn "DANGO_LOG_" --include="*.go"` returns nothing under
  `internal/logging/`. (If a `cmd/` binary later wants envvars, it
  owns that mapping.)
- `grep -rn "logging\\.Format\\|logging\\.BindFlags" --include="*.go"`
  returns nothing repo-wide.
- The package no longer imports `os` for environment reads
  (`os.Getenv` removed); `os` may still be imported transitively if
  needed for file-sink work.

## Notes for reviewer

- This PR is contained because no other Go file in the repo currently
  imports any of `logging.New`, `logging.Config`, or
  `logging.DefaultConfig`. The breakage is internal to the package.
- `OpenFileSink` is a thin convenience, not a hard requirement. It is
  here so callers don't have to repeat `MkdirAll` + `OpenFile`
  boilerplate. If reviewers prefer to drop it and push that onto
  callers, the API change is local — only the demo/example migration
  in PR A-3 would expand slightly.
- `AddSource` default is `true` because the pretty handler is designed
  around the source column. Reviewers who think a quieter default is
  better should flag it here; PR A-3 inherits whatever default lands.
