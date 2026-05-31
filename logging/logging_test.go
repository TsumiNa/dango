package logging

import (
	"bytes"
	"io"
	"log/slog"
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

func TestNewLoggerSuppressesSourceWhenAddSourceFalse(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := NewLogger(Config{Level: slog.LevelInfo, Output: &buf, AddSource: false})
	l.Info("hello", "k", "v")

	out := buf.String()
	if strings.Contains(out, ".go:") {
		t.Fatalf("expected no source column for AddSource=false, got %q", out)
	}
	if !strings.Contains(out, "INF") || !strings.Contains(out, "hello") || !strings.Contains(out, "k=v") {
		t.Fatalf("expected level/message/attrs to still render, got %q", out)
	}
}

func TestZeroConfigDiffersFromDefaultConfigOnAddSource(t *testing.T) {
	t.Parallel()

	// Locks the doc-comment contract: zero Config has AddSource=false
	// (Go bool default); DefaultConfig has AddSource=true. The reviewer
	// (PR #101) caught an earlier doc that wrongly claimed parity.
	if (Config{}).AddSource {
		t.Fatal("Config{}.AddSource = true, want false (Go bool zero)")
	}
	if !DefaultConfig().AddSource {
		t.Fatal("DefaultConfig().AddSource = false, want true")
	}
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
