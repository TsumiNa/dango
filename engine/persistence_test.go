package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/engine/runner"
	runtimepkg "github.com/tsumina/dango/store/runtime"
	streampkg "github.com/tsumina/dango/stream"
)

func TestRuntimePersistenceSQLiteSupportsReplayRunnerRecordsAndDescribeAfterReopen(t *testing.T) {
	clearLLMEnv(t)
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dango.db")
	persistence, err := runtimepkg.Open(runtimepkg.Config{SQLitePath: dbPath})
	if err != nil {
		t.Fatalf("runtime.Open: %v", err)
	}
	configureOrchestratorPersistence(t, persistence)

	o := newOrchestrator(testLogger,
		WithPersistence(persistence.Backend()),
	)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t,
		mustPlanJSON(t, &runnerpkg.CoarsePlan{
			Request: "run a single node",
			Nodes: []runnerpkg.CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}),
		mustReviewJSON(t, true, ""),
	)); err != nil {
		t.Fatalf("SetOrchestratorSkill: %v", err)
	}

	resp, err := o.StartRequest(ctx, Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	runnerID := mustReadRunnerCreated(t, resp.Stream)
	managedRunner, err := o.Runner(runnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("runner Wait: %v", err)
	}
	waitForPersistedSettledPhase(t, ctx, persistence, resp.RequestID)

	records, err := o.LoadRunnerRecords(ctx, runnerID)
	if err != nil {
		t.Fatalf("LoadRunnerRecords: %v", err)
	}
	if len(records) == 0 || !hasStoredEvent(records, runnerpkg.EventNodeCompleted.String(), "only") {
		t.Fatalf("records = %+v, want persisted node completion", records)
	}
	view, err := o.DescribeRequest(ctx, resp.RequestID)
	if err != nil {
		t.Fatalf("DescribeRequest: %v", err)
	}
	if view.RunnerID != runnerID {
		t.Fatalf("DescribeRequest runnerID = %q, want %q", view.RunnerID, runnerID)
	}
	if view.Phase != runnerpkg.PhaseSettled {
		t.Fatalf("DescribeRequest phase = %q, want %q", view.Phase, runnerpkg.PhaseSettled)
	}
	resp.Stream.Close()
	if err := persistence.Close(context.Background()); err != nil {
		t.Fatalf("Close(first persistence): %v", err)
	}

	reopened, err := runtimepkg.Open(runtimepkg.Config{SQLitePath: dbPath})
	if err != nil {
		t.Fatalf("runtime.Open(reopen): %v", err)
	}
	defer func() {
		if err := reopened.Close(context.Background()); err != nil {
			t.Fatalf("Close(reopened persistence): %v", err)
		}
	}()
	fresh := newOrchestrator(testLogger,
		WithPersistence(reopened.Backend()),
	)

	rawFrames, err := reopened.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: resp.RequestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents(reopen): %v", err)
	}
	if len(rawFrames) == 0 {
		t.Fatal("LoadEvents(reopen) returned no request frames")
	}
	reopenedRecords, err := fresh.LoadRunnerRecords(ctx, runnerID)
	if err != nil {
		t.Fatalf("LoadRunnerRecords(reopen): %v", err)
	}
	if len(reopenedRecords) == 0 || !hasStoredEvent(reopenedRecords, runnerpkg.EventNodeCompleted.String(), "only") {
		t.Fatalf("reopened records = %+v, want persisted node completion", reopenedRecords)
	}
	reopenedView, err := fresh.DescribeRequest(ctx, resp.RequestID)
	if err != nil {
		t.Fatalf("DescribeRequest(reopen): %v", err)
	}
	if reopenedView.RunnerID != runnerID {
		t.Fatalf("DescribeRequest(reopen) runnerID = %q, want %q", reopenedView.RunnerID, runnerID)
	}
	cursor, err := reopened.SnapshotCursorStore().LoadCursor(ctx, resp.RequestID)
	if err != nil {
		t.Fatalf("LoadCursor(reopen): %v", err)
	}
	if cursor.EventSequence != reopenedView.SnapshotCursor().EventSequence {
		t.Fatalf("cursor event sequence = %d, want %d", cursor.EventSequence, reopenedView.SnapshotCursor().EventSequence)
	}
}

func configureOrchestratorPersistence(t *testing.T, persistence *runtimepkg.Persistence) {
	t.Helper()
	if persistence == nil || persistence.EventLogStore() == nil || persistence.RunnerStore() == nil || persistence.SnapshotCursorStore() == nil {
		t.Fatal("runtime persistence is not fully configured")
	}
}

func waitForPersistedSettledPhase(t *testing.T, ctx context.Context, persistence *runtimepkg.Persistence, requestID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := persistence.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: requestID}, 1, streampkg.Filter{})
		if err != nil {
			t.Fatalf("LoadEvents(wait): %v", err)
		}
		for _, event := range events {
			expanded, err := streampkg.ExpandBundleEvent(event)
			if err != nil {
				t.Fatalf("ExpandBundleEvent(wait): %v", err)
			}
			for _, candidate := range expanded {
				if candidate.EventType != streampkg.EventRunnerPhaseChanged {
					continue
				}
				var delta struct {
					Phase string `json:"phase"`
				}
				if err := json.Unmarshal(candidate.Delta, &delta); err != nil {
					continue
				}
				if delta.Phase == string(runnerpkg.PhaseSettled) {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for persisted settled phase")
}
