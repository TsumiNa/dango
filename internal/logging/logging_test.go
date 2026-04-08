package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewJSONLoggerWritesStructuredOutput(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, closer, err := New(Config{
		Level:  "debug",
		Format: "json",
	}, &buffer)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if closer != nil {
		t.Fatalf("expected no file closer, got %T", closer)
	}

	logger.Info("hello", "component", "test")
	output := buffer.String()
	if !strings.Contains(output, `"msg":"hello"`) {
		t.Fatalf("output = %q, want message field", output)
	}
	if !strings.Contains(output, `"service":"dango"`) {
		t.Fatalf("output = %q, want service field", output)
	}
}

func TestNewRejectsUnsupportedLevel(t *testing.T) {
	t.Parallel()

	_, closer, err := New(Config{
		Level:  "trace",
		Format: "text",
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for unsupported level")
	}
	if closer != nil {
		t.Fatalf("expected nil closer on failure, got %T", closer)
	}
}
