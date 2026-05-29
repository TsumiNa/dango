# PR A-1 — Pretty Handler

Kind: code, additive.

**Prerequisite.** None. Purely adds new files inside
`internal/logging/`. The existing `logging.New` / `Config` keep working
unchanged.

## Goal

Add the preset `slog.Handler` that PR A-2 will wire in. Landing the
handler on its own lets reviewers focus on the format, color gating,
and concurrency story without API-shape noise.

See [master plan §5](../pr-a-logging-integration-refactor-plan.md#5-preset-format)
for the format requirements and sample output.

## Scope

1. **Add `internal/logging/handler.go`** with an unexported
   `prettyHandler` type and a package-private constructor
   `newPrettyHandler(w io.Writer, level slog.Leveler, addSource bool) slog.Handler`.
2. **Layout** matches the master plan §5.2: `HH:MM:SS.mmm  LVL  pkg/file.go:NN  msg  k=v ...`.
3. **Color gating** uses `golang.org/x/term.IsTerminal` against the
   writer when it is an `*os.File`; otherwise color is off. Resolved
   once at construction.
4. **Source trimming** strips the `github.com/tsumina/dango/` module
   prefix; falls back to `file:line` if the trim fails.
5. **Concurrency**: per-handler `sync.Mutex` on the writer so
   concurrent emits don't interleave. Use a `bytes.Buffer` pool for
   per-record formatting.
6. **`WithAttrs` / `WithGroup`** accumulate into the returned handler
   so `slog.Logger.With(...)` and group-prefixed keys (`g.b=2`) work
   as expected.

## Files added

- `internal/logging/handler.go`
- `internal/logging/handler_test.go`

## Files not touched

- `internal/logging/logging.go` — unchanged. The new handler is unused
  by the existing `New` in this PR; PR A-2 wires it in.
- Everything outside `internal/logging/`.

## Tests

Per `.github/instructions/implementation-and-tests.instructions.md`,
TDD first.

- `TestPrettyHandlerWritesSingleLine` — INFO record with attrs renders
  to `HH:MM:SS.mmm  INF  <src>  msg  k=v` (regex over a buffer sink,
  color off).
- `TestPrettyHandlerLevelFilter` — DEBUG record dropped when
  `Level=Info`.
- `TestPrettyHandlerWithAttrsAndGroups` —
  `logger.With("a",1).WithGroup("g").Info("x", "b", 2)` produces
  `a=1 g.b=2`.
- `TestPrettyHandlerSourceTrim` — record built with PC from this test
  file renders source as `internal/logging/handler_test.go:NN`, not
  the full module-qualified path.
- `TestPrettyHandlerSerializesConcurrentWrites` — 100 goroutines emit
  to a shared buffer; every line passes the single-line regex (no
  interleaving).
- `TestPrettyHandlerNoColorOnNonTTY` — buffer sink yields ANSI-free
  bytes even when level styling is configured.

## Acceptance

- `go test ./internal/logging/...` green.
- `go vet ./internal/logging/...` clean.
- `grep -rn "newPrettyHandler\\|prettyHandler" --include="*.go"`
  finds matches only inside `internal/logging/` (handler is private).
- No new module dependencies; only `lipgloss` (existing) and
  `golang.org/x/term` (existing).

## Notes for reviewer

- Color escapes are gated **at construction**, not per record, so
  switching writers after construction is intentionally not supported.
- The handler does **not** read environment variables. Sink/level
  decisions belong to the caller.
- `service=dango` is added by `slog.Logger.With(...)` in PR A-2's
  `NewLogger`, not by this handler. Keep the handler format-only.
