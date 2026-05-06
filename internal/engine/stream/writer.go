package stream

import (
	"context"
	"encoding/json"
)

// Writer is a convenience producer bound to one source and status provider.
type Writer struct {
	stream *Stream
	source Source
	status func() string
}

// Writer returns a producer helper for source.
func (s *Stream) Writer(source Source, status func() string) *Writer {
	return &Writer{
		stream: s,
		source: source,
		status: status,
	}
}

// Emit marshals delta and emits eventType from the writer's source.
func (w *Writer) Emit(ctx context.Context, eventType string, delta any, metadata map[string]any) error {
	raw, err := marshalDelta(delta)
	if err != nil {
		return err
	}
	status := StatusRunning
	if w.status != nil {
		status = w.status()
	}
	return w.stream.Emit(ctx, Event{
		EventType: eventType,
		From:      w.source,
		Status:    status,
		Delta:     raw,
		Metadata:  cloneMetadata(metadata),
	})
}

// Status emits a generic status.progress event carrying status as the event
// status and delta as the payload.
func (w *Writer) Status(ctx context.Context, status string, delta any) error {
	raw, err := marshalDelta(delta)
	if err != nil {
		return err
	}
	return w.stream.Emit(ctx, Event{
		EventType: EventStatusProgress,
		From:      w.source,
		Status:    status,
		Delta:     raw,
	})
}

func marshalDelta(delta any) (json.RawMessage, error) {
	if delta == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := delta.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, ErrInvalidEvent
		}
		return append(json.RawMessage(nil), raw...), nil
	}
	b, err := json.Marshal(delta)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}
