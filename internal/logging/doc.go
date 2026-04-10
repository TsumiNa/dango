// Package logging provides the shared structured logging setup used by dango
// commands and services.
//
// The package exists so that the CLI, orchestrator, runner, runtime, and
// executor layers all speak the same slog dialect. [Config] describes the
// externally visible logging knobs, [DefaultConfig] reads their defaults from
// the environment, [Config.BindFlags] exposes them on command-line flag sets,
// and [New] turns the resolved configuration into a logger plus an optional log
// file closer. Helper functions such as [From] and [Component] let downstream
// packages safely reuse or annotate loggers without each package having to
// rebuild handler configuration.
//
// The usual workflow starts in the CLI package: load [DefaultConfig], let the
// command bind or override flags, call [New], and pass the resulting logger
// into the packages that perform registry, planning, scheduling, or execution
// work. Those packages then derive component-scoped loggers with [Component] so
// one process-wide configuration still produces package-specific fields.
//
// This package deliberately owns configuration and handler construction, not
// lifecycle policy. It standardizes format selection, source reporting, and
// optional file teeing so the rest of the system can focus on emitting useful
// events instead of re-solving logger setup.
package logging
