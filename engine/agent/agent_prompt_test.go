package agent

import (
	"strings"
	"testing"
)

func TestAppendMarkdownSection(t *testing.T) {
	got := appendMarkdownSection("  # Base\n", "Details", "  body  \n")
	want := "# Base\n\n## Details\n\nbody"
	if got != want {
		t.Fatalf("appendMarkdownSection = %q, want %q", got, want)
	}
}

func TestAppendMarkdownSectionSkipsEmptyBody(t *testing.T) {
	got := appendMarkdownSection("  # Base\n", "Details", " \n")
	if got != "# Base" {
		t.Fatalf("appendMarkdownSection empty body = %q", got)
	}
	if strings.Contains(got, "Details") {
		t.Fatalf("appendMarkdownSection added heading for empty body: %q", got)
	}
}
