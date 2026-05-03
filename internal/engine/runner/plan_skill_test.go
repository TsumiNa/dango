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

func TestParsePlannerReviewOutputAcceptsRejectObject(t *testing.T) {
	review, err := parsePlannerReviewOutput(`{"reject":{"summary":"needs changes","analysis":"upstream field mapping is unclear"}}`)
	if err != nil {
		t.Fatalf("parsePlannerReviewOutput: %v", err)
	}
	if review.Approved {
		t.Fatal("review.Approved = true, want false")
	}
	if got, want := review.Reason, "needs changes: upstream field mapping is unclear"; got != want {
		t.Fatalf("review.Reason = %q, want %q", got, want)
	}
}

func TestParsePlannerReviewOutputAllowsEmptyLegacyReason(t *testing.T) {
	review, err := parsePlannerReviewOutput(`{"approved":false}`)
	if err != nil {
		t.Fatalf("parsePlannerReviewOutput: %v", err)
	}
	if review.Approved {
		t.Fatal("review.Approved = true, want false")
	}
	if review.Reason != "" {
		t.Fatalf("review.Reason = %q, want empty", review.Reason)
	}
}
