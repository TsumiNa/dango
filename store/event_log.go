package store

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	streampkg "github.com/tsumina/dango/stream"
)

// EventLogStore records and loads raw request stream event frames.
//
// Implementations belong to persistence packages such as internal/store/sqlite.
// They are not part of stream delivery; callers should use them from dedicated
// persistence goroutines so writes do not block normal subscribers.
type EventLogStore interface {
	// AppendEvent stores one prepared raw request-stream frame.
	AppendEvent(ctx context.Context, event streampkg.Event) error

	// LoadEvents returns matching raw frames in ascending sequence order.
	//
	// The returned events belong to the persisted request scope identified by
	// scope. When from is 0, implementations should load from the beginning of
	// the stored event log.
	LoadEvents(ctx context.Context, scope streampkg.Scope, from uint64, filter streampkg.Filter) ([]streampkg.Event, error)
}

// JSONEventLogStore persists raw request stream frames as JSON lines under one
// directory.
//
// The store is safe for concurrent use within one process. Appends and loads
// for the same request id are serialized through a per-request mutex.
type JSONEventLogStore struct {
	root  string
	locks stripedStoreLocks
}

// NewJSONEventLogStore returns a JSON-backed request event-log store rooted at
// dir.
func NewJSONEventLogStore(dir string) (*JSONEventLogStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("store: JSONEventLogStore requires a non-empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: JSONEventLogStore mkdir %q: %w", dir, err)
	}
	return &JSONEventLogStore{root: dir, locks: newStripedStoreLocks(defaultStripedStoreLockCount)}, nil
}

// Root returns the directory backing the store.
func (s *JSONEventLogStore) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// AppendEvent implements [EventLogStore].
func (s *JSONEventLogStore) AppendEvent(_ context.Context, event streampkg.Event) error {
	if s == nil {
		return fmt.Errorf("store: JSONEventLogStore.AppendEvent called on nil store")
	}
	if err := validateJSONEventLogEvent(event); err != nil {
		return err
	}
	path, err := s.path(event.Scope.RequestID)
	if err != nil {
		return err
	}
	unlock := s.locks.Lock(event.Scope.RequestID)
	defer unlock()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("store: open event log %q: %w", path, err)
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("store: encode event log %q/%d: %w", event.Scope.RequestID, event.SequenceNumber, err)
	}
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("store: write event log %q/%d: %w", event.Scope.RequestID, event.SequenceNumber, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("store: sync event log %q/%d: %w", event.Scope.RequestID, event.SequenceNumber, err)
	}
	return nil
}

// LoadEvents implements [EventLogStore].
func (s *JSONEventLogStore) LoadEvents(_ context.Context, scope streampkg.Scope, from uint64, filter streampkg.Filter) ([]streampkg.Event, error) {
	if s == nil {
		return nil, fmt.Errorf("store: JSONEventLogStore.LoadEvents called on nil store")
	}
	if scope.RequestID == "" {
		return nil, fmt.Errorf("store: request event replay requires scope.request_id")
	}
	path, err := s.path(scope.RequestID)
	if err != nil {
		return nil, err
	}
	unlock := s.locks.Lock(scope.RequestID)
	defer unlock()
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: read event log %q: %w", path, err)
	}
	defer file.Close()
	start := from
	if start == 0 {
		start = 1
	}
	decoder := json.NewDecoder(bufio.NewReader(file))
	out := make([]streampkg.Event, 0, 16)
	for i := 1; ; i++ {
		var event streampkg.Event
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("store: decode event log %q entry %d: %w", scope.RequestID, i, err)
		}
		if event.SequenceNumber < start {
			continue
		}
		if filter.Match(event) {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *JSONEventLogStore) path(requestID string) (string, error) {
	if err := validateStoreID("request", requestID); err != nil {
		return "", err
	}
	return filepath.Join(s.root, requestID+".jsonl"), nil
}

func validateJSONEventLogEvent(event streampkg.Event) error {
	if event.Scope.RequestID == "" {
		return fmt.Errorf("store: request event missing scope.request_id")
	}
	if event.SequenceNumber == 0 {
		return fmt.Errorf("store: request event missing sequence_number")
	}
	if event.LogicalTime == 0 {
		return fmt.Errorf("store: request event missing logical_time")
	}
	if event.EventType == "" {
		return fmt.Errorf("store: request event missing event_type")
	}
	if event.From.Layer == "" {
		return fmt.Errorf("store: request event missing from.layer")
	}
	if event.Status == "" {
		return fmt.Errorf("store: request event missing status")
	}
	if event.Timestamp.IsZero() {
		return fmt.Errorf("store: request event missing timestamp")
	}
	if event.Delta != nil && !json.Valid(event.Delta) {
		return fmt.Errorf("store: request event has invalid delta JSON")
	}
	return nil
}

func validateStoreID(kind, id string) error {
	if id == "" {
		return fmt.Errorf("store: %s id must not be empty", kind)
	}
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("store: %s id %q contains path separators", kind, id)
	}
	if strings.HasPrefix(id, ".") {
		return fmt.Errorf("store: %s id %q must not start with '.'", kind, id)
	}
	return nil
}
