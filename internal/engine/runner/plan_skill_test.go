package runner

import (
	"encoding/json"
	"testing"
)

func TestMarshalPlannerReviewInputUsesMarkdownDocuments(t *testing.T) {
	prompt, err := marshalPlannerReviewInput(&CoarsePlan{Request: "demo"}, map[string]any{
		"A": "markdown-A",
	})
	if err != nil {
		t.Fatalf("marshalPlannerReviewInput: %v", err)
	}

	var payload struct {
		Data struct {
			PolishDocuments map[string]string `json:"polish_documents"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(prompt), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload.Data.PolishDocuments["A"] != "markdown-A" {
		t.Fatalf("polish document A = %q, want markdown-A", payload.Data.PolishDocuments["A"])
	}
}
