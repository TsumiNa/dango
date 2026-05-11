package runner

import (
	"strings"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func TestMemoDocumentMarkdownRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	raw, err := (MemoDocument{
		ChannelHeader: streampkg.ChannelHeader{
			RunnerID:  "runner-1",
			CreatedAt: createdAt,
		},
		NodeID:    "node-1",
		SkillName: "planner",
		Path:      "memo/plan.md",
		Body:      "memo body",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	parsed, err := ParseMemoMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseMemoMarkdown: %v", err)
	}
	if parsed.Kind != streampkg.ChannelKindMemo || parsed.Version != MemoDocumentVersion {
		t.Fatalf("kind/version = %q/%d", parsed.Kind, parsed.Version)
	}
	if parsed.RunnerID != "runner-1" || parsed.NodeID != "node-1" || parsed.Path != "memo/plan.md" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !parsed.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", parsed.CreatedAt, createdAt)
	}
	if parsed.Body != "memo body" {
		t.Fatalf("body = %q, want %q", parsed.Body, "memo body")
	}
}

func TestMemoDocumentMarkdownRejectsMissingFields(t *testing.T) {
	_, err := (MemoDocument{ChannelHeader: streampkg.ChannelHeader{RunnerID: "runner-1"}}).Markdown()
	if err == nil || !strings.Contains(err.Error(), "node_id must not be empty") {
		t.Fatalf("Markdown error = %v, want missing node_id", err)
	}
}

func TestParseMemoMarkdownRejectsLegacyKind(t *testing.T) {
	raw := `---
kind: dango.memo
version: 1
runner_id: runner-1
node_id: node-1
path: memo/plan.md
created_at: 2026-05-01T12:00:00Z
---

memo body`
	_, err := ParseMemoMarkdown(raw)
	if err == nil || !strings.Contains(err.Error(), `want "memo"`) {
		t.Fatalf("ParseMemoMarkdown error = %v, want legacy kind rejection", err)
	}
}

func TestParseMemoMarkdownRejectsUnsupportedKind(t *testing.T) {
	raw := `---
kind: dango.exchange
version: 1
runner_id: runner-1
node_id: node-1
path: memo/plan.md
created_at: 2026-05-01T12:00:00Z
---

memo body`
	_, err := ParseMemoMarkdown(raw)
	if err == nil || !strings.Contains(err.Error(), `want "memo"`) {
		t.Fatalf("ParseMemoMarkdown error = %v, want kind mismatch", err)
	}
}
