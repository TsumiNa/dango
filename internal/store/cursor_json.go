package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JSONSnapshotCursorStore persists one snapshot cursor JSON file per request.
//
// The store is safe for concurrent use within one process. Saves and loads for
// the same request id are serialized through a per-request mutex.
type JSONSnapshotCursorStore struct {
	root string

	mu     sync.Mutex
	states map[string]*sync.Mutex
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
	return &JSONSnapshotCursorStore{root: dir, states: make(map[string]*sync.Mutex)}, nil
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
	lock := s.getState(cursor.RequestID)
	lock.Lock()
	defer lock.Unlock()
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
	lock := s.getState(requestID)
	lock.Lock()
	defer lock.Unlock()
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

func (s *JSONSnapshotCursorStore) getState(requestID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lock, ok := s.states[requestID]; ok {
		return lock
	}
	lock := &sync.Mutex{}
	s.states[requestID] = lock
	return lock
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
