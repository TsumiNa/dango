package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/engine/runner"
	storepkg "github.com/tsumina/dango/store"
	streampkg "github.com/tsumina/dango/stream"
)

func TestReplayDescribeViewBuildsGraphAndArtifacts(t *testing.T) {
	t.Parallel()

	const requestID = "req_describe"
	const runnerID = "run_describe"
	eventLog := &stubEventLogStore{events: map[string][]streampkg.Event{
		requestID: {
			rawEvent(requestID, 1, streampkg.EventStatusProgress, streampkg.Source{Layer: "orchestrator", ID: "orchestrator"}, streampkg.StatusRunning, map[string]any{
				"message":   "runner created",
				"runner_id": runnerID,
			}),
			rawBundleEvent(requestID, 2,
				logicalEvent(streampkg.EventRunnerPhaseChanged, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusRunning, streampkg.Scope{RunnerID: runnerID}, map[string]any{
					"phase":  string(runnerpkg.PhaseExecuting),
					"status": string(runnerpkg.RunnerStatusRunning),
				}),
				logicalEvent(streampkg.EventRunnerNodeAdded, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusPending, streampkg.Scope{RunnerID: runnerID, NodeID: "plan"}, map[string]any{
					"event":            runnerpkg.EventNodeAdded.String(),
					"node_id":          "plan",
					"skill_name":       "planner",
					"task_description": "Draft a plan",
				}),
				logicalEvent(streampkg.EventRunnerNodeAdded, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusPending, streampkg.Scope{RunnerID: runnerID, NodeID: "report"}, map[string]any{
					"event":            runnerpkg.EventNodeAdded.String(),
					"node_id":          "report",
					"skill_name":       "reporter",
					"task_description": "Write the report",
					"depends_on":       []string{"plan"},
				}),
				logicalEvent(streampkg.EventArtifactCreated, streampkg.Source{Layer: "agent", ID: "report", ParentID: runnerID}, streampkg.StatusCompleted, streampkg.Scope{RunnerID: runnerID, NodeID: "report"}, map[string]any{
					"path":          "/tmp/report.md",
					"resource_type": "file",
					"description":   "final report",
					"stage":         "execute",
				}),
				logicalEvent(streampkg.EventRunnerPhaseChanged, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusCompleted, streampkg.Scope{RunnerID: runnerID}, map[string]any{
					"phase":  string(runnerpkg.PhaseSettled),
					"status": string(runnerpkg.RunnerStatusIdle),
				}),
			),
		},
	}}

	view, err := ReplayDescribeView(context.Background(), requestID, nil, storepkg.SnapshotCursor{}, eventLog)
	if err != nil {
		t.Fatalf("ReplayDescribeView: %v", err)
	}
	if view.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", view.RequestID, requestID)
	}
	if view.RunnerID != runnerID {
		t.Fatalf("RunnerID = %q, want %q", view.RunnerID, runnerID)
	}
	if view.Phase != runnerpkg.PhaseSettled {
		t.Fatalf("Phase = %q, want %q", view.Phase, runnerpkg.PhaseSettled)
	}
	if view.Status != runnerpkg.RunnerStatusIdle {
		t.Fatalf("Status = %q, want %q", view.Status, runnerpkg.RunnerStatusIdle)
	}
	if len(view.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(view.Nodes))
	}
	if got := view.Nodes["plan"]; got.SkillName != "planner" || got.TaskDescription != "Draft a plan" {
		t.Fatalf("plan node = %+v, want planner/Draft a plan", got)
	}
	report := view.Nodes["report"]
	if report.SkillName != "reporter" || report.TaskDescription != "Write the report" {
		t.Fatalf("report node = %+v, want reporter/Write the report", report)
	}
	if len(report.DependsOn) != 1 || report.DependsOn[0] != "plan" {
		t.Fatalf("report depends_on = %v, want [plan]", report.DependsOn)
	}
	if len(view.Artifacts) != 1 {
		t.Fatalf("len(Artifacts) = %d, want 1", len(view.Artifacts))
	}
	if got := view.Artifacts[0]; got.Path != "/tmp/report.md" || got.NodeID != "report" {
		t.Fatalf("artifact = %+v, want report artifact path", got)
	}
	if view.SnapshotCursor().EventSequence != 2 {
		t.Fatalf("cursor event sequence = %d, want 2", view.SnapshotCursor().EventSequence)
	}
	if eventLog.lastFrom != 1 {
		t.Fatalf("event log from = %d, want 1", eventLog.lastFrom)
	}
}

