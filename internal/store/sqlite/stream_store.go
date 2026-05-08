package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
	sqldb "github.com/tsumina/dango/internal/store/sqlite/db"
)

var _ streampkg.Store = (*StreamStore)(nil)

// StreamStore persists request-scoped stream events in SQLite.
//
// StreamStore keeps a reference to the shared [Store] and is safe to share
// across goroutines because it delegates to the underlying sql.DB-backed query
// layer.
type StreamStore struct {
	store *Store
}

// NewStreamStore returns a SQLite-backed adapter that implements the stream
// event store contract for request event logs.
func NewStreamStore(store *Store) *StreamStore {
	return &StreamStore{store: store}
}

// Append stores one prepared request-stream event as a raw JSON frame plus
// helper columns used for replay lookups.
func (s *StreamStore) Append(ctx context.Context, event streampkg.Event) error {
	if s == nil || s.store == nil || s.store.queries == nil {
		return fmt.Errorf("sqlite: StreamStore.Append called on nil store")
	}
	if err := validateStoredStreamEvent(event); err != nil {
		return err
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf(
			"sqlite: encode request stream event %q/%d: %w",
			event.Scope.RequestID,
			event.SequenceNumber,
			err,
		)
	}
	if err := s.store.queries.InsertRequestStreamEvent(ctx, sqldb.InsertRequestStreamEventParams{
		RequestID:      event.Scope.RequestID,
		SequenceNumber: int64(event.SequenceNumber),
		LogicalTime:    int64(event.LogicalTime),
		EventType:      event.EventType,
		SourceLayer:    event.From.Layer,
		SourceID:       nullableString(event.From.ID),
		SourceParentID: nullableString(event.From.ParentID),
		RunnerID:       nullableString(event.Scope.RunnerID),
		NodeID:         nullableString(event.Scope.NodeID),
		SessionID:      nullableString(event.Scope.SessionID),
		Status:         event.Status,
		Timestamp:      event.Timestamp.UTC().Format(time.RFC3339Nano),
		RawEventJson:   string(raw),
	}); err != nil {
		return fmt.Errorf(
			"sqlite: insert request stream event %q/%d: %w",
			event.Scope.RequestID,
			event.SequenceNumber,
			err,
		)
	}
	return nil
}

// Load returns request-scoped events in ascending sequence order.
func (s *StreamStore) Load(ctx context.Context, scope streampkg.Scope, from uint64, filter streampkg.Filter) ([]streampkg.Event, error) {
	if s == nil || s.store == nil || s.store.queries == nil {
		return nil, fmt.Errorf("sqlite: StreamStore.Load called on nil store")
	}
	if scope.RequestID == "" {
		return nil, fmt.Errorf("sqlite: request stream replay requires scope.request_id")
	}
	if from > math.MaxInt64 {
		return nil, nil
	}
	loadScope := mergeLoadScope(scope, filter.Scope)
	rows, err := s.store.queries.ListRequestStreamEvents(ctx, sqldb.ListRequestStreamEventsParams{
		RequestID:          scope.RequestID,
		FromSequenceNumber: int64(normalizeFrom(from)),
		RunnerID:           nullableString(loadScope.RunnerID),
		NodeID:             nullableString(loadScope.NodeID),
		SessionID:          nullableString(loadScope.SessionID),
		Status:             optionalSingleValue(filter.Statuses),
		EventType:          optionalExactEventType(filter),
		EventTypePrefix:    optionalEventTypePrefix(filter),
		SourceLayer:        optionalSourceLayer(filter),
		SourceID:           optionalSourceID(filter),
		SourceParentID:     optionalSourceParentID(filter),
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite: load request stream events for %q: %w", scope.RequestID, err)
	}

	requestScope := streampkg.Filter{Scope: scope}
	out := make([]streampkg.Event, 0, len(rows))
	for _, row := range rows {
		var event streampkg.Event
		if err := json.Unmarshal([]byte(row.RawEventJson), &event); err != nil {
			return nil, fmt.Errorf(
				"sqlite: decode request stream event %q/%d: %w",
				scope.RequestID,
				row.SequenceNumber,
				err,
			)
		}
		if !requestScope.Match(event) {
			continue
		}
		if filter.Match(event) {
			out = append(out, event)
		}
	}
	return out, nil
}

func validateStoredStreamEvent(event streampkg.Event) error {
	if event.Scope.RequestID == "" {
		return fmt.Errorf("sqlite: request stream event missing scope.request_id")
	}
	if event.SequenceNumber == 0 {
		return fmt.Errorf("sqlite: request stream event missing sequence_number")
	}
	if event.LogicalTime == 0 {
		return fmt.Errorf("sqlite: request stream event missing logical_time")
	}
	if event.SequenceNumber > math.MaxInt64 {
		return fmt.Errorf("sqlite: request stream event sequence_number %d exceeds int64", event.SequenceNumber)
	}
	if event.LogicalTime > math.MaxInt64 {
		return fmt.Errorf("sqlite: request stream event logical_time %d exceeds int64", event.LogicalTime)
	}
	if event.EventType == "" {
		return fmt.Errorf("sqlite: request stream event missing event_type")
	}
	if event.From.Layer == "" {
		return fmt.Errorf("sqlite: request stream event missing from.layer")
	}
	if event.Status == "" {
		return fmt.Errorf("sqlite: request stream event missing status")
	}
	if event.Timestamp.IsZero() {
		return fmt.Errorf("sqlite: request stream event missing timestamp")
	}
	return nil
}

func normalizeFrom(from uint64) uint64 {
	if from == 0 {
		return 1
	}
	return from
}

func mergeLoadScope(scope streampkg.Scope, filterScope streampkg.Scope) streampkg.Scope {
	out := scope
	if out.RequestID == "" {
		out.RequestID = filterScope.RequestID
	}
	if out.RunnerID == "" {
		out.RunnerID = filterScope.RunnerID
	}
	if out.NodeID == "" {
		out.NodeID = filterScope.NodeID
	}
	if out.SessionID == "" {
		out.SessionID = filterScope.SessionID
	}
	return out
}

func optionalSingleValue(values []string) sql.NullString {
	if len(values) != 1 || values[0] == "" {
		return sql.NullString{}
	}
	return nullableString(values[0])
}

func optionalExactEventType(filter streampkg.Filter) sql.NullString {
	if len(filter.EventTypes) != 1 || len(filter.Prefixes) != 0 {
		return sql.NullString{}
	}
	return nullableString(filter.EventTypes[0])
}

func optionalEventTypePrefix(filter streampkg.Filter) sql.NullString {
	if len(filter.Prefixes) != 1 || len(filter.EventTypes) != 0 {
		return sql.NullString{}
	}
	return nullableString(filter.Prefixes[0])
}

func optionalSourceLayer(filter streampkg.Filter) sql.NullString {
	if len(filter.Sources) != 1 {
		return sql.NullString{}
	}
	return nullableString(filter.Sources[0].Layer)
}

func optionalSourceID(filter streampkg.Filter) sql.NullString {
	if len(filter.Sources) != 1 {
		return sql.NullString{}
	}
	return nullableString(filter.Sources[0].ID)
}

func optionalSourceParentID(filter streampkg.Filter) sql.NullString {
	if len(filter.Sources) != 1 {
		return sql.NullString{}
	}
	return nullableString(filter.Sources[0].ParentID)
}
