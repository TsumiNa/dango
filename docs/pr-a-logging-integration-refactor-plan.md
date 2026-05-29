# PR A — Logging Integration Refactor (Implementation Plan)

Status: Drafted 2026-05-26. Tracked from
`docs/deferred-refactor-tracker-memo.md` "PR A".

This plan replaces the original "open questions" block in the memo with a
concrete design and a step-by-step implementation order. When work
starts, this document is the source of truth; the memo can be updated to
point here.

**Per-PR breakdown.** Per-PR scope, files touched, tests, and
acceptance criteria live in
[`pr-a-logging-integration-refactor-steps/`](./pr-a-logging-integration-refactor-steps/00-overview.md):

- [`10-pretty-handler.md`](./pr-a-logging-integration-refactor-steps/10-pretty-handler.md) — PR A-1, additive handler.
- [`20-logging-package-api.md`](./pr-a-logging-integration-refactor-steps/20-logging-package-api.md) — PR A-2, `internal/logging` API rewrite.
- [`30-engine-wiring-and-migration.md`](./pr-a-logging-integration-refactor-steps/30-engine-wiring-and-migration.md) — PR A-3, engine rename cascade + caller migration.

This master document is the **shared design reference** that the per-PR
files link back to: goal, non-goals, API surface, wiring flow, format
spec, sample output, and items explicitly deferred. Per-PR execution
detail (file lists, test cases, implementation order) lives in the
sub-files and is not duplicated here.

## 1. Goal

Make `internal/logging` the single, opt-in integration path for all
runtime logging in dango. Callers wire one pre-configured `*slog.Logger`
into the `Orchestrator`; the orchestrator threads it down to runners and
agents. The output format is owned by `logging` and is not caller-
configurable; only sink (discard / stderr / file) and level are.

Default behavior matches "redirect to `/dev/null`": no log output unless
the caller explicitly asks for it.

## 2. Non-goals

- No `init()`-time logger installation. Wiring is explicit at construction.
- No backwards compatibility shims. `WithOrchestratorLogger` and
  `WithAgentLogger` are removed outright (per
  `.github/instructions/in-branch-api-compat.instructions.md`).
- No per-package logger overrides via options. Subsystems still annotate
  with `logging.Component(...)` from the single root logger.
- No structured-event emission changes. PR A only touches setup and
  threading; existing `logger.Info(...)` call sites keep their fields.

## 3. Final API surface

### 3.1 `internal/logging`

```go
package logging

// Config is the only public knob set callers may tune. Format is
// intentionally not configurable.
type Config struct {
    // Level selects minimum severity. Defaults to slog.LevelInfo.
    Level slog.Level

    // Output selects the sink. Defaults to io.Discard (the
    // "/dev/null" default). Use os.Stderr, an *os.File, or any
    // io.Writer the caller owns.
    Output io.Writer

    // AddSource toggles source-location reporting. Defaults to true,
    // because the preset handler is designed around showing it. Callers
    // that want a quieter format can set this to false.
    AddSource bool
}

// DefaultConfig returns the discard-by-default configuration.
func DefaultConfig() Config

// NewLogger constructs the preset logger from cfg. The returned logger
// always carries service=dango as a base attribute and uses the dango
// pretty handler (see §5).
//
// NewLogger never returns nil; passing the zero Config yields the
// discard logger.
func NewLogger(cfg Config) *slog.Logger

// OpenFileSink opens path in append mode, creating parent directories
// as needed, and returns an io.WriteCloser. Callers are responsible for
// closing it once the orchestrator is torn down. This is a convenience
// for the common "log to artifacts/<run>/log" pattern; callers may
// build their own sinks instead.
func OpenFileSink(path string) (io.WriteCloser, error)

// From returns logger or, if nil, the discard logger.
func From(logger *slog.Logger) *slog.Logger

// Component annotates logger with component=name. Used by sub-packages
// so the single root logger still produces package-scoped fields.
func Component(logger *slog.Logger, name string) *slog.Logger
```

Removed from the existing `Config`:

- `Format string` — the format is fixed (see §5).
- `File string` — replaced by the caller-owned `Output` writer and the
  `OpenFileSink` helper, so the orchestrator does not own file lifetime.
