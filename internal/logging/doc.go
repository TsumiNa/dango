// Package logging provides dango's preset slog logger and the single
// integration point through which the orchestrator, runner, and agent
// receive their lifecycle logger.
//
// The package exposes a small surface:
//
//   - [Config] — the only public knob set callers may tune. Only sink
//     (Output) and minimum level are configurable; the output format
//     is owned by the package and not exposed.
//   - [DefaultConfig] — discard-by-default configuration that callers
//     start from when they have no explicit log policy.
//   - [NewLogger] — builds the *slog.Logger from a Config, wires the
//     preset pretty handler, and annotates with the service=dango base
//     attribute. The returned logger is never nil.
//
// The intended wiring is one call at the top of the program:
//
//	logger := logging.NewLogger(logging.Config{
//	    Level:  slog.LevelInfo,
//	    Output: os.Stderr,
//	})
//	o := orchestrate.NewOrchestrator(orchestrate.WithLogger(logger))
//
// The orchestrator propagates the same logger to every runner it
// constructs and, transitively, to every agent each runner builds.
// Callers that do not wire a logger get the discard default — the
// logging.DefaultConfig sink is [io.Discard], so an unconfigured
// service emits no log output. This matches the "redirect to
// /dev/null" default behavior the rest of dango assumes.
//
// This package deliberately owns format and handler construction, not
// lifecycle policy. It does not read environment variables, does not
// bind flags, and does not own file lifetimes. A future CLI binary
// that wants envvar/flag-driven configuration owns that mapping and
// passes a built Config into [NewLogger].
package logging
