package runner

import (
	"strings"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/stream"
)

func TestExchangeDocMarkdownRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	raw, err := (ExchangeDoc{
		ChannelHeader: streampkg.ChannelHeader{
			RunnerID:  "runner-1",
			CreatedAt: createdAt,
		},
		NodeID:    "node-1",
		SkillName: "analyst",
		Title:     "normalization notes",
		Body:      "shared context body",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	parsed, err := ParseExchangeDocMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseExchangeDocMarkdown: %v", err)
	}
	if parsed.Kind != streampkg.ChannelKindExchange || parsed.Version != ExchangeDocVersion {
		t.Fatalf("kind/version = %q/%d", parsed.Kind, parsed.Version)
	}
	if parsed.RunnerID != "runner-1" || parsed.NodeID != "node-1" || parsed.Title != "normalization notes" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !parsed.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", parsed.CreatedAt, createdAt)
	}
	if parsed.Body != "shared context body" {
		t.Fatalf("body = %q, want %q", parsed.Body, "shared context body")
	}
}

func TestExchangeDocMarkdownRejectsMissingFields(t *testing.T) {
	_, err := (ExchangeDoc{NodeID: "node-1"}).Markdown()
	if err == nil || !strings.Contains(err.Error(), "runner_id must not be empty") {
		t.Fatalf("Markdown error = %v, want missing runner_id", err)
	}
}

func TestParseExchangeDocMarkdownRejectsLegacyKind(t *testing.T) {
	raw := `---
kind: dango.exchange_doc
version: 1
runner_id: runner-1
node_id: node-1
created_at: 2026-05-01T12:00:00Z
---

legacy body`
	_, err := ParseExchangeDocMarkdown(raw)
	if err == nil || !strings.Contains(err.Error(), `want "exchange"`) {
		t.Fatalf("ParseExchangeDocMarkdown error = %v, want legacy kind rejection", err)
	}
}

func TestParseExchangeDocMarkdownRejectsUnsupportedKind(t *testing.T) {
	raw := `---
kind: dango.exchange
version: 1
runner_id: runner-1
node_id: node-1
created_at: 2026-05-01T12:00:00Z
---

legacy body`
	_, err := ParseExchangeDocMarkdown(raw)
	if err == nil || !strings.Contains(err.Error(), `want "exchange"`) {
		t.Fatalf("ParseExchangeDocMarkdown error = %v, want kind mismatch", err)
	}
}