- `BindFlags` — moves to `cmd/` (or wherever the binary lives) when a
  CLI is added later. The library does not bind flags.
- `Format`-related env vars. `DANGO_LOG_LEVEL` and `DANGO_LOG_SOURCE`
  may be re-introduced in `cmd/`, but `internal/logging` itself stops
  reading the environment.

### 3.2 `Orchestrator`

```go
// WithLogger installs logger as the Orchestrator's lifecycle logger.
// The same logger is propagated to every Runner the Orchestrator
// constructs and, transitively, to every Agent each Runner builds.
//
// The Orchestrator keeps a reference to logger. slog.Logger values are
// safe for concurrent use; callers that wrap a handler with mutable
// state are responsible for that handler's synchronization.
//
// If WithLogger is not used, the Orchestrator runs with the discard
// logger from logging.DefaultConfig.
func WithLogger(logger *slog.Logger) OrchestratorOption
```

`WithOrchestratorLogger` is removed.

### 3.3 `runner.Runner`

`WithLogger` already exists with the right signature; its doc comment
gains a line stating the logger is normally injected by the orchestrator
and a direct `runner.WithLogger(...)` call is the test-only path.

### 3.4 `Agent`

```go
// WithLogger installs logger as the Agent's lifecycle logger. The
// orchestrator-built agents receive the orchestrator's logger
// automatically; tests use this option to inject a buffer-backed
// logger.
func WithLogger(logger *slog.Logger) AgentOption
```

`WithAgentLogger` is renamed to `WithLogger` (engine package; no
collision because the option type is `AgentOption`).

## 4. Wiring flow

```
caller -> orchestrate.NewOrchestrator(WithLogger(L)) -> Orchestrator{logger:L}
                                                       |
                                                       v
   newRunnerFromPlan(..., logger=L, ...) -> runner.New(runner.WithLogger(L))
                                                       |
                                                       v
   buildPlanNodes(logger=L, ...) -> engine.NewAgent(..., WithLogger(L), ...)
```

- The orchestrator's logger is the only logger the caller installs.
- The orchestrator passes `o.logger` (never `slog.Default()`) to
  `newRunnerFromPlan` and `buildPlanNodes`.
- If `WithLogger` was never called, `o.logger == logging.NewLogger(logging.DefaultConfig())`,
  i.e. the discard logger. Downstream code can call methods on it
  freely — it never panics and emits nothing.
- Subsystems that want a component field call
  `logging.Component(o.logger, "engine.queue")` etc. at their own entry
  points. PR A does not retrofit this everywhere; it only ensures the
  hook is consistent.

## 5. Preset format

### 5.1 Requirements

- Human-readable on a terminal. Modern, single-line by default.
- Always shows source location (`file:line`) when `AddSource=true`.
- Carries one base attribute `service=dango` plus any
  `component=<pkg>` set by callers.
- Stable column-ish layout so scrolling logs read consistently.
- No new third-party dependency. The repo already vendors
  `github.com/charmbracelet/lipgloss` and `golang.org/x/term`; both are
  enough.

### 5.2 Layout

```
HH:MM:SS.mmm  LVL  pkg/file.go:123  message     key=value key=value
```

- Timestamp: local time, millisecond precision. Width 12.
- Level: 3-letter uppercase (`DBG`, `INF`, `WRN`, `ERR`), color-coded.
- Source: full module-relative path with the
  `github.com/tsumina/dango/` prefix stripped (e.g.
  `internal/engine/runner/runner.go:487`). Falls back to a best-effort
  trim at the last `/dango/` segment, then to the raw frame path if
  neither matches. Source segment is omitted entirely when the record
  has no PC.
- Message: CR/LF are backslash-escaped (`\n`, `\r`) so a multi-line
  message stays one physical log line. Callers wanting structured
  multi-line context should put it in attribute values.
- Attributes: rendered after the message, space-separated `k=v`,
  with values quoted (Go-syntax) only when they contain spaces, equals
  signs, double quotes, or control chars; empty strings render as
  `key=""`.

### 5.3 Color policy

- Colors are applied via a `lipgloss.Renderer` **bound to the resolved
  `Output` writer** (not the package-global renderer), so the profile
  decision targets the same sink `detectColor` inspected. Profile is
  forced to `termenv.ANSI256` when the writer is a TTY-backed `*os.File`
  and to `termenv.Ascii` otherwise; the same handler decides once at
  construction.
