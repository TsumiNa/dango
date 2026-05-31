package stream

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestWriterMarshalsDeltaAndUsesStatus(t *testing.T) {
	s := New(Scope{SessionID: "sess_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{Prefixes: []string{"llm."}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	writer := s.Writer(Source{Layer: "conversation", ID: "conv_1"}, func() string {
		return StatusRunning
	})
	err = writer.Emit(t.Context(), EventLLMOutputDelta, map[string]string{"text": "hi"}, map[string]any{"model": "test"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := receiveEvent(t, sub.Events())
	if got.Status != StatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.Scope.SessionID != "sess_1" {
		t.Errorf("session id = %q, want sess_1", got.Scope.SessionID)
	}
	if got.Metadata["model"] != "test" {
		t.Errorf("metadata model = %v, want test", got.Metadata["model"])
	}
	var delta map[string]string
	if err := json.Unmarshal(got.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta["text"] != "hi" {
		t.Errorf("delta text = %q, want hi", delta["text"])
	}
}

func TestWriterRejectsInvalidRawDelta(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)

	writer := s.Writer(Source{Layer: "conversation"}, nil)
	err := writer.Emit(t.Context(), EventLLMOutputDelta, json.RawMessage(`{`), nil)
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Emit err = %v, want ErrInvalidEvent", err)
	}
}

func TestWriterStatusEvent(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{EventTypes: []string{EventStatusProgress}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	writer := s.Writer(Source{Layer: "runner"}, nil)
	if err := writer.Status(t.Context(), StatusCompleted, "done"); err != nil {
		t.Fatalf("Status: %v", err)
	}

	got := receiveEvent(t, sub.Events())
	if got.EventType != EventStatusProgress {
		t.Errorf("event type = %q, want %q", got.EventType, EventStatusProgress)
	}
	if got.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if string(got.Delta) != `"done"` {
		t.Errorf("delta = %s, want done string", got.Delta)
	}
}
