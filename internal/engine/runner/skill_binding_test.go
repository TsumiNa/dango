package runner

import (
	"context"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/llm"
)

type bindRecorderExecutor struct {
	calls       int
	seenSession []string
}

func (e *bindRecorderExecutor) BindForRunner(sessID *string, accessibleDirs []string, sessStores ...llm.SessionStore) (string, error) {
	e.calls++
	if len(sessStores) != 1 || sessStores[0] == nil {
		return "", context.Canceled
	}
	if sessID != nil {
		e.seenSession = append(e.seenSession, *sessID)
		return *sessID, nil
	}
	e.seenSession = append(e.seenSession, "")
	return "session-1", nil
}

func (e *bindRecorderExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return nil, nil, nil
}

func (e *bindRecorderExecutor) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *bindRecorderExecutor) Report(ctx context.Context, output any) (any, error) {
	return output, nil
}

func TestRunner_PrepareNodeExecutors_ReusesStoredSessionID(t *testing.T) {
	exec := &bindRecorderExecutor{}
	r := New(
		WithLogger(testLogger),
		WithPlan(&CoarsePlan{Request: "demo"}, map[string]*Node{
			"only": {Id: "only", Executor: exec},
		}),
	)

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	if exec.calls != 1 {
		t.Fatalf("bind calls after StartPolish = %d, want 1", exec.calls)
	}
	if got := exec.seenSession[0]; got != "" {
		t.Fatalf("first bind session = %q, want empty", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached awaiting review, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.AcceptPolishedPlan(context.Background(), &CoarsePlan{Request: "demo"}); err != nil {
		t.Fatalf("AcceptPolishedPlan: %v", err)
	}
	if exec.calls != 2 {
		t.Fatalf("bind calls after AcceptPolishedPlan = %d, want 2", exec.calls)
	}
	if got := exec.seenSession[1]; got != "session-1" {
		t.Fatalf("second bind session = %q, want session-1", got)
	}
}
