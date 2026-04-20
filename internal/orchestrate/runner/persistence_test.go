package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunnerSetStoreRejectsAfterStart(t *testing.T) {
	r := NewRunner(testLogger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.Start(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.State().Status == RunnerStatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.SetStore(mustNewRunnerStore(t, t.TempDir())); !errors.Is(err, ErrRunnerAlreadyStarted) {
		t.Fatalf("SetStore err = %v, want ErrRunnerAlreadyStarted", err)
	}

	cancel()
	assertCanceledStart(t, <-done)
}

func TestRunnerPersistsEventsAndCancellation(t *testing.T) {
	store := mustNewRunnerStore(t, t.TempDir())
	r := NewRunner(testLogger)
	if err := r.SetStore(store); err != nil {
		t.Fatalf("SetStore: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	sub := r.Subscribe(16)
	go func() {
		done <- r.Start(ctx)
	}()

	node := &Node{
		Id: "persisted",
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return 10, nil, nil
			},
		},
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	waitForRunnerEvent(t, sub, EventNodeCompleted, "persisted")
	cancel()
	assertCanceledStart(t, <-done)

	records, err := store.Load(context.Background(), r.ID())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) == 0 || records[0].Kind != RunnerRecordInit {
		t.Fatalf("records[0] = %+v, want init", records)
	}
	if !hasStoredEvent(records, EventNodeAdded.String(), "persisted") {
		t.Fatal("missing persisted node-added event")
	}
	if !hasStoredEvent(records, EventNodeStarted.String(), "persisted") {
		t.Fatal("missing persisted node-started event")
	}
	if !hasStoredEvent(records, EventNodeCompleted.String(), "persisted") {
		t.Fatal("missing persisted node-completed event")
	}
	if !hasStoredEvent(records, EventEngineStopped.String(), "") {
		t.Fatal("missing persisted engine-stopped event")
	}
	finalStatus := lastStatus(records)
	if finalStatus.Status != RunnerStatusCanceled {
		t.Fatalf("final status = %q, want canceled", finalStatus.Status)
	}
	if got := r.State().Status; got != RunnerStatusCanceled {
		t.Fatalf("runner state = %q, want canceled", got)
	}
	if r.State().FinishedAt.IsZero() {
		t.Fatal("runner FinishedAt was not recorded")
	}
}

func TestRunnerPersistsFailure(t *testing.T) {
	store := mustNewRunnerStore(t, t.TempDir())
	r := NewRunner(testLogger)
	if err := r.SetStore(store); err != nil {
		t.Fatalf("SetStore: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.Start(ctx)
	}()

	node := &Node{
		Id: "boom",
		Executor: &testExecutor{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return nil, nil, errors.New("simulated failure")
			},
		},
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	startErr := <-done
	assertFailureContains(t, startErr, "simulated failure")

	records, loadErr := store.Load(context.Background(), r.ID())
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if !hasStoredEvent(records, EventNodeFailed.String(), "boom") {
		t.Fatal("missing persisted node-failed event")
	}
	finalStatus := lastStatus(records)
	if finalStatus.Status != RunnerStatusFailed {
		t.Fatalf("final status = %q, want failed", finalStatus.Status)
	}
	if finalStatus.Error == "" || !strings.Contains(finalStatus.Error, "simulated failure") {
		t.Fatalf("final status error = %q, want simulated failure", finalStatus.Error)
	}
	if got := r.State().Status; got != RunnerStatusFailed {
		t.Fatalf("runner state = %q, want failed", got)
	}
}
