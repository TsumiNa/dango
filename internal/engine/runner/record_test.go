package runner

import (
	"testing"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func TestStoredRunnerEventStoresHandoffMarkdownAsMarkdownText(t *testing.T) {
	raw, err := (HandoffDoc{
		ChannelHeader: streampkg.ChannelHeader{RunnerID: "runner-1"},
		FromNode:      "node-1",
		ToNodes:       []string{"node-2"},
		Body:          "output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	stored := newStoredRunnerEvent(RunnerEvent{
		Type:   EventNodeCompleted,
		NodeID: "node-1",
		Data:   raw,
	})
	if stored.DataEncoding != "markdown" {
		t.Fatalf("DataEncoding = %q, want markdown", stored.DataEncoding)
	}
	if stored.DataText != raw {
		t.Fatalf("DataText = %q, want raw markdown", stored.DataText)
	}
	if len(stored.DataJSON) != 0 {
		t.Fatalf("DataJSON = %s, want empty", stored.DataJSON)
	}
}

func TestStoredRunnerEventStoresMemoMarkdownAsMarkdownText(t *testing.T) {
	raw, err := (MemoDocument{
		ChannelHeader: streampkg.ChannelHeader{RunnerID: "runner-1"},
		NodeID:        "node-1",
		Path:          "memo/plan.md",
		Body:          "memo body",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	stored := newStoredRunnerEvent(RunnerEvent{
		Type:   EventNodeCompleted,
		NodeID: "node-1",
		Data:   raw,
	})
	if stored.DataEncoding != "markdown" {
		t.Fatalf("DataEncoding = %q, want markdown", stored.DataEncoding)
	}
	if stored.DataText != raw {
		t.Fatalf("DataText = %q, want raw markdown", stored.DataText)
	}
}

func TestStoredRunnerEventDoesNotTreatUnknownChannelKindAsMarkdown(t *testing.T) {
	raw := `---
kind: not-a-channel
version: 1
runner_id: runner-1
created_at: 2026-04-30T12:00:00Z
---

body`

	stored := newStoredRunnerEvent(RunnerEvent{
		Type:   EventNodeCompleted,
		NodeID: "node-1",
		Data:   raw,
	})
	if stored.DataEncoding == "markdown" {
		t.Fatalf("DataEncoding = %q, want non-markdown for unknown kind", stored.DataEncoding)
	}
}