func TestReplayDescribeViewResumesFromCursorWithoutDuplicates(t *testing.T) {
	t.Parallel()

	const requestID = "req_resume"
	const runnerID = "run_resume"
	firstLog := &stubEventLogStore{events: map[string][]streampkg.Event{
		requestID: {
			rawBundleEvent(requestID, 1,
				logicalEvent(streampkg.EventStatusProgress, streampkg.Source{Layer: "orchestrator", ID: "orchestrator"}, streampkg.StatusRunning, streampkg.Scope{}, map[string]any{
					"message":   "runner created",
					"runner_id": runnerID,
				}),
				logicalEvent(streampkg.EventRunnerNodeAdded, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusPending, streampkg.Scope{RunnerID: runnerID, NodeID: "report"}, map[string]any{
					"event":            runnerpkg.EventNodeAdded.String(),
					"node_id":          "report",
					"skill_name":       "reporter",
					"task_description": "Write the report",
				}),
			),
		},
	}}

	view, err := ReplayDescribeView(context.Background(), requestID, nil, storepkg.SnapshotCursor{}, firstLog)
	if err != nil {
		t.Fatalf("ReplayDescribeView(first): %v", err)
	}
	cursor := view.SnapshotCursor()
	if cursor.EventSequence != 1 {
		t.Fatalf("cursor event sequence after first replay = %d, want 1", cursor.EventSequence)
	}

	secondLog := &stubEventLogStore{events: map[string][]streampkg.Event{
		requestID: {
			firstLog.events[requestID][0],
			rawBundleEvent(requestID, 2,
				logicalEvent(streampkg.EventArtifactCreated, streampkg.Source{Layer: "agent", ID: "report", ParentID: runnerID}, streampkg.StatusCompleted, streampkg.Scope{RunnerID: runnerID, NodeID: "report"}, map[string]any{
					"path":          "/tmp/resume.md",
					"resource_type": "file",
					"description":   "resumed artifact",
					"stage":         "execute",
				}),
				logicalEvent(streampkg.EventRunnerPhaseChanged, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusCompleted, streampkg.Scope{RunnerID: runnerID}, map[string]any{
					"phase":  string(runnerpkg.PhaseSettled),
					"status": string(runnerpkg.RunnerStatusIdle),
				}),
			),
		},
	}}

	view, err = ReplayDescribeView(context.Background(), requestID, view, cursor, secondLog)
	if err != nil {
		t.Fatalf("ReplayDescribeView(second): %v", err)
	}
	if secondLog.lastFrom != 2 {
		t.Fatalf("event log from = %d, want 2", secondLog.lastFrom)
	}
	if len(view.Nodes) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1", len(view.Nodes))
	}
	if len(view.Artifacts) != 1 {
		t.Fatalf("len(Artifacts) = %d, want 1", len(view.Artifacts))
	}
	if got := view.Artifacts[0]; got.Path != "/tmp/resume.md" {
		t.Fatalf("artifact path = %q, want /tmp/resume.md", got.Path)
	}
	if view.SnapshotCursor().EventSequence != 2 {
		t.Fatalf("cursor event sequence after resume = %d, want 2", view.SnapshotCursor().EventSequence)
	}
}

func TestReplayDescribeViewMarksStatusFailedAfterPhaseUpdate(t *testing.T) {
	t.Parallel()

	const requestID = "req_failed"
	const runnerID = "run_failed"
	eventLog := &stubEventLogStore{events: map[string][]streampkg.Event{
		requestID: {
			rawBundleEvent(requestID, 1,
				logicalEvent(streampkg.EventRunnerPhaseChanged, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusRunning, streampkg.Scope{RunnerID: runnerID}, map[string]any{
					"phase":  string(runnerpkg.PhaseExecuting),
					"status": string(runnerpkg.RunnerStatusRunning),
				}),
			),
			rawEvent(requestID, 2, streampkg.EventStatusFailed, streampkg.Source{Layer: "orchestrator", ID: "orchestrator"}, streampkg.StatusFailed, map[string]any{
				"message":   "request rejected",
				"runner_id": runnerID,
			}),
		},
	}}

	view, err := ReplayDescribeView(context.Background(), requestID, nil, storepkg.SnapshotCursor{}, eventLog)
	if err != nil {
		t.Fatalf("ReplayDescribeView: %v", err)
	}
	if view.Status != runnerpkg.RunnerStatusFailed {
		t.Fatalf("Status = %q, want %q", view.Status, runnerpkg.RunnerStatusFailed)
	}
}

