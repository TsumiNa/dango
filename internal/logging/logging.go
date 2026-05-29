package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	// callers retain ownership and are responsible for closing
	// file-backed writers (see [OpenFileSink]). Concurrent writes from
	// derived loggers are serialized by the handler.
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
// and close any file-backed writers themselves (see [OpenFileSink]).
func NewLogger(cfg Config) *slog.Logger {
	output := cfg.Output
	if output == nil {
		output = io.Discard
	}
	return slog.New(newPrettyHandler(output, cfg.Level, cfg.AddSource)).With("service", "dango")
}

// OpenFileSink opens path in append-write mode, creating parent
// directories as needed, and returns the file as an [io.WriteCloser].
//
// The caller owns the returned writer and must close it once the
// logger using it is torn down. OpenFileSink is a convenience for the
// common "log to <artifacts>/<run>/log" pattern; callers that need
// rotation, compression, or fan-out should build their own sink and
// pass it directly via [Config.Output].
func OpenFileSink(path string) (io.WriteCloser, error) {
	if path == "" {
		return nil, fmt.Errorf("logging: file sink path must be non-empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("logging: create log directory for %q: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("logging: open log file %q: %w", path, err)
	}
	return file, nil
}

// From returns logger when it is non-nil, or the discard logger from
// [DefaultConfig] otherwise.
//
// It is the safe entry point used by helpers such as [Component] when
// callers may not have wired a logger yet.
func From(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return NewLogger(DefaultConfig())
}

// Component annotates logger with component=name. Sub-packages derive
// a subsystem-scoped logger from the single process-wide root logger
// by calling Component at their entry points.
//
// Component never returns nil; a nil logger falls through [From].
func Component(logger *slog.Logger, name string) *slog.Logger {
	return From(logger).With("component", name)
}
