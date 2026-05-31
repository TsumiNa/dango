package logging

import (
	"io"
	"log/slog"
)

// Config controls the slog logger built by [NewLogger]. The format is
// intentionally not configurable; callers tune only the sink and the
// minimum level.
//
// The zero value is usable and produces a safe discard-backed logger
// (Level slog.LevelInfo, Output nil → [io.Discard], AddSource false).
// It is *not* identical to [DefaultConfig] — the only difference is
// AddSource, which is false in the zero value (Go's bool default) and
// true in [DefaultConfig] because the preset pretty handler is
// designed around showing a source column. Callers that want the
// source column with a fluent struct literal should either start from
// [DefaultConfig] or set AddSource explicitly.
type Config struct {
	// Level selects the minimum severity emitted by the logger. The
	// zero value (slog.LevelInfo) is the dango default.
	Level slog.Level

	// Output selects the log sink. A nil Output is treated as
	// [io.Discard] so callers that pass the zero Config still get a
	// safe, side-effect-free logger.
	//
	// The Output writer is shared with the logger after construction;
	// callers retain ownership and are responsible for closing any
	// file-backed writers themselves. Concurrent writes from derived
	// loggers are serialized by the handler.
	Output io.Writer

	// AddSource toggles source-location reporting. The Go zero value
	// is false (source column suppressed); [DefaultConfig] sets it to
	// true because the preset pretty handler is designed around
	// showing the source column. Set false to drop the column without
	// losing the rest of the layout.
	AddSource bool
}

// DefaultConfig returns the discard-by-default logging configuration:
// info-level, output to [io.Discard], source reporting on. Callers
// wanting log output point Output at [os.Stderr], an open file, or any
// other [io.Writer] they own.
func DefaultConfig() Config {
	return Config{
		Level:     slog.LevelInfo,
		Output:    io.Discard,
		AddSource: true,
	}
}

// NewLogger builds the dango slog logger from cfg.
//
// The returned logger always carries the service=dango base attribute
// and uses the package's preset pretty handler (see handler.go). The
// returned logger is never nil; a zero Config produces a usable
// discard-backed logger.
//
// NewLogger keeps a reference to cfg.Output through the handler;
// callers must keep that writer valid for the lifetime of the logger
// and close any file-backed writers themselves.
func NewLogger(cfg Config) *slog.Logger {
	output := cfg.Output
	if output == nil {
		output = io.Discard
	}
	return slog.New(newPrettyHandler(output, cfg.Level, cfg.AddSource)).With("service", "dango")
}
