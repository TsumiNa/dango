package runner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func newPolishNode(id string, polishFrag any) *Node {
	return &Node{
		Id: id,
		Executor: &testExecutor{
			polish: func(ctx context.Context) (any, error) {
				return polishFrag, nil
			},
		},
	}
}

func TestRunner_StartPolishRequiresPlan(t *testing.T) {
	r := New(WithLogger(testLogger))
	if err := r.StartPolish(context.Background()); !errors.Is(err, ErrPlanRequired) {
		t.Fatalf("StartPolish without plan err = %v, want ErrPlanRequired", err)
	}
}

func TestRunner_StartPolishCollectsFragmentsAndAwaitsReview(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	nodes := map[string]*Node{
		"A": newPolishNode("A", "frag-A"),
		"B": newPolishNode("B", "frag-B"),
	}
	r := New(WithLogger(testLogger), WithPlan(plan, nodes))

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	frags := r.PolishFragments()
	if frags["A"] != "frag-A" {
		t.Errorf("fragment A = %v, want frag-A", frags["A"])
	}
	if frags["B"] != "frag-B" {
		t.Errorf("fragment B = %v, want frag-B", frags["B"])
	}
}

func TestRunner_StartPolishFailureAborts(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	polishErr := errors.New("boom")
	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Executor: &testExecutor{
				polish: func(ctx context.Context) (any, error) {
					return nil, polishErr
				},
			},
		},
	}
	r := New(WithLogger(testLogger), WithPlan(plan, nodes))

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := r.Wait(waitCtx)
	if !errors.Is(err, polishErr) {
		t.Fatalf("Wait err = %v, want contains polishErr", err)
	}
	if r.Phase() != PhaseSettled {
		t.Fatalf("phase = %q, want PhaseSettled", r.Phase())
	}
}

func TestRunner_AcceptPolishedPlanRunsEngineAndReports(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	var reportCalls atomic.Int32
	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Executor: &testExecutor{
				run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
					return "exec-A", nil, nil
				},
				polish: func(ctx context.Context) (any, error) { return "frag-A", nil },
				report: func(ctx context.Context, output any) (any, error) {
					reportCalls.Add(1)
					return "sum-" + output.(string), nil
				},
			},
		},
	}
	r := New(WithLogger(testLogger), WithPlan(plan, nodes))

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}

	// Wait for AwaitingReview.
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	events := r.Subscribe(16)
	acceptedPlan := &CoarsePlan{Request: "reviewed-plan"}
	if err := r.AcceptPolishedPlan(context.Background(), acceptedPlan); err != nil {
		t.Fatalf("AcceptPolishedPlan: %v", err)
	}

	// Wait until engine reports idle, then Complete to run Report.
	waitForRunnerEvent(t, events, EventEngineIdle, "")

	if err := r.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := reportCalls.Load(); got != 1 {
		t.Fatalf("report calls = %d, want 1", got)
	}
	if got := r.Plan().Request; got != acceptedPlan.Request {
		t.Fatalf("accepted plan request = %q, want %q", got, acceptedPlan.Request)
	}
	if got := r.Plan().RunnerID; got != r.ID() {
		t.Fatalf("accepted plan runner id = %q, want %q", got, r.ID())
	}
	if sum := r.ReportSummaries()["A"]; sum != "sum-exec-A" {
		t.Fatalf("summary A = %v, want sum-exec-A", sum)
	}
	if r.Phase() != PhaseSettled {
		t.Fatalf("phase = %q, want PhaseSettled", r.Phase())
	}
}

func TestRunner_RejectPolishedPlanTransitionsToAwaitingReplan(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	nodes := map[string]*Node{
		"A": newPolishNode("A", "frag-A"),
	}
	r := New(WithLogger(testLogger), WithPlan(plan, nodes))

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.RejectPolishedPlan("needs-revision"); err != nil {
		t.Fatalf("RejectPolishedPlan: %v", err)
	}
	if r.Phase() != PhaseAwaitingReplan {
		t.Fatalf("phase = %q, want PhaseAwaitingReplan", r.Phase())
	}
	if got := r.ReplanReason(); got != "needs-revision" {
		t.Fatalf("ReplanReason = %q, want needs-revision", got)
	}
}

func TestRunner_ReplanWithRestartsPolish(t *testing.T) {
	plan1 := &CoarsePlan{Request: "v1"}
	plan2 := &CoarsePlan{Request: "v2"}
	nodes1 := map[string]*Node{"A": newPolishNode("A", "frag-A1")}
	nodes2 := map[string]*Node{"B": newPolishNode("B", "frag-B2")}

	r := New(WithLogger(testLogger), WithPlan(plan1, nodes1))
	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview (1), got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.RejectPolishedPlan("try-again"); err != nil {
		t.Fatalf("RejectPolishedPlan: %v", err)
	}
	if err := r.ReplanWith(context.Background(), plan2, nodes2); err != nil {
		t.Fatalf("ReplanWith: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview (2), got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}
	frags := r.PolishFragments()
	if _, ok := frags["A"]; ok {
		t.Fatalf("old fragment A should be cleared, got %v", frags)
	}
	if frags["B"] != "frag-B2" {
		t.Fatalf("fragment B = %v, want frag-B2", frags["B"])
	}
	if r.Plan() != plan2 {
		t.Fatalf("plan not swapped")
	}
}

func TestRunner_CompleteRejectedFromWrongPhase(t *testing.T) {
	r := New(WithLogger(testLogger))
	if err := r.Complete(context.Background()); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("Complete from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
}

func TestRunner_AcceptRejectFromWrongPhase(t *testing.T) {
	r := New(WithLogger(testLogger))
	if err := r.AcceptPolishedPlan(context.Background(), &CoarsePlan{}); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("Accept from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
	if err := r.RejectPolishedPlan("x"); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("Reject from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
	if err := r.ReplanWith(context.Background(), &CoarsePlan{}, nil); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("ReplanWith from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
}
