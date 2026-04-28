package runner

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNewRunner_AssignsUniqueID(t *testing.T) {
	first := New(WithLogger(testLogger))
	second := New(WithLogger(testLogger))
	if first.ID() == "" {
		t.Fatal("expected first runner to have a non-empty ID")
	}
	if second.ID() == "" {
		t.Fatal("expected second runner to have a non-empty ID")
	}
	if first.ID() == second.ID() {
		t.Fatalf("runner IDs should be unique, got %q", first.ID())
	}
}

func TestRunner_StaticGraphExecution(t *testing.T) {
	r := New(WithLogger(testLogger))

	nodeA := &Node{
		Id: "A",
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return 10, nil, nil
			},
		},
	}

	nodeB := &Node{
		Id:      "B",
		Parents: []*Node{nodeA},
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				val := parentOutputs["A"].(int)
				return val * 2, nil, nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := r.AddNodes(ctx, nodeA, nodeB); err != nil {
		t.Fatalf("failed to add nodes: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	snap, err := r.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to get snapshot: %v", err)
	}

	if snap.CompletedNodes["A"] != 10 {
		t.Errorf("expected node A output 10, got %v", snap.CompletedNodes["A"])
	}
	if snap.CompletedNodes["B"] != 20 {
		t.Errorf("expected node B output 20, got %v", snap.CompletedNodes["B"])
	}
}

func TestRunner_DynamicNodeAppend(t *testing.T) {
	r := New(WithLogger(testLogger))

	var nodeD *Node
	nodeC := &Node{
		Id: "C",
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				nodeD = &Node{
					Id: "D",
					Executor: &testExecutor{
						run: func(ctx context.Context, pOuts map[string]any) (any, []*Node, error) {
							return "dynamic-result", nil, nil
						},
					},
				}
				return "static-result", []*Node{nodeD}, nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_ = r.AddNodes(ctx, nodeC)

	time.Sleep(100 * time.Millisecond)

	snap, err := r.GetSnapshot(ctx)
	if err != nil {
		t.Fatalf("failed to get snapshot: %v", err)
	}

	if snap.CompletedNodes["C"] != "static-result" {
		t.Errorf("expected node C output static-result, got %v", snap.CompletedNodes["C"])
	}
	if snap.CompletedNodes["D"] != "dynamic-result" {
		t.Errorf("expected node D dynamically executed output, got %v", snap.CompletedNodes["D"])
	}
}

func TestRunner_ErrorTermination(t *testing.T) {
	r := New(WithLogger(testLogger))

	nodeErr := &Node{
		Id: "Err",
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return nil, nil, fmt.Errorf("simulated failure")
			},
		},
	}

	nodeNever := &Node{
		Id:      "Never",
		Parents: []*Node{nodeErr},
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return "should-not-run", nil, nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_ = r.AddNodes(ctx, nodeErr, nodeNever)

	startErr := r.Wait(ctx)
	if startErr == nil || startErr.Error() != "simulated failure" {
		t.Errorf("expected simulated failure, got %v", startErr)
	}
}

func TestRunner_SubscriberAndExternalAppend(t *testing.T) {
	r := New(WithLogger(testLogger))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subCh := r.Subscribe(10)

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	nodeFirst := &Node{
		Id: "First",
		Executor: &testExecutor{
			run: func(ctx context.Context, pOuts map[string]any) (any, []*Node, error) {
				return 1, nil, nil
			},
		},
	}

	_ = r.AddNodes(ctx, nodeFirst)

	completedFirst := false
	for e := range subCh {
		if e.Type == EventNodeCompleted && e.NodeID == "First" {
			completedFirst = true
			break
		}
	}
	if !completedFirst {
		t.Fatal("never received completed event for First")
	}

	nodeExtDynamic := &Node{
		Id:      "ExtDynamic",
		Parents: []*Node{nodeFirst},
		Executor: &testExecutor{
			run: func(ctx context.Context, pOuts map[string]any) (any, []*Node, error) {
				val, _ := pOuts["First"].(int)
				return val * 10, nil, nil
			},
		},
	}

	_ = r.AddNodes(ctx, nodeExtDynamic)

	completedExt := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for ExtDynamic to complete")
		case e := <-subCh:
			if e.Type == EventNodeCompleted && e.NodeID == "ExtDynamic" {
				if e.Data.(int) != 10 {
					t.Fatalf("expected output 10, got %v", e.Data)
				}
				completedExt = true
			}
		}
		if completedExt {
			break
		}
	}
}

func TestRunner_WithPlanSeedsInitialNodes(t *testing.T) {
	nodeA := &Node{
		Id: "A",
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return "from-A", nil, nil
			},
		},
	}
	plan := &CoarsePlan{
		Request: "seed test",
		Nodes:   []CoarsePlanNode{{ID: "A", SkillName: "noop", TaskDescription: "only"}},
	}
	r := New(
		WithLogger(testLogger),
		WithPlan(plan, map[string]*Node{"A": nodeA}),
	)

	if got := r.Plan(); got == nil || got.RunnerID != r.ID() {
		t.Fatalf("Plan().RunnerID = %v, want %q", got, r.ID())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sub := r.Subscribe(16)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForRunnerEvent(t, sub, EventNodeCompleted, "A")

	view := r.View()
	if view.Plan == nil || view.Plan.Request != "seed test" {
		t.Fatalf("view.Plan = %+v, want request=seed test", view.Plan)
	}
	if view.Plan.RunnerID != r.ID() {
		t.Fatalf("view.Plan.RunnerID = %q, want %q", view.Plan.RunnerID, r.ID())
	}
	if view.Phase != PhaseExecuting && view.Phase != PhaseSettled {
		t.Fatalf("view.Phase = %q, want executing or settled", view.Phase)
	}
}

func TestRunner_DoneClosesOnAbort(t *testing.T) {
	r := New(WithLogger(testLogger))
	r.Abort(context.Canceled)

	select {
	case <-r.Done():
	case <-time.After(time.Second):
		t.Fatal("Done channel not closed after Abort")
	}
	if got := r.State().Status; got != RunnerStatusCanceled {
		t.Fatalf("state = %q, want canceled", got)
	}
	if got := r.Phase(); got != PhaseSettled {
		t.Fatalf("phase = %q, want settled", got)
	}
	if err := r.Start(context.Background()); err != ErrRunnerAlreadyStarted {
		t.Fatalf("Start after Abort err = %v, want ErrRunnerAlreadyStarted", err)
	}
}
