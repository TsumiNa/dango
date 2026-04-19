package orchestrate

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
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrRuntimeLogNotFound is returned when the requested runtime id has no log.
var ErrRuntimeLogNotFound = errors.New("orchestrate: runtime log not found")

// ErrRuntimeLogNotInitialised is returned when a runtime log is appended
// before it is anchored by a RuntimeRecordInit record.
var ErrRuntimeLogNotInitialised = errors.New("orchestrate: runtime log must start with an init record")

// ErrRuntimeLogAlreadyInitialised is returned when an init record is appended
// to a runtime log that already has one.
var ErrRuntimeLogAlreadyInitialised = errors.New("orchestrate: runtime log already initialised")

// RuntimeStore is an append-only persistence layer for Runtime lifecycle data.
//
// Implementations must be safe for concurrent use. Records for a single
// runtimeID are serialised so Seq values stay monotonic without gaps.
type RuntimeStore interface {
	// Append writes rec to the log for runtimeID, assigning rec.Seq from the
	// store's monotonic counter (starting at 1) and stamping rec.Timestamp when
	// it is zero.
	//
	// The first record written to a fresh runtime log must have Kind
	// RuntimeRecordInit; otherwise ErrRuntimeLogNotInitialised is returned.
	Append(ctx context.Context, runtimeID string, rec *RuntimeRecord) (int64, error)

	// Load returns every fully written record for runtimeID in Seq order.
	Load(ctx context.Context, runtimeID string) ([]RuntimeRecord, error)

	// Delete removes runtimeID's log entirely.
	Delete(ctx context.Context, runtimeID string) error
}

// RuntimeRecordKind tags the kind of append-only runtime record stored on disk.
type RuntimeRecordKind string

const (
	RuntimeRecordInit   RuntimeRecordKind = "init"
	RuntimeRecordStatus RuntimeRecordKind = "status"
	RuntimeRecordEvent  RuntimeRecordKind = "event"
)

// RuntimeRecord is one append-only record in a persisted runtime log.
type RuntimeRecord struct {
	Seq       int64             `json:"seq"`
	Kind      RuntimeRecordKind `json:"kind"`
	Timestamp time.Time         `json:"ts"`

	Status RuntimeStatus       `json:"status,omitempty"`
	Error  string              `json:"error,omitempty"`
	Event  *StoredRuntimeEvent `json:"event,omitempty"`
}

// StoredRuntimeEvent is the durable representation of a RuntimeEvent.
//
// Data is stored either as raw JSON when the value is JSON-encodable or as
// plain text when the original payload was an error or could not be encoded.
type StoredRuntimeEvent struct {
	Type         string          `json:"type"`
	NodeID       string          `json:"node_id,omitempty"`
	DataEncoding string          `json:"data_encoding,omitempty"`
	DataJSON     json.RawMessage `json:"data_json,omitempty"`
	DataText     string          `json:"data_text,omitempty"`
}

func newStoredRuntimeEvent(event RuntimeEvent) *StoredRuntimeEvent {
	stored := &StoredRuntimeEvent{Type: event.Type.String(), NodeID: event.NodeID}
	if event.Data == nil {
		return stored
	}
	if errValue, ok := event.Data.(error); ok {
		stored.DataEncoding = "text"
		stored.DataText = errValue.Error()
		return stored
	}
	raw, err := json.Marshal(event.Data)
	if err != nil {
		stored.DataEncoding = "text"
		stored.DataText = fmt.Sprintf("%v", event.Data)
		return stored
	}
	stored.DataEncoding = "json"
	stored.DataJSON = raw
	return stored
}

// JSONRuntimeStore is a filesystem-backed RuntimeStore that writes one JSONL
// file per runtime under a root directory.
//
// Append uses flock to serialise writers across goroutines and processes. A
// trailing partial line is tolerated by Load and ignored until the next clean
// append rewrites the sequence cache.
type JSONRuntimeStore struct {
	root string

	mu     sync.Mutex
	states map[string]*runtimeLogState
}

type runtimeLogState struct {
	sync.Mutex
	lastSeq int64
	hasInit bool
	size    int64
	cached  bool
}

// NewJSONRuntimeStore returns a JSONRuntimeStore rooted at dir.
func NewJSONRuntimeStore(dir string) (*JSONRuntimeStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("orchestrate: JSONRuntimeStore requires a non-empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("orchestrate: JSONRuntimeStore mkdir %q: %w", dir, err)
	}
	return &JSONRuntimeStore{root: dir, states: make(map[string]*runtimeLogState)}, nil
}

