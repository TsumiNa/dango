package runner

import (
	"testing"
	"time"
)

func TestAnnotateExchangeOutputAddsRunnerAndNodeMetadata(t *testing.T) {
	raw, err := (ExchangeDocument{
		Stage:     ExchangeStageExecute,
		CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Handoff:   "output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := New(WithLogger(testLogger))
	got := r.annotateExchangeOutput(&Node{
		Id:              "node-1",
		SkillName:       "skill-1",
		TaskDescription: "Do the thing.",
	}, raw)

	text, ok := got.(string)
	if !ok {
		t.Fatalf("annotated output type = %T, want string", got)
	}
	parsed, err := ParseExchangeMarkdown(text)
	if err != nil {
		t.Fatalf("ParseExchangeMarkdown: %v", err)
	}
	if parsed.RunnerID != r.ID() || parsed.NodeID != "node-1" || parsed.SkillName != "skill-1" || parsed.TaskDescription != "Do the thing." {
		t.Fatalf("metadata = %+v, want runner/node metadata", parsed)
	}
}

func TestAnnotateExchangeOutputLeavesPlainValuesUntouched(t *testing.T) {
	r := New(WithLogger(testLogger))
	if got := r.annotateExchangeOutput(&Node{Id: "node-1"}, 10); got != 10 {
		t.Fatalf("annotate int = %v, want 10", got)
	}
	if got := r.annotateExchangeOutput(&Node{Id: "node-1"}, "plain"); got != "plain" {
		t.Fatalf("annotate plain string = %v, want plain", got)
	}
}
