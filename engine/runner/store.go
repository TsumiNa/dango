package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// JSONRunnerStore is a filesystem-backed RunnerStore that writes one JSONL
// file per runner under a root directory.
//
// Append uses flock to serialise writers across goroutines and processes. A
// trailing partial line is tolerated by Load and ignored until the next clean
// append rewrites the sequence cache.
type JSONRunnerStore struct {
	root string

	mu     sync.Mutex
	states map[string]*runnerLogState
}

type runnerLogState struct {
	sync.Mutex
	lastSeq int64
	hasInit bool
	size    int64
	cached  bool
}

// NewJSONRunnerStore returns a JSONRunnerStore rooted at dir.
func NewJSONRunnerStore(dir string) (*JSONRunnerStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("orchestrate: JSONRunnerStore requires a non-empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("orchestrate: JSONRunnerStore mkdir %q: %w", dir, err)
	}
	return &JSONRunnerStore{root: dir, states: make(map[string]*runnerLogState)}, nil
}

// Root returns the directory backing the store.
func (s *JSONRunnerStore) Root() string { return s.root }

func (s *JSONRunnerStore) path(id string) (string, error) {
	if err := validateRunnerID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, id+".jsonl"), nil
}

func validateRunnerID(id string) error {
	if id == "" {
		return fmt.Errorf("orchestrate: runner id must not be empty")
	}
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("orchestrate: runner id %q contains path separators", id)
	}
	if id[0] == '.' {
		return fmt.Errorf("orchestrate: runner id %q must not start with '.'", id)
	}
	return nil
}

func (s *JSONRunnerStore) getState(id string) *runnerLogState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[id]; ok {
		return st
	}
	st := &runnerLogState{}
	s.states[id] = st
	return st
}

// Append implements RunnerStore.
func (s *JSONRunnerStore) Append(_ context.Context, id string, rec *RunnerRecord) (int64, error) {
	if rec == nil {
		return 0, fmt.Errorf("orchestrate: JSONRunnerStore.Append requires a non-nil record")
	}
	p, err := s.path(id)
	if err != nil {
		return 0, err
	}

	m := s.getState(id)
	m.Lock()
	defer m.Unlock()

	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("orchestrate: JSONRunnerStore open %q: %w", p, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("orchestrate: JSONRunnerStore flock %q: %w", p, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("orchestrate: JSONRunnerStore stat %q: %w", p, err)
	}

	var lastSeq int64
	var hasInit bool
	if m.cached && info.Size() == m.size {
		lastSeq = m.lastSeq
		hasInit = m.hasInit
	} else {
		lastSeq, hasInit, err = scanLastRunnerSeq(p)
		if err != nil {
			return 0, err
		}
	}

	switch {
	case rec.Kind == RunnerRecordInit && hasInit:
		return 0, ErrRunnerLogAlreadyInitialised
	case rec.Kind != RunnerRecordInit && !hasInit:
		return 0, ErrRunnerLogNotInitialised
	}

	rec.Seq = lastSeq + 1
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return 0, fmt.Errorf("orchestrate: JSONRunnerStore encode record: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		m.cached = false
		return 0, fmt.Errorf("orchestrate: JSONRunnerStore write %q: %w", p, err)
	}
	if err := f.Sync(); err != nil {
		m.cached = false
		return 0, fmt.Errorf("orchestrate: JSONRunnerStore sync %q: %w", p, err)
	}

	info, err = f.Stat()
	if err == nil {
		m.lastSeq = rec.Seq
		m.hasInit = hasInit || rec.Kind == RunnerRecordInit
		m.size = info.Size()
		m.cached = true
	} else {
		m.cached = false
	}

	return rec.Seq, nil
}

// Load implements RunnerStore.
func (s *JSONRunnerStore) Load(_ context.Context, id string) ([]RunnerRecord, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}

	m := s.getState(id)
	m.Lock()
	defer m.Unlock()

	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrRunnerLogNotFound
		}
		return nil, fmt.Errorf("orchestrate: JSONRunnerStore read %q: %w", p, err)
	}
	defer f.Close()

	return decodeRunnerRecords(f, p)
}

// Delete implements RunnerStore.
func (s *JSONRunnerStore) Delete(_ context.Context, id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}

	m := s.getState(id)
	m.Lock()
	defer m.Unlock()
	m.cached = false

	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrRunnerLogNotFound
		}
		return fmt.Errorf("orchestrate: JSONRunnerStore delete %q: %w", p, err)
	}
	return nil
}