// Root returns the directory backing the store.
func (s *JSONRuntimeStore) Root() string { return s.root }

func (s *JSONRuntimeStore) path(id string) (string, error) {
	if err := validateRuntimeID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, id+".jsonl"), nil
}

func validateRuntimeID(id string) error {
	if id == "" {
		return fmt.Errorf("orchestrate: runtime id must not be empty")
	}
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("orchestrate: runtime id %q contains path separators", id)
	}
	if id[0] == '.' {
		return fmt.Errorf("orchestrate: runtime id %q must not start with '.'", id)
	}
	return nil
}

func (s *JSONRuntimeStore) getState(id string) *runtimeLogState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[id]; ok {
		return st
	}
	st := &runtimeLogState{}
	s.states[id] = st
	return st
}

// Append implements RuntimeStore.
func (s *JSONRuntimeStore) Append(_ context.Context, id string, rec *RuntimeRecord) (int64, error) {
	if rec == nil {
		return 0, fmt.Errorf("orchestrate: JSONRuntimeStore.Append requires a non-nil record")
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
		return 0, fmt.Errorf("orchestrate: JSONRuntimeStore open %q: %w", p, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("orchestrate: JSONRuntimeStore flock %q: %w", p, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("orchestrate: JSONRuntimeStore stat %q: %w", p, err)
	}

	var lastSeq int64
	var hasInit bool
	if m.cached && info.Size() == m.size {
		lastSeq = m.lastSeq
		hasInit = m.hasInit
	} else {
		lastSeq, hasInit, err = scanLastRuntimeSeq(p)
		if err != nil {
			return 0, err
		}
	}

	switch {
	case rec.Kind == RuntimeRecordInit && hasInit:
		return 0, ErrRuntimeLogAlreadyInitialised
	case rec.Kind != RuntimeRecordInit && !hasInit:
		return 0, ErrRuntimeLogNotInitialised
	}

	rec.Seq = lastSeq + 1
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return 0, fmt.Errorf("orchestrate: JSONRuntimeStore encode record: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		m.cached = false
		return 0, fmt.Errorf("orchestrate: JSONRuntimeStore write %q: %w", p, err)
	}
	if err := f.Sync(); err != nil {
		m.cached = false
		return 0, fmt.Errorf("orchestrate: JSONRuntimeStore sync %q: %w", p, err)
	}

	info, err = f.Stat()
	if err == nil {
		m.lastSeq = rec.Seq
		m.hasInit = hasInit || rec.Kind == RuntimeRecordInit
		m.size = info.Size()
		m.cached = true
	} else {
		m.cached = false
	}

	return rec.Seq, nil
}

// Load implements RuntimeStore.
func (s *JSONRuntimeStore) Load(_ context.Context, id string) ([]RuntimeRecord, error) {
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
			return nil, ErrRuntimeLogNotFound
		}
		return nil, fmt.Errorf("orchestrate: JSONRuntimeStore read %q: %w", p, err)
	}
	defer f.Close()

	return decodeRuntimeRecords(f, p)
}

// Delete implements RuntimeStore.
func (s *JSONRuntimeStore) Delete(_ context.Context, id string) error {
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
			return ErrRuntimeLogNotFound
		}
		return fmt.Errorf("orchestrate: JSONRuntimeStore delete %q: %w", p, err)
	}
	return nil
}

func scanLastRuntimeSeq(p string) (int64, bool, error) {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("orchestrate: JSONRuntimeStore read %q: %w", p, err)
	}
	defer f.Close()

	records, err := decodeRuntimeRecords(f, p)
	if err != nil {
		return 0, false, err
	}
	var last int64
	hasInit := false
	for _, rec := range records {
		if rec.Seq > last {
			last = rec.Seq
		}
		if rec.Kind == RuntimeRecordInit {
			hasInit = true
		}
	}
	return last, hasInit, nil
}

func decodeRuntimeRecords(r io.Reader, p string) ([]RuntimeRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var (
		out      []RuntimeRecord
		pending  RuntimeRecord
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
			return nil, fmt.Errorf("orchestrate: JSONRuntimeStore decode %q: corrupt record at line %d", p, pendLine)
		}
		pending = RuntimeRecord{}
		if err := json.Unmarshal(line, &pending); err != nil {
			pendOK = false
			pendLine = lineNo
			continue
		}
		pendOK = true
		pendLine = lineNo
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("orchestrate: JSONRuntimeStore scan %q: %w", p, err)
	}
	if pendOK {
		out = append(out, pending)
	}
	return out, nil
}
