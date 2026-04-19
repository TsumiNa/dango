package orchestrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

func TestNewRuntime_AssignsUniqueID(t *testing.T) {
	first := NewRuntime(testLogger)
	second := NewRuntime(testLogger)
	if first.ID() == "" {
		t.Fatal("expected first runtime to have a non-empty ID")
	}
	if second.ID() == "" {
		t.Fatal("expected second runtime to have a non-empty ID")
	}
	if first.ID() == second.ID() {
		t.Fatalf("runtime IDs should be unique, got %q", first.ID())
	}
}

func TestRuntime_StaticGraphExecution(t *testing.T) {
	r := NewRuntime(testLogger)

	nodeA := &Node{
		Id: "A",
		Executor: &Executor{
			RunE: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return 10, nil, nil
			},
		},
	}

	nodeB := &Node{
		Id:      "B",
		Parents: []*Node{nodeA},
		Executor: &Executor{
			RunE: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				val := parentOutputs["A"].(int)
				return val * 2, nil, nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	// Start runtime
	go func() {
		_ = r.Start(ctx)
	}()

	// Add nodes to runtime
	err := r.AddNodes(ctx, nodeA, nodeB)
	if err != nil {
		t.Fatalf("failed to add nodes: %v", err)
	}

	// Give it a moment to process
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

func TestRuntime_DynamicNodeAppend(t *testing.T) {
	r := NewRuntime(testLogger)

	// Node C will dynamically spawn Node D
	var nodeD *Node
	nodeC := &Node{
		Id: "C",
		Executor: &Executor{
			RunE: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				nodeD = &Node{
					Id:      "D",
					Parents: nil,
					Executor: &Executor{
						RunE: func(ctx context.Context, pOuts map[string]any) (any, []*Node, error) {
							return "dynamic-result", nil, nil
						},
					},
				}
				return "static-result", []*Node{nodeD}, nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	go func() {
		_ = r.Start(ctx)
	}()

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

func TestRuntime_ErrorTermination(t *testing.T) {
	r := NewRuntime(testLogger)

	nodeErr := &Node{
		Id: "Err",
		Executor: &Executor{
			RunE: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return nil, nil, fmt.Errorf("simulated failure")
			},
		},
	}

	nodeNever := &Node{
		Id:      "Never",
		Parents: []*Node{nodeErr},
		Executor: &Executor{
			RunE: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return "should-not-run", nil, nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)

	var startErr error
	go func() {
		defer wg.Done()
		err := r.Start(ctx)
		if startErr == nil {
			startErr = err
		}
	}()

	_ = r.AddNodes(ctx, nodeErr, nodeNever)
	wg.Wait()

	if startErr == nil || startErr.Error() != "simulated failure" {
		t.Errorf("expected simulated failure, got %v", startErr)
	}
}

func TestRuntime_SubscriberAndExternalAppend(t *testing.T) {
	r := NewRuntime(testLogger)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	subCh := r.Subscribe(10)

	go func() {
		_ = r.Start(ctx)
	}()

	nodeFirst := &Node{
		Id: "First",
		Executor: &Executor{
			RunE: func(ctx context.Context, pOuts map[string]any) (any, []*Node, error) {
				return 1, nil, nil
			},
		},
	}

	_ = r.AddNodes(ctx, nodeFirst)

	// Wait for First to complete via events
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

	// While it's running, dynamically add a completely new node externally
	nodeExtDynamic := &Node{
		Id:      "ExtDynamic",
		Parents: []*Node{nodeFirst},
		Executor: &Executor{
			RunE: func(ctx context.Context, pOuts map[string]any) (any, []*Node, error) {
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
