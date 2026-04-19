package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func waitForRuntimeEvent(t *testing.T, ch <-chan RuntimeEvent, want EventType, nodeID string) RuntimeEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for event %s/%s", want.String(), nodeID)
		case ev := <-ch:
			if ev.Type == want && ev.NodeID == nodeID {
				return ev
			}
		}
	}
}

func hasStoredEvent(records []RuntimeRecord, eventType string, nodeID string) bool {
	for _, rec := range records {
		if rec.Kind != RuntimeRecordEvent || rec.Event == nil {
			continue
		}
		if rec.Event.Type == eventType && rec.Event.NodeID == nodeID {
			return true
		}
	}
	return false
}

func lastStatus(records []RuntimeRecord) RuntimeRecord {
	var last RuntimeRecord
	for _, rec := range records {
		if rec.Kind == RuntimeRecordStatus {
			last = rec
		}
	}
	return last
}

func TestRuntimeSetStoreRejectsAfterStart(t *testing.T) {
	r := NewRuntime(testLogger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.Start(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.State().Status == RuntimeStatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.SetStore(mustNewRuntimeStore(t, t.TempDir())); !errors.Is(err, ErrRuntimeAlreadyStarted) {
		t.Fatalf("SetStore err = %v, want ErrRuntimeAlreadyStarted", err)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}
}

func TestRuntimePersistsEventsAndCancellation(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	r := NewRuntime(testLogger)
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
		Executor: &Executor{
			RunE: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return 10, nil, nil
			},
		},
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	waitForRuntimeEvent(t, sub, EventNodeCompleted, "persisted")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}

	records, err := store.Load(context.Background(), r.ID())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) == 0 || records[0].Kind != RuntimeRecordInit {
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
	if finalStatus.Status != RuntimeStatusCanceled {
		t.Fatalf("final status = %q, want canceled", finalStatus.Status)
	}
	if got := r.State().Status; got != RuntimeStatusCanceled {
		t.Fatalf("runtime state = %q, want canceled", got)
	}
	if r.State().FinishedAt.IsZero() {
		t.Fatal("runtime FinishedAt was not recorded")
	}
}

func TestRuntimePersistsFailure(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	r := NewRuntime(testLogger)
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
		Executor: &Executor{
			RunE: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return nil, nil, errors.New("simulated failure")
			},
		},
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "simulated failure") {
		t.Fatalf("Start err = %v, want simulated failure", err)
	}

	records, loadErr := store.Load(context.Background(), r.ID())
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if !hasStoredEvent(records, EventNodeFailed.String(), "boom") {
		t.Fatal("missing persisted node-failed event")
	}
	finalStatus := lastStatus(records)
	if finalStatus.Status != RuntimeStatusFailed {
		t.Fatalf("final status = %q, want failed", finalStatus.Status)
	}
	if finalStatus.Error == "" || !strings.Contains(finalStatus.Error, "simulated failure") {
		t.Fatalf("final status error = %q, want simulated failure", finalStatus.Error)
	}
	if got := r.State().Status; got != RuntimeStatusFailed {
		t.Fatalf("runtime state = %q, want failed", got)
	}
}