- Level palette: `DBG` dim grey, `INF` blue, `WRN` yellow, `ERR` red
  bold. Source path uses a subtle dim style; attribute keys are dim,
  values inherit terminal default.
- File sinks and any non-`*os.File` writers never receive ANSI escapes
  because the Ascii profile makes `Render` a no-op.

### 5.4 Handler implementation sketch

```go
// internal/logging/handler.go
type prettyHandler struct {
    mu        *sync.Mutex
    w         io.Writer
    level     slog.Leveler
    addSource bool
    attrs     []slog.Attr
    groups    []string
    styles    levelStyles // lipgloss styles bound to a writer-scoped renderer
}

func newPrettyHandler(w io.Writer, level slog.Leveler, addSource bool) slog.Handler
func (h *prettyHandler) Enabled(_ context.Context, lvl slog.Level) bool
func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error
func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler
func (h *prettyHandler) WithGroup(name string) slog.Handler
```

Behavioral notes:

- `Handle` builds the line into a pooled `bytes.Buffer`, writes it in
  one `Write`. Concurrent emits are serialized by a shared `*sync.Mutex`
  that derived handlers (`WithAttrs`/`WithGroup`) inherit so all writers
  to the same sink serialize together.
- Source resolution uses `runtime.CallersFrames` on `r.PC`. Trim to the
  module's repo root by string-stripping the known prefix
  `github.com/tsumina/dango/`; falls back to a best-effort
  `LastIndex("/dango/")` trim, then to the raw path.
- `writeAttr` calls `a.Value.Resolve()` before inspecting `Kind` so
  custom types that implement `slog.LogValuer` (including ones that
  resolve to groups) are expanded per the slog handler contract.
- `WithAttrs`/`WithGroup` accumulate into the returned handler so
  `slog.Logger.With(...)` works as expected. Bound attrs are
  pre-prefixed with the groups active at bind time; later `WithGroup`
  calls do not retroactively re-prefix earlier-bound attrs.

## 6. Files, tests, implementation order

Moved out of this document. Each per-PR file in
[`pr-a-logging-integration-refactor-steps/`](./pr-a-logging-integration-refactor-steps/00-overview.md)
owns its own files-touched list, test plan, and acceptance criteria.
Cross-PR ordering lives in
[`00-overview.md`](./pr-a-logging-integration-refactor-steps/00-overview.md).

## 7. Sample output

For a successful runner-start sequence with `Output=os.Stderr` on a
TTY (colors stripped here for plain text):

```
14:02:11.482  INF  internal/engine/orchestrator.go:128  starting runner runner_id=ru_4f3a service=dango
14:02:11.483  INF  internal/engine/runner/runner.go:487  Starting execution engine event loop... runner_id=ru_4f3a component=runner service=dango
14:02:11.501  DBG  internal/engine/agent.go:131  Creating a new Agent node_id=plan-0 skill=elevation_lookup service=dango
14:02:12.044  WRN  internal/engine/runner/runner.go:612  retrying transient error node_id=plan-0 attempt=2 service=dango
14:02:12.811  ERR  internal/engine/runner/runner.go:652  Node execution failed, terminating chain. node_id=plan-0 error="bind skill: ..." service=dango
```

The same emit, written to a file sink (no TTY), is byte-identical minus
the ANSI escapes.

## 8. Open items deferred past PR A

These are out of scope for PR A and tracked here so they are not
re-litigated during implementation:

- **CLI binary integration.** When a `cmd/dango` (or equivalent)
  binary lands, that binary owns the flag/env mapping that produces a
  `logging.Config`. It is not the library's job.
- **OpenTelemetry / structured event bridge.** Existing
  `stream_events.jsonl` and OTLP code paths are untouched. A future
  PR may add a `slog.Handler` that fans out to OTLP; this plan does
  not require it.
- **Per-component levels.** If a noisy package later needs a lower
  level than the root, the right answer is a thin `slog.Handler`
  wrapper, not a new `Config` field.
- **Log rotation.** `OpenFileSink` opens with `O_APPEND` and returns
  a plain file. Callers needing rotation wrap the writer themselves.
