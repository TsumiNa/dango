package logging

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigIsDiscard(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	if cfg.Output != io.Discard {
		t.Fatalf("DefaultConfig().Output = %v, want io.Discard", cfg.Output)
	}
	if cfg.Level != slog.LevelInfo {
		t.Fatalf("DefaultConfig().Level = %v, want Info", cfg.Level)
	}
	if !cfg.AddSource {
		t.Fatalf("DefaultConfig().AddSource = false, want true (preset handler is designed around showing source)")
	}

	// Smoke: NewLogger with the default config must not panic and must not
	// produce observable output (no stderr/stdout side effects).
	l := NewLogger(cfg)
	if l == nil {
		t.Fatal("NewLogger(DefaultConfig()) returned nil")
	}
	l.Info("hello", "k", "v")
	l.Warn("warn")
	l.Error("err")
}

func TestNewLoggerCarriesServiceAttr(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := NewLogger(Config{Level: slog.LevelInfo, Output: &buf, AddSource: false})
	l.Info("hello")

	if !strings.Contains(buf.String(), "service=dango") {
		t.Fatalf("expected service=dango in output, got %q", buf.String())
	}
}

func TestNewLoggerUsesPrettyHandler(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := NewLogger(Config{Level: slog.LevelInfo, Output: &buf, AddSource: true})
	l.Info("hello", "k", "v")

	out := strings.TrimRight(buf.String(), "\n")
	if !singleLineRE.MatchString(out) {
		t.Fatalf("output %q does not match pretty handler layout (NewLogger should wire prettyHandler)", out)
	}
}

func TestNewLoggerNilOutputFallsBackToDiscard(t *testing.T) {
	t.Parallel()

	// Passing a zero Config (nil Output) should not panic and should
	// behave like the discard default.
	l := NewLogger(Config{})
	if l == nil {
		t.Fatal("NewLogger(Config{}) returned nil")
	}
	l.Info("hello")
}

func TestNewLoggerHonorsCustomLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := NewLogger(Config{Level: slog.LevelWarn, Output: &buf, AddSource: false})
	l.Info("dropped")
	l.Warn("kept")

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Fatalf("Info record should have been filtered at Warn level, got %q", out)
	}
	if !strings.Contains(out, "kept") {
		t.Fatalf("Warn record should have been emitted, got %q", out)
	}
}

func TestComponentAddsField(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	base := NewLogger(Config{Level: slog.LevelInfo, Output: &buf, AddSource: false})
	Component(base, "ru").Info("hello")

	if !strings.Contains(buf.String(), "component=ru") {
		t.Fatalf("expected component=ru in output, got %q", buf.String())
	}
}

func TestFromReturnsDiscardForNil(t *testing.T) {
	t.Parallel()

	l := From(nil)
	if l == nil {
		t.Fatal("From(nil) returned nil; expected a usable discard logger")
	}
	l.Info("should not panic")
}

func TestFromReturnsInputWhenNonNil(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	base := NewLogger(Config{Level: slog.LevelInfo, Output: &buf, AddSource: false})
	if got := From(base); got != base {
		t.Fatalf("From(base) returned a different logger pointer; expected pass-through")
	}
}

func TestOpenFileSinkCreatesParents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c.log")

	sink, err := OpenFileSink(path)
	if err != nil {
		t.Fatalf("OpenFileSink(%q): %v", path, err)
	}
	if sink == nil {
		t.Fatal("OpenFileSink returned nil sink on success")
	}

	if _, err := sink.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("file contents = %q, want %q", got, "hello\n")
	}
}

func TestOpenFileSinkAppendsExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "existing.log")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sink, err := OpenFileSink(path)
	if err != nil {
		t.Fatalf("OpenFileSink(%q): %v", path, err)
	}
	if _, err := sink.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "first\nsecond\n" {
		t.Fatalf("file contents = %q, want %q", got, "first\nsecond\n")
	}
}

func TestOpenFileSinkFailsOnEmptyPath(t *testing.T) {
	t.Parallel()

	sink, err := OpenFileSink("")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
	if sink != nil {
		t.Fatalf("expected nil sink on error, got %T", sink)
	}
}
