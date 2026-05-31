package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	storepkg "github.com/tsumina/dango/store"
	streampkg "github.com/tsumina/dango/stream"
)

var _ storepkg.EventLogStore = (*StreamStore)(nil)

// StreamStore persists request-scoped stream events in Postgres.
type StreamStore struct {
	store *Store
}

// NewStreamStore returns a Postgres-backed event log store for request events.
func NewStreamStore(store *Store) *StreamStore {
	return &StreamStore{store: store}
}

// AppendEvent stores one prepared request-stream event as a raw JSON frame.
func (s *StreamStore) AppendEvent(ctx context.Context, event streampkg.Event) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return fmt.Errorf("postgres: StreamStore.AppendEvent called on nil store")
	}
	if err := validateStoredStreamEvent(event); err != nil {
		return err
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("postgres: encode request stream event %q/%d: %w", event.Scope.RequestID, event.SequenceNumber, err)
	}
	if _, err := s.store.db.ExecContext(ctx, `
		INSERT INTO request_stream_events (
			request_id, sequence_number, logical_time, event_type,
			source_layer, source_id, source_parent_id,
			runner_id, node_id, session_id,
			status, timestamp, raw_event_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb)
	`,
		event.Scope.RequestID,
		int64(event.SequenceNumber),
		int64(event.LogicalTime),
		event.EventType,
		event.From.Layer,
		nullableString(event.From.ID),
		nullableString(event.From.ParentID),
		nullableString(event.Scope.RunnerID),
		nullableString(event.Scope.NodeID),
		nullableString(event.Scope.SessionID),
		event.Status,
		event.Timestamp.UTC(),
		string(raw),
	); err != nil {
		return fmt.Errorf("postgres: insert request stream event %q/%d: %w", event.Scope.RequestID, event.SequenceNumber, err)
	}
	return nil
}

// LoadEvents returns request-scoped events in ascending sequence order.
func (s *StreamStore) LoadEvents(ctx context.Context, scope streampkg.Scope, from uint64, filter streampkg.Filter) ([]streampkg.Event, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, fmt.Errorf("postgres: StreamStore.LoadEvents called on nil store")
	}
	if scope.RequestID == "" {
		return nil, fmt.Errorf("postgres: request stream replay requires scope.request_id")
	}
	if from > math.MaxInt64 {
		return nil, nil
	}
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT sequence_number, raw_event_json::text
		FROM request_stream_events
		WHERE request_id = $1 AND sequence_number >= $2
		ORDER BY sequence_number ASC
	`, scope.RequestID, int64(normalizeFrom(from)))
	if err != nil {
		return nil, fmt.Errorf("postgres: load request stream events for %q: %w", scope.RequestID, err)
	}
	defer rows.Close()

	requestScope := streampkg.Filter{Scope: scope}
	out := make([]streampkg.Event, 0)
	for rows.Next() {
		var sequence int64
		var raw string
		if err := rows.Scan(&sequence, &raw); err != nil {
			return nil, fmt.Errorf("postgres: scan request stream event row for %q: %w", scope.RequestID, err)
		}
		var event streampkg.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("postgres: decode request stream event %q/%d: %w", scope.RequestID, sequence, err)
		}
		if !requestScope.Match(event) {
			continue
		}
		if filter.Match(event) {
			out = append(out, event)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate request stream events for %q: %w", scope.RequestID, err)
	}
	return out, nil
}

func validateStoredStreamEvent(event streampkg.Event) error {
	if event.Scope.RequestID == "" {
		return fmt.Errorf("postgres: request stream event missing scope.request_id")
	}
	if event.SequenceNumber == 0 {
		return fmt.Errorf("postgres: request stream event missing sequence_number")
	}
	if event.LogicalTime == 0 {
		return fmt.Errorf("postgres: request stream event missing logical_time")
	}
	if event.SequenceNumber > math.MaxInt64 {
		return fmt.Errorf("postgres: request stream event sequence_number %d exceeds int64", event.SequenceNumber)
	}
	if event.LogicalTime > math.MaxInt64 {
		return fmt.Errorf("postgres: request stream event logical_time %d exceeds int64", event.LogicalTime)
	}
	if event.EventType == "" {
		return fmt.Errorf("postgres: request stream event missing event_type")
	}
	if event.From.Layer == "" {
		return fmt.Errorf("postgres: request stream event missing from.layer")
	}
	if event.Status == "" {
		return fmt.Errorf("postgres: request stream event missing status")
	}
	if event.Timestamp.IsZero() {
		return fmt.Errorf("postgres: request stream event missing timestamp")
	}
	return nil
}

func normalizeFrom(from uint64) uint64 {
	if from == 0 {
		return 1
	}
	return from
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
