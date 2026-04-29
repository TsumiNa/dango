package runner

import (
	"strings"
	"testing"
	"time"
)

func TestExchangeDocumentMarkdownRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	doc := ExchangeDocument{
		Stage:           ExchangeStageExecute,
		RunnerID:        "runner-1",
		NodeID:          "node-1",
		SkillName:       "summarizer",
		TaskDescription: "Summarize the input.",
		CreatedAt:       createdAt,
		Handoffs: []ExchangeHandoff{{
			To:      ExchangeRecipientDownstream,
			Intent:  ExchangeIntentContinue,
			Summary: "Use the summary as downstream context.",
		}},
		Memo:      "Progress note.",
		Reasoning: "Reasoning trace.",
		Handoff:   "Downstream output.",
	}

	raw, err := doc.Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if !strings.HasPrefix(raw, "---\nkind: dango.exchange\n") {
		t.Fatalf("markdown prefix = %q", raw[:min(len(raw), 40)])
	}

	parsed, err := ParseExchangeMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseExchangeMarkdown: %v", err)
	}
	if parsed.Kind != ExchangeDocumentKind {
		t.Fatalf("Kind = %q, want %q", parsed.Kind, ExchangeDocumentKind)
	}
	if parsed.Version != ExchangeDocumentVersion {
		t.Fatalf("Version = %d, want %d", parsed.Version, ExchangeDocumentVersion)
	}
	if parsed.Stage != doc.Stage || parsed.RunnerID != doc.RunnerID || parsed.NodeID != doc.NodeID || parsed.SkillName != doc.SkillName {
		t.Fatalf("metadata = %+v, want stage/runner/node/skill from %+v", parsed, doc)
	}
	if len(parsed.Handoffs) != 1 || parsed.Handoffs[0].To != ExchangeRecipientDownstream || parsed.Handoffs[0].Intent != ExchangeIntentContinue {
		t.Fatalf("Handoffs = %+v, want downstream continue", parsed.Handoffs)
	}
	if parsed.Memo != doc.Memo {
		t.Fatalf("Memo = %q, want %q", parsed.Memo, doc.Memo)
	}
	if parsed.Reasoning != doc.Reasoning {
		t.Fatalf("Reasoning = %q, want %q", parsed.Reasoning, doc.Reasoning)
	}
	if parsed.Handoff != doc.Handoff {
		t.Fatalf("Handoff = %q, want %q", parsed.Handoff, doc.Handoff)
	}
}

func TestNormalizeExchangeMarkdownWrapsPlainTextWithDefaults(t *testing.T) {
	createdAt := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	raw, err := NormalizeExchangeMarkdown("plain output", ExchangeDocument{
		Stage:           ExchangeStageReport,
		NodeID:          "node-2",
		SkillName:       "reporter",
		TaskDescription: "Report the output.",
		CreatedAt:       createdAt,
		Handoffs: []ExchangeHandoff{{
			To:     ExchangeRecipientOrchestrator,
			Intent: ExchangeIntentSummarize,
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeExchangeMarkdown: %v", err)
	}

	parsed, err := ParseExchangeMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseExchangeMarkdown: %v", err)
	}
	if parsed.Stage != ExchangeStageReport || parsed.NodeID != "node-2" || parsed.SkillName != "reporter" {
		t.Fatalf("metadata = %+v, want defaults", parsed)
	}
	if parsed.Handoff != "plain output" {
		t.Fatalf("Handoff = %q, want plain output", parsed.Handoff)
	}
	if len(parsed.Handoffs) != 1 || parsed.Handoffs[0].Intent != ExchangeIntentSummarize {
		t.Fatalf("Handoffs = %+v, want summarize", parsed.Handoffs)
	}
}

func TestParseExchangeMarkdownRejectsWrongKind(t *testing.T) {
	raw := "---\nkind: other\nversion: 1\nstage: execute\n---\n"
	if _, err := ParseExchangeMarkdown(raw); err == nil {
		t.Fatal("expected wrong kind to fail")
	}
}
