package stream

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPR3EventFamilyConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "exchange", got: EventExchangePublished, want: "exchange.published"},
		{name: "handoff emitted", got: EventHandoffEmitted, want: "handoff.emitted"},
		{name: "handoff delivered", got: EventHandoffDelivered, want: "handoff.delivered"},
		{name: "memo snapshot", got: EventMemoSnapshot, want: "memo.snapshot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("constant = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestPR3EventPayloadJSONRoundTrip(t *testing.T) {
	created := time.Date(2026, 5, 9, 9, 43, 21, 0, time.UTC)

	exchange := ExchangePublishedPayload{
		RunnerID:  "run_1",
		NodeID:    "node_1",
		Path:      "exchange/0001-node_1.md",
		Document:  "---\nkind: dango.exchange_doc\n---\n",
		Title:     "normalised schema",
		CreatedAt: created,
	}
	handoff := HandoffEmittedPayload{
		RunnerID: "run_1",
		FromNode: "node_1",
		ToNodes:  []string{"node_2", "node_3"},
		Intent:   "bootstrap",
		Path:     "skills/node_1/outbox/handoff.md",
		Document: "---\nkind: dango.handoff_doc\n---\n",
		Artifacts: []HandoffArtifactPayload{
			{Path: "artifacts/data.csv", Type: "csv", Description: "training rows"},
		},
		CreatedAt: created,
	}
	delivered := HandoffDeliveredPayload{
		RunnerID:      "run_1",
		FromNode:      "node_1",
		ToNode:        "node_2",
		InboxPath:     "skills/node_2/inbox/node_1",
		HandoffPath:   "skills/node_2/inbox/node_1/handoff.md",
		ArtifactPaths: []string{"skills/node_2/inbox/node_1/artifacts/data.csv"},
		Artifacts: []HandoffArtifactPayload{
			{Path: "artifacts/data.csv", Type: "csv"},
		},
		DeliveredAt: created,
	}
	memo := MemoSnapshotPayload{
		RunnerID:    "run_1",
		NodeID:      "node_2",
		SkillName:   "analysis_skill",
		SnapshotDir: "archive/memo/node_2",
		SnapshotAt:  created,
	}

	testCases := []struct {
		name string
		in   any
		out  any
	}{
		{name: "exchange", in: exchange, out: &ExchangePublishedPayload{}},
		{name: "handoff emitted", in: handoff, out: &HandoffEmittedPayload{}},
		{name: "handoff delivered", in: delivered, out: &HandoffDeliveredPayload{}},
		{name: "memo snapshot", in: memo, out: &MemoSnapshotPayload{}},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := json.Unmarshal(raw, tt.out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, err := json.Marshal(tt.out)
			if err != nil {
				t.Fatalf("marshal decoded: %v", err)
			}
			if string(raw) != string(got) {
				t.Fatalf("round trip mismatch:\nraw=%s\ngot=%s", raw, got)
			}
		})
	}
}

func TestPR3EventFamilyFilterAndReplay(t *testing.T) {
	s := New(Scope{RequestID: "req_1", RunnerID: "run_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	emitPayload := func(eventType string, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if err := s.Emit(context.Background(), Event{
			EventType: eventType,
			From:      Source{Layer: "runner", ID: "run_1"},
			Status:    StatusCompleted,
			Delta:     raw,
		}); err != nil {
			t.Fatalf("emit %s: %v", eventType, err)
		}
	}

	emitPayload(EventExchangePublished, ExchangePublishedPayload{
		RunnerID: "run_1",
		NodeID:   "node_1",
		Path:     "exchange/0001-node_1.md",
		Document: "---\nkind: dango.exchange_doc\n---\n",
	})
	emitPayload(EventHandoffEmitted, HandoffEmittedPayload{
		RunnerID: "run_1",
		FromNode: "node_1",
		ToNodes:  []string{"node_2"},
		Path:     "skills/node_1/outbox/handoff.md",
		Document: "---\nkind: dango.handoff_doc\n---\n",
	})
	emitPayload(EventHandoffDelivered, HandoffDeliveredPayload{
		RunnerID:    "run_1",
		FromNode:    "node_1",
		ToNode:      "node_2",
		InboxPath:   "skills/node_2/inbox/node_1",
		HandoffPath: "skills/node_2/inbox/node_1/handoff.md",
	})
	emitPayload(EventMemoSnapshot, MemoSnapshotPayload{
		RunnerID:    "run_1",
		NodeID:      "node_2",
		SnapshotDir: "archive/memo/node_2",
	})

	handoffSub, err := s.Subscribe(Filter{Prefixes: []string{"handoff."}}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Subscribe handoff: %v", err)
	}
	defer handoffSub.Cancel()

	first := receiveEvent(t, handoffSub.Events())
	second := receiveEvent(t, handoffSub.Events())
	if first.EventType != EventHandoffEmitted {
		t.Fatalf("first handoff event = %q, want %q", first.EventType, EventHandoffEmitted)
	}
	if second.EventType != EventHandoffDelivered {
		t.Fatalf("second handoff event = %q, want %q", second.EventType, EventHandoffDelivered)
	}
	assertNoEvent(t, handoffSub.Events())

	replay, err := s.Replay(Filter{EventTypes: []string{EventExchangePublished, EventMemoSnapshot}}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replay) != 2 {
		t.Fatalf("replay len = %d, want 2", len(replay))
	}
	if replay[0].EventType != EventExchangePublished {
		t.Fatalf("replay[0].event_type = %q, want %q", replay[0].EventType, EventExchangePublished)
	}
	if replay[1].EventType != EventMemoSnapshot {
		t.Fatalf("replay[1].event_type = %q, want %q", replay[1].EventType, EventMemoSnapshot)
	}
}
