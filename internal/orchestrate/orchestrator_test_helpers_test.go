package orchestrate

import (
	"log/slog"
	"os"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

type Node = runnerpkg.Node
type EventType = runnerpkg.EventType
type RunnerEvent = runnerpkg.RunnerEvent
type RunnerRecord = runnerpkg.RunnerRecord

const (
	EventNodeAdded       = runnerpkg.EventNodeAdded
	EventNodeStarted     = runnerpkg.EventNodeStarted
	EventNodeCompleted   = runnerpkg.EventNodeCompleted
	EventNodeFailed      = runnerpkg.EventNodeFailed
	EventEngineIdle      = runnerpkg.EventEngineIdle
	EventEngineStopped   = runnerpkg.EventEngineStopped
	RunnerStatusPending  = runnerpkg.RunnerStatusPending
	RunnerStatusRunning  = runnerpkg.RunnerStatusRunning
	RunnerStatusIdle     = runnerpkg.RunnerStatusIdle
	RunnerStatusFailed   = runnerpkg.RunnerStatusFailed
	RunnerStatusCanceled = runnerpkg.RunnerStatusCanceled
	RunnerRecordInit     = runnerpkg.RunnerRecordInit
)

var ErrRunnerLogNotFound = runnerpkg.ErrRunnerLogNotFound

func mustNewRunnerStore(t *testing.T, dir string) *runnerpkg.JSONRunnerStore {
	t.Helper()
	store, err := runnerpkg.NewJSONRunnerStore(dir)
	if err != nil {
		t.Fatalf("NewJSONRunnerStore: %v", err)
	}
	return store
}

func waitForRunnerEvent(t *testing.T, ch <-chan runnerpkg.RunnerEvent, want runnerpkg.EventType, nodeID string) runnerpkg.RunnerEvent {
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

func hasStoredEvent(records []runnerpkg.RunnerRecord, eventType string, nodeID string) bool {
	for _, rec := range records {
		if rec.Kind != runnerpkg.RunnerRecordEvent || rec.Event == nil {
			continue
		}
		if rec.Event.Type == eventType && rec.Event.NodeID == nodeID {
			return true
		}
	}
	return false
}

func mustNodeExecutor(t *testing.T, node *runnerpkg.Node) *Executor {
	t.Helper()
	executor, ok := node.Executor.(*Executor)
	if !ok || executor == nil {
		t.Fatalf("node %q executor = %T, want *Executor", node.Id, node.Executor)
	}
	return executor
}
