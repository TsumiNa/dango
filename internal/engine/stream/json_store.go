package stream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

const jsonStoreFile = "events.jsonl"

// JSONStore is a filesystem-backed [Store] that persists one stream as a JSON
// Lines file under a root directory.
//
// Append uses flock to serialize writers across goroutines and processes on the
// same root. Load tolerates a trailing partially-written line and returns the
// fully-written prefix events in ascending sequence order.
//
// One JSONStore is intended to back one logical stream archive. Construct a new
// store per persisted stream root rather than sharing one root across unrelated
// streams.
type JSONStore struct {
	root string
	mu   sync.Mutex
}

// NewJSONStore returns a JSONStore rooted at dir. The directory is created with
// 0o755 if it does not already exist.
func NewJSONStore(dir string) (*JSONStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("stream: JSONStore requires a non-empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("stream: JSONStore mkdir %q: %w", dir, err)
	}
	return &JSONStore{root: dir}, nil
}

// Root returns the directory backing the store.
func (s *JSONStore) Root() string { return s.root }

func (s *JSONStore) path() string {
	return filepath.Join(s.root, jsonStoreFile)
}

// Append implements [Store].
func (s *JSONStore) Append(_ context.Context, event Event) error {
	if s == nil {
		return fmt.Errorf("stream: JSONStore.Append called on nil store")
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("stream: JSONStore encode event: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.path()
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("stream: JSONStore open %q: %w", p, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("stream: JSONStore flock %q: %w", p, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("stream: JSONStore write %q: %w", p, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("stream: JSONStore sync %q: %w", p, err)
	}
	return nil
}

// Load implements [Store].
func (s *JSONStore) Load(_ context.Context, scope Scope, from uint64, filter Filter) ([]Event, error) {
	if s == nil {
		return nil, fmt.Errorf("stream: JSONStore.Load called on nil store")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.path()
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stream: JSONStore read %q: %w", p, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return nil, fmt.Errorf("stream: JSONStore flock %q: %w", p, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	events, err := decodeStoredEvents(f, p)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}

	scopeFilter := Filter{Scope: scope}
	var replay []Event
	for _, event := range events {
		if from > 0 && event.SequenceNumber < from {
			continue
		}
		if !scopeFilter.matchScope(event.Scope) {
			continue
		}
		if filter.Match(event) {
			replay = append(replay, event)
		}
	}
	return replay, nil
}

func decodeStoredEvents(r io.Reader, path string) ([]Event, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var (
		out      []Event
		pending  Event
		pendOK   bool
		pendLine int
		lineNo   int
	)
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if pendOK {
			out = append(out, pending)
		} else if pendLine > 0 {
			return nil, fmt.Errorf("stream: JSONStore decode %q: corrupt event at line %d", path, pendLine)
		}
		pending = Event{}
		if err := json.Unmarshal(line, &pending); err != nil {
			pendOK = false
			pendLine = lineNo
			continue
		}
		pendOK = true
		pendLine = lineNo
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream: JSONStore scan %q: %w", path, err)
	}
	if pendOK {
		out = append(out, pending)
	}
	return out, nil
}
