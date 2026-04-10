package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config controls the process-wide slog logger setup shared by dango commands
// and services.
//
// CLI code typically starts from [DefaultConfig], optionally exposes these
// fields through [Config.BindFlags], and then passes the resolved value to
// [New]. The same Config is intended to drive both control-plane and
// executor-side logging so all components share one service-level logging
// policy.
type Config struct {
	// Level selects the minimum severity emitted by the logger.
	Level string
	// Format selects the output encoding. Supported values are text and json.
	Format string
	// File optionally appends logs to a file in addition to stderr.
	File string
	// AddSource enables source location reporting in slog handlers.
	AddSource bool
}

// DefaultConfig returns the logging configuration derived from environment
// variables and repository defaults.
//
// The returned value is suitable for further mutation through CLI flags before
// it is passed to [New].
func DefaultConfig() Config {
	return Config{
		Level:     firstNonEmpty(os.Getenv("DANGO_LOG_LEVEL"), "info"),
		Format:    firstNonEmpty(os.Getenv("DANGO_LOG_FORMAT"), "text"),
		File:      strings.TrimSpace(os.Getenv("DANGO_LOG_FILE")),
		AddSource: parseBoolEnv(os.Getenv("DANGO_LOG_SOURCE")),
	}
}

type flagBinder interface {
	StringVar(p *string, name string, value string, usage string)
	BoolVar(p *bool, name string, value bool, usage string)
}

// BindFlags exposes Config fields on fs.
//
// BindFlags is a no-op when c or fs is nil. The flags mutate the receiver in
// place so later calls to [New] see the final resolved configuration after the
// usual environment-defaults-then-flags layering used by dango commands.
func (c *Config) BindFlags(fs flagBinder) {
	if c == nil || fs == nil {
		return
	}

	fs.StringVar(&c.Level, "log-level", c.Level, "log level: debug|info|warn|error")
	fs.StringVar(&c.Format, "log-format", c.Format, "log format: text|json")
	fs.StringVar(&c.File, "log-file", c.File, "optional log file path")
	fs.BoolVar(&c.AddSource, "log-source", c.AddSource, "include source locations in logs")
}

// New constructs the shared slog logger used by dango services.
//
// New normalizes the requested level and format, optionally tees output to a
// log file, auto-enables source locations for debug-level logging, and always
// annotates the returned logger with the service=dango field. When cfg.File is
// set, New also returns a closer for the opened log file, and the caller is
// responsible for closing it.
func New(cfg Config, stderr io.Writer) (*slog.Logger, io.Closer, error) {
	writer := io.Writer(stderr)
	if writer == nil {
		writer = io.Discard
	}

	var closer io.Closer
	if strings.TrimSpace(cfg.File) != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0o755); err != nil {
			return nil, nil, fmt.Errorf("create log directory for %q: %w", cfg.File, err)
		}

		file, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file %q: %w", cfg.File, err)
		}

		writer = io.MultiWriter(writer, file)
		closer = file
	}

	level, err := parseLevel(cfg.Level)
	if err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, nil, err
	}

	if !cfg.AddSource && level <= slog.LevelDebug {
		cfg.AddSource = true
	}

	options := &slog.HandlerOptions{
		AddSource: cfg.AddSource,
		Level:     level,
	}

	var handler slog.Handler
	switch normalizeFormat(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(writer, options)
	case "text":
		handler = slog.NewTextHandler(writer, options)
	default:
		if closer != nil {
			_ = closer.Close()
		}
		return nil, nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}

	return slog.New(handler).With("service", "dango"), closer, nil
}

// From returns logger when it is non-nil, or a discard logger otherwise.
//
// It is the safe entrypoint used by helper functions such as [Component] when
// callers may not have provided a logger yet.
func From(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
}

// Component annotates logger with the provided component name.
//
// Component never returns nil and is the standard way for packages to derive a
// subsystem-specific logger from the process-wide base logger.
func Component(logger *slog.Logger, component string) *slog.Logger {
	return From(logger).With("component", component)
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", value)
	}
}

func normalizeFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "text", "console":
		return "text"
	case "json":
		return "json"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func parseBoolEnv(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