func TestDescribeRequest_RebuildsViewAndSavesCursor(t *testing.T) {
	t.Parallel()

	const requestID = "req_describe_method"
	const runnerID = "run_describe_method"
	eventLog := &stubEventLogStore{events: map[string][]streampkg.Event{
		requestID: {
			rawEvent(requestID, 1, streampkg.EventStatusProgress, streampkg.Source{Layer: "orchestrator", ID: "orchestrator"}, streampkg.StatusRunning, map[string]any{
				"message":   "runner created",
				"runner_id": runnerID,
			}),
			rawBundleEvent(requestID, 2,
				logicalEvent(streampkg.EventRunnerPhaseChanged, streampkg.Source{Layer: "runner", ID: runnerID}, streampkg.StatusCompleted, streampkg.Scope{RunnerID: runnerID}, map[string]any{
					"phase":  string(runnerpkg.PhaseSettled),
					"status": string(runnerpkg.RunnerStatusIdle),
				}),
			),
		},
	}}
	cursorStore := &stubSnapshotCursorStore{}
	o := newOrchestrator(testLogger, WithPersistence(newTestPersistenceBackend(
		func(b *testPersistenceBackend) { b.eventLog = eventLog },
		func(b *testPersistenceBackend) { b.cursor = cursorStore },
	)))

	view, err := o.DescribeRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("DescribeRequest: %v", err)
	}
	if view.RequestID != requestID {
		t.Fatalf("RequestID = %q, want %q", view.RequestID, requestID)
	}
	if view.RunnerID != runnerID {
		t.Fatalf("RunnerID = %q, want %q", view.RunnerID, runnerID)
	}
	if cursorStore.saved.RequestID != requestID {
		t.Fatalf("saved cursor requestID = %q, want %q", cursorStore.saved.RequestID, requestID)
	}
	if cursorStore.saved.EventSequence != 2 {
		t.Fatalf("saved cursor event sequence = %d, want 2", cursorStore.saved.EventSequence)
	}
	if eventLog.lastFrom != 1 {
		t.Fatalf("event log from = %d, want 1", eventLog.lastFrom)
	}
}

type stubEventLogStore struct {
	events    map[string][]streampkg.Event
	lastFrom  uint64
	lastScope streampkg.Scope
}

type stubSnapshotCursorStore struct {
	saved storepkg.SnapshotCursor
	load  storepkg.SnapshotCursor
	err   error
}

func (s *stubEventLogStore) AppendEvent(context.Context, streampkg.Event) error {
	panic("AppendEvent should not be called in describe replay tests")
}

func (s *stubEventLogStore) LoadEvents(_ context.Context, scope streampkg.Scope, from uint64, _ streampkg.Filter) ([]streampkg.Event, error) {
	s.lastScope = scope
	s.lastFrom = from
	all := s.events[scope.RequestID]
	if len(all) == 0 {
		return nil, nil
	}
	out := make([]streampkg.Event, 0, len(all))
	for _, event := range all {
		if event.SequenceNumber >= from {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *stubSnapshotCursorStore) SaveCursor(_ context.Context, cursor storepkg.SnapshotCursor) error {
	s.saved = cursor
	return s.err
}

func (s *stubSnapshotCursorStore) LoadCursor(context.Context, string) (storepkg.SnapshotCursor, error) {
	if s.err != nil {
		return storepkg.SnapshotCursor{}, s.err
	}
	return s.load, nil
}

func rawEvent(requestID string, sequence uint64, eventType string, source streampkg.Source, status string, delta map[string]any) streampkg.Event {
	raw, err := json.Marshal(delta)
	if err != nil {
		panic(err)
	}
	return streampkg.Event{
		EventType:      eventType,
		From:           source,
		SequenceNumber: sequence,
		LogicalTime:    sequence,
		Status:         status,
		Timestamp:      time.Unix(1_700_000_000+int64(sequence), 0).UTC(),
		Scope:          streampkg.Scope{RequestID: requestID},
		Delta:          raw,
	}
}

func rawBundleEvent(requestID string, sequence uint64, events ...streampkg.Event) streampkg.Event {
	delta, err := streampkg.EncodeEventBatch(streampkg.EventBatch{TickID: sequence, Events: events})
	if err != nil {
		panic(err)
	}
	return streampkg.Event{
		EventType:      streampkg.EventMergeBundle,
		From:           streampkg.Source{Layer: "hub", ID: "hub"},
		SequenceNumber: sequence,
		LogicalTime:    sequence,
		Status:         streampkg.StatusCompleted,
		Timestamp:      time.Unix(1_700_000_000+int64(sequence), 0).UTC(),
		Scope:          streampkg.Scope{RequestID: requestID},
		Delta:          delta,
	}
}

func logicalEvent(eventType string, source streampkg.Source, status string, scope streampkg.Scope, delta map[string]any) streampkg.Event {
	raw, err := json.Marshal(delta)
	if err != nil {
		panic(err)
	}
	return streampkg.Event{
		EventType: eventType,
		From:      source,
		Status:    status,
		Scope:     scope,
		Delta:     raw,
	}
}
