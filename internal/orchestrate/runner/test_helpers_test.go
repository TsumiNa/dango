package runner

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

type testExecutor struct {
	run    func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error)
	polish func(ctx context.Context) (any, error)
	report func(ctx context.Context, output any) (any, error)
}

func (e *testExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	if e.run == nil {
		return nil, nil, nil
	}
	return e.run(ctx, parentOutputs)
}

func (e *testExecutor) Polish(ctx context.Context) (any, error) {
	if e.polish == nil {
		return nil, nil
	}
	return e.polish(ctx)
}

func (e *testExecutor) Report(ctx context.Context, output any) (any, error) {
	if e.report == nil {
		return nil, nil
	}
	return e.report(ctx, output)
}

func mustNewRunnerStore(t *testing.T, dir string) *JSONRunnerStore {
	t.Helper()
	store, err := NewJSONRunnerStore(dir)
	if err != nil {
		t.Fatalf("NewJSONRunnerStore: %v", err)
	}
	return store
}

func waitForRunnerEvent(t *testing.T, ch <-chan RunnerEvent, want EventType, nodeID string) RunnerEvent {
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

func hasStoredEvent(records []RunnerRecord, eventType string, nodeID string) bool {
	for _, rec := range records {
		if rec.Kind != RunnerRecordEvent || rec.Event == nil {
			continue
		}
		if rec.Event.Type == eventType && rec.Event.NodeID == nodeID {
			return true
		}
	}
	return false
}

func lastStatus(records []RunnerRecord) RunnerRecord {
	var last RunnerRecord
	for _, rec := range records {
		if rec.Kind == RunnerRecordStatus {
			last = rec
		}
	}
	return last
}

func assertCanceledStart(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}
}

func assertFailureContains(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("Start err = %v, want %q", err, text)
	}
}
