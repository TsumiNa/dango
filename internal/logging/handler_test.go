package logging

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// singleLineRE matches one pretty-formatted record without ANSI escapes:
// "HH:MM:SS.mmm  LVL  <src>:<line>  <message and attrs>".
var singleLineRE = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3}\s+(DBG|INF|WRN|ERR)\s+\S+:\d+\s+\S.*$`)

func TestPrettyHandlerWritesSingleLine(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := slog.New(newPrettyHandler(&buf, slog.LevelInfo, true))
	l.Info("hello", "k", "v")

	out := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("expected single-line output, got %q", out)
	}
	if !singleLineRE.MatchString(out) {
		t.Fatalf("output %q does not match expected layout", out)
	}
	if !strings.Contains(out, "INF") {
		t.Fatalf("expected INF level token in %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("expected message in %q", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("expected k=v attribute in %q", out)
	}
}

func TestPrettyHandlerLevelFilter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := slog.New(newPrettyHandler(&buf, slog.LevelInfo, true))
	l.Debug("nope")

	if buf.Len() != 0 {
		t.Fatalf("expected no output for Debug at Info level, got %q", buf.String())
	}
}

func TestPrettyHandlerWithAttrsAndGroups(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := slog.New(newPrettyHandler(&buf, slog.LevelInfo, false)).With("a", 1).WithGroup("g")
	l.Info("x", "b", 2)

	out := buf.String()
	if !strings.Contains(out, "a=1") {
		t.Fatalf("expected a=1 (no group prefix on pre-group attr) in %q", out)
	}
	if !strings.Contains(out, "g.b=2") {
		t.Fatalf("expected g.b=2 (group-prefixed record attr) in %q", out)
	}
}

func TestPrettyHandlerSourceTrim(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	h := newPrettyHandler(&buf, slog.LevelInfo, true)

	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "src", pcs[0])
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "internal/logging/handler_test.go:") {
		t.Fatalf("expected trimmed source path containing internal/logging/handler_test.go, got %q", out)
	}
	if strings.Contains(out, "github.com/tsumina/dango/") {
		t.Fatalf("output retained module-qualified prefix: %q", out)
	}
	if strings.Contains(out, "/Users/") || strings.Contains(out, "/home/") {
		t.Fatalf("output retained absolute filesystem prefix: %q", out)
	}
}

func TestPrettyHandlerSerializesConcurrentWrites(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := slog.New(newPrettyHandler(&buf, slog.LevelInfo, true))

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			l.Info("concurrent", "i", i)
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != goroutines {
		t.Fatalf("expected %d lines, got %d", goroutines, len(lines))
	}
	for j, line := range lines {
		if !singleLineRE.MatchString(line) {
			t.Fatalf("line %d malformed (suggests interleaving): %q", j, line)
		}
	}
}

func TestPrettyHandlerNoColorOnNonTTY(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := slog.New(newPrettyHandler(&buf, slog.LevelInfo, true))
	l.Info("hello", "k", "v")
	l.Warn("warn")
	l.Error("err")

	if strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("expected ANSI-free output for non-TTY writer, got %q", buf.String())
	}
}

func TestPrettyHandlerQuotesValuesWithSpaces(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := slog.New(newPrettyHandler(&buf, slog.LevelInfo, false))
	l.Info("m", "msg", "hello world", "plain", "ok")

	out := buf.String()
	if !strings.Contains(out, `msg="hello world"`) {
		t.Fatalf("expected quoted value for spaces, got %q", out)
	}
	if !strings.Contains(out, "plain=ok") {
		t.Fatalf("expected unquoted plain value, got %q", out)
	}
}
