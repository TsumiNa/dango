package runner

import (
	"strings"
	"testing"
	"time"
)

func TestHandoffDocMarkdownRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	raw, err := (HandoffDoc{
		RunnerID:  "runner-1",
		FromNode:  "node-a",
		ToNodes:   []string{"node-b", "node-c"},
		Intent:    "continue",
		CreatedAt: createdAt,
		Artifacts: []HandoffArtifact{{
			Path:        "outbox/artifacts/report.md",
			Type:        "file",
			Description: "report",
		}},
		Body: "handoff body",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	parsed, err := ParseHandoffMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseHandoffMarkdown: %v", err)
	}
	if parsed.Kind != HandoffDocKind || parsed.Version != HandoffDocVersion {
		t.Fatalf("kind/version = %q/%d", parsed.Kind, parsed.Version)
	}
	if parsed.RunnerID != "runner-1" || parsed.FromNode != "node-a" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if len(parsed.ToNodes) != 2 || parsed.ToNodes[0] != "node-b" || parsed.ToNodes[1] != "node-c" {
		t.Fatalf("to_nodes = %#v", parsed.ToNodes)
	}
	if len(parsed.Artifacts) != 1 || parsed.Artifacts[0].Path != "outbox/artifacts/report.md" {
		t.Fatalf("artifacts = %#v", parsed.Artifacts)
	}
	if !parsed.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", parsed.CreatedAt, createdAt)
	}
	if parsed.Body != "handoff body" {
		t.Fatalf("body = %q, want %q", parsed.Body, "handoff body")
	}
}

func TestHandoffDocMarkdownRejectsMissingFields(t *testing.T) {
	_, err := (HandoffDoc{RunnerID: "runner-1", FromNode: "node-a"}).Markdown()
	if err == nil || !strings.Contains(err.Error(), "to_nodes must not be empty") {
		t.Fatalf("Markdown error = %v, want missing to_nodes", err)
	}
}

func TestHandoffDocMarkdownRejectsWhitespacePaddedToNodes(t *testing.T) {
	_, err := (HandoffDoc{
		RunnerID: "runner-1",
		FromNode: "node-a",
		ToNodes:  []string{" node-b "},
	}).Markdown()
	if err == nil || !strings.Contains(err.Error(), "leading or trailing whitespace") {
		t.Fatalf("Markdown error = %v, want whitespace to_nodes rejection", err)
	}
}

func TestParseHandoffMarkdownRejectsWhitespacePaddedToNodes(t *testing.T) {
	raw := `---
kind: dango.handoff_doc
version: 1
runner_id: runner-1
from_node: node-a
to_nodes:
  - " node-b "
created_at: 2026-05-01T12:00:00Z
---

handoff body`
	_, err := ParseHandoffMarkdown(raw)
	if err == nil || !strings.Contains(err.Error(), "leading or trailing whitespace") {
		t.Fatalf("ParseHandoffMarkdown error = %v, want whitespace to_nodes rejection", err)
	}
}

func TestHandoffDocMarkdownRejectsUnsafeArtifactPath(t *testing.T) {
	_, err := (HandoffDoc{
		RunnerID: "runner-1",
		FromNode: "node-a",
		ToNodes:  []string{"node-b"},
		Artifacts: []HandoffArtifact{{
			Path: "../secret.txt",
		}},
	}).Markdown()
	if err == nil || !strings.Contains(err.Error(), "must not escape workspace") {
		t.Fatalf("Markdown error = %v, want unsafe artifact path rejection", err)
	}
}

func TestHandoffDocMarkdownRejectsCollapsedTraversalArtifactPath(t *testing.T) {
	_, err := (HandoffDoc{
		RunnerID: "runner-1",
		FromNode: "node-a",
		ToNodes:  []string{"node-b"},
		Artifacts: []HandoffArtifact{{
			Path: "foo/..",
		}},
	}).Markdown()
	if err == nil || !strings.Contains(err.Error(), "must not escape workspace") {
		t.Fatalf("Markdown error = %v, want collapsed traversal artifact path rejection", err)
	}
}

func TestParseHandoffMarkdownRejectsAbsoluteArtifactPath(t *testing.T) {
	raw := `---
kind: dango.handoff_doc
version: 1
runner_id: runner-1
from_node: node-a
to_nodes:
  - node-b
created_at: 2026-05-01T12:00:00Z
artifacts:
  - path: /tmp/report.md
---

handoff body`
	_, err := ParseHandoffMarkdown(raw)
	if err == nil || !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("ParseHandoffMarkdown error = %v, want absolute artifact path rejection", err)
	}
}

func TestParseHandoffMarkdownRejectsLegacyExchangeKind(t *testing.T) {
	raw := `---
kind: dango.exchange
version: 1
runner_id: runner-1
from_node: node-a
to_nodes:
  - node-b
created_at: 2026-05-01T12:00:00Z
---

legacy handoff body`
	_, err := ParseHandoffMarkdown(raw)
	if err == nil || !strings.Contains(err.Error(), "want \""+HandoffDocKind+"\"") {
		t.Fatalf("ParseHandoffMarkdown error = %v, want kind mismatch", err)
	}
}
