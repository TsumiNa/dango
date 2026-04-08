// Package logging provides shared structured logging setup for dango services.
//
// The package translates CLI flags and environment variables into a configured
// slog logger. It standardizes output format, level handling, optional file
// teeing, and component tagging so logs are consistent across orchestrator and
// executor paths.
package logging
