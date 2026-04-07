package spec

import (
	"strings"
	"testing"
	"time"
)

func TestRenderAndParseHandoff(t *testing.T) {
	t.Parallel()

	nextTool := "pdf-generator"
	input := Handoff{
		Metadata: HandoffMetadata{
			TaskID:      "task-123",
			Tool:        "markdown-prep",
			Status:      HandoffStatusCompleted,
			OutputFiles: []string{"report.md", "assets/chart.png"},
			NextTool:    &nextTool,
			Timestamp:   time.Date(2026, 4, 8, 3, 4, 5, 0, time.UTC),
		},
		Body: "## Description\n\nGenerated markdown and charts.",
	}

	payload, err := RenderHandoff(input)
	if err != nil {
		t.Fatalf("RenderHandoff() error = %v", err)
	}

	parsed, err := ParseHandoff(payload)
	if err != nil {
		t.Fatalf("ParseHandoff() error = %v", err)
	}
	frontmatter, err := ExtractHandoffFrontmatter(payload)
	if err != nil {
		t.Fatalf("ExtractHandoffFrontmatter() error = %v", err)
	}

	if got, want := parsed.Metadata.TaskID, input.Metadata.TaskID; got != want {
		t.Fatalf("parsed.Metadata.TaskID = %q, want %q", got, want)
	}
	if got, want := parsed.Metadata.Tool, input.Metadata.Tool; got != want {
		t.Fatalf("parsed.Metadata.Tool = %q, want %q", got, want)
	}
	if got, want := parsed.Metadata.Status, input.Metadata.Status; got != want {
		t.Fatalf("parsed.Metadata.Status = %q, want %q", got, want)
	}
	if !strings.Contains(parsed.Body, "Generated markdown and charts.") {
		t.Fatalf("parsed body = %q, want description text", parsed.Body)
	}
	if !strings.Contains(string(frontmatter), "task_id: task-123") {
		t.Fatalf("frontmatter = %q, want task_id entry", string(frontmatter))
	}
}
