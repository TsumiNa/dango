package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrSnapshotCursorNotFound is returned when a request has no stored cursor.
var ErrSnapshotCursorNotFound = errors.New("store: snapshot cursor not found")

// SnapshotCursor records the latest persisted replay position for one request.
//
// EventSequence tracks the last top-level request-stream frame that has been
// materialized into a describe view. CheckpointSequence tracks the latest
// runner checkpoint sequence the describer incorporated when building that
// view.
type SnapshotCursor struct {
	RequestID          string
	RunnerID           string
	CheckpointSequence int64
	EventSequence      uint64
	UpdatedAt          time.Time
}

// SnapshotCursorStore persists per-request describe replay cursors.
type SnapshotCursorStore interface {
	// SaveCursor inserts or replaces the cursor for cursor.RequestID.
	SaveCursor(ctx context.Context, cursor SnapshotCursor) error

	// LoadCursor returns the stored cursor for requestID.
	LoadCursor(ctx context.Context, requestID string) (SnapshotCursor, error)
}

// JSONSnapshotCursorStore persists one snapshot cursor JSON file per request.
//
// The store is safe for concurrent use within one process. Saves and loads for
// the same request id are serialized through a per-request mutex.
type JSONSnapshotCursorStore struct {
	root  string
	locks stripedStoreLocks
}

// NewJSONSnapshotCursorStore returns a JSON-backed snapshot cursor store
// rooted at dir.
func NewJSONSnapshotCursorStore(dir string) (*JSONSnapshotCursorStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("store: JSONSnapshotCursorStore requires a non-empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: JSONSnapshotCursorStore mkdir %q: %w", dir, err)
	}
	return &JSONSnapshotCursorStore{root: dir, locks: newStripedStoreLocks(defaultStripedStoreLockCount)}, nil
}

// Root returns the directory backing the store.
func (s *JSONSnapshotCursorStore) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// SaveCursor implements [SnapshotCursorStore].
func (s *JSONSnapshotCursorStore) SaveCursor(_ context.Context, cursor SnapshotCursor) error {
	if s == nil {
		return fmt.Errorf("store: JSONSnapshotCursorStore.SaveCursor called on nil store")
	}
	if err := validateJSONSnapshotCursor(cursor); err != nil {
		return err
	}
	path, err := s.path(cursor.RequestID)
	if err != nil {
		return err
	}
	unlock := s.locks.Lock(cursor.RequestID)
	defer unlock()
	if cursor.UpdatedAt.IsZero() {
		cursor.UpdatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return fmt.Errorf("store: encode snapshot cursor %q: %w", cursor.RequestID, err)
	}
	temp, err := os.CreateTemp(s.root, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("store: create temp snapshot cursor %q: %w", cursor.RequestID, err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("store: write snapshot cursor %q: %w", cursor.RequestID, err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("store: sync snapshot cursor %q: %w", cursor.RequestID, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("store: close snapshot cursor %q: %w", cursor.RequestID, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("store: replace snapshot cursor %q: %w", cursor.RequestID, err)
	}
	return nil
}

// LoadCursor implements [SnapshotCursorStore].
func (s *JSONSnapshotCursorStore) LoadCursor(_ context.Context, requestID string) (SnapshotCursor, error) {
	if s == nil {
		return SnapshotCursor{}, fmt.Errorf("store: JSONSnapshotCursorStore.LoadCursor called on nil store")
	}
	path, err := s.path(requestID)
	if err != nil {
		return SnapshotCursor{}, err
	}
	unlock := s.locks.Lock(requestID)
	defer unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SnapshotCursor{}, ErrSnapshotCursorNotFound
		}
		return SnapshotCursor{}, fmt.Errorf("store: read snapshot cursor %q: %w", requestID, err)
	}
	var cursor SnapshotCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return SnapshotCursor{}, fmt.Errorf("store: decode snapshot cursor %q: %w", requestID, err)
	}
	return cursor, nil
}

func (s *JSONSnapshotCursorStore) path(requestID string) (string, error) {
	if err := validateStoreID("request", requestID); err != nil {
		return "", err
	}
	return filepath.Join(s.root, requestID+".json"), nil
}

func validateJSONSnapshotCursor(cursor SnapshotCursor) error {
	if cursor.RequestID == "" {
		return fmt.Errorf("store: snapshot cursor missing request_id")
	}
	if cursor.CheckpointSequence < 0 {
		return fmt.Errorf("store: snapshot cursor checkpoint_sequence must be non-negative")
	}
	return nil
}
