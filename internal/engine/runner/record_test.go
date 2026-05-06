package runner

import "testing"

func TestStoredRunnerEventStoresExchangeMarkdownAsMarkdownText(t *testing.T) {
	raw, err := (ExchangeDocument{
		Stage:   ExchangeStageExecute,
		Handoff: "output",
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
