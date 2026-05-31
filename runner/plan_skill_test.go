package runner

import (
	"encoding/json"
	"strings"
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

func TestParsePlannerReviewOutputStripsMarkdownFences(t *testing.T) {
	raw := "```json\n{\"approved\":true}\n```"
	review, err := parsePlannerReviewOutput(raw)
	if err != nil {
		t.Fatalf("parsePlannerReviewOutput: %v", err)
	}
	if !review.Approved {
		t.Fatalf("approved = false, want true")
	}
}

func TestParsePlannerReplanOutputStripsLeadingProse(t *testing.T) {
	raw := "Sure, here is the revised plan:\n{\"plan\":{\"request\":\"r\",\"nodes\":[]}}\nLet me know if you need changes."
	plan, err := parsePlannerReplanOutput(raw)
	if err != nil {
		t.Fatalf("parsePlannerReplanOutput: %v", err)
	}
	if plan.Request != "r" {
		t.Fatalf("plan.Request = %q, want r", plan.Request)
	}
}

func TestParsePlannerReplanOutputErrorIncludesRawSnippet(t *testing.T) {
	_, err := parsePlannerReplanOutput("")
	if err == nil {
		t.Fatal("expected error for empty raw")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Fatalf("error missing diagnostic: %v", err)
	}
}

func TestPlannerRetryPromptIncludesError(t *testing.T) {
	prompt := PlannerRetryPrompt(errParseFailed)
	if !strings.Contains(prompt, "could not be parsed") || !strings.Contains(prompt, errParseFailed.Error()) {
		t.Fatalf("retry prompt missing parse-error context: %s", prompt)
	}
}

var errParseFailed = simpleErr("synthetic parse failure")

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

func TestExtractJSONObjectHandlesEmbeddedFence(t *testing.T) {
	raw := "Here you go:\n```json\n{\"a\":1,\"b\":\"}{escaped\"}\n```\nthanks"
	got := ExtractJSONObject(raw)
	if got != `{"a":1,"b":"}{escaped"}` {
		t.Fatalf("ExtractJSONObject = %q", got)
	}
}
