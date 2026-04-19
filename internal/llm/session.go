package llm

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

// ErrSessionNotFound is returned by [SessionStore.Load] and
// [SessionStore.Delete] when the requested session id has no log on the
// store.
var ErrSessionNotFound = errors.New("llm: session not found")

// ErrSessionNotInitialised is returned by [SessionStore.Append] when the
// first event written to a session is not [EventInit]. Every session log
// must be anchored by an init event so replay knows the session's
// instructions and tool schema.
var ErrSessionNotInitialised = errors.New("llm: session log must start with an init event")

// ErrSessionAlreadyInitialised is returned by [SessionStore.Append] when
// an [EventInit] is appended to a session that already has events on the
// store. EventInit must occur exactly once per session.
var ErrSessionAlreadyInitialised = errors.New("llm: session already initialised")

// SessionStore is an append-only persistence layer for [Conversation]
// state. Each mutation a conversation performs is recorded as an
// [Event]; replay of the event sequence reconstructs the conversation
// deterministically. The append-only shape gives every conversation a
// step-by-step history that can be rolled back via
// [SessionStore.Truncate] and reused as training data.
//
// Implementations must be safe for concurrent use: multiple goroutines
// may share a SessionStore, but the events for a single sessionID are
// serialised by the store so Seq values stay monotonic without gaps.
type SessionStore interface {
	// Append writes ev to the log for sessionID, assigning ev.Seq
	// from the store's monotonic counter (starting at 1) and
	// stamping ev.Timestamp with the current time if it is the
	// zero value. It returns the assigned Seq.
	//
	// The first event written to a fresh session must have Kind
	// [EventInit]; otherwise [ErrSessionNotInitialised] is
	// returned. Appending a second [EventInit] to the same session
	// returns [ErrSessionAlreadyInitialised].
	Append(ctx context.Context, sessionID string, ev *Event) (int64, error)

	// Load returns all events recorded for sessionID, in Seq order.
	// Returns [ErrSessionNotFound] when the session has no log.
	Load(ctx context.Context, sessionID string) ([]Event, error)

	// Truncate removes every event with Seq > toSeq from
	// sessionID's log. toSeq <= 0 leaves only the initial
	// [EventInit] anchor (preserving the session shell but clearing
	// its turns). Truncating a missing session returns
	// [ErrSessionNotFound].
	Truncate(ctx context.Context, sessionID string, toSeq int64) error

	// Delete removes sessionID's log entirely. Deleting a missing
	// session returns [ErrSessionNotFound].
	Delete(ctx context.Context, sessionID string) error
}

// JSONStore is a filesystem-backed [SessionStore] that writes each
// session as a single JSON Lines file under a root directory. Append
// uses an exclusive file lock (flock) to serialise writers, so the
// store is safe for use from multiple goroutines (and processes) on
// the same root.
//
// On crash the trailing partially-written line is tolerated:
// [JSONStore.Load] returns every fully-written prefix event without
// error; the crash-debris line stays on disk but does not surface to
// callers and is rewritten away on the next [JSONStore.Truncate].
//
// The zero value is not usable; construct one with [NewJSONStore].
type JSONStore struct {
	root string

	// mu guards states. Each session id gets its own sync.Mutex so
	// independent sessions do not block each other on Append while
	// writes within a single session stay ordered.
	mu     sync.Mutex
	states map[string]*sessionState
}

type sessionState struct {
	sync.Mutex
	lastSeq int64
	hasInit bool
	size    int64
	cached  bool
}

// NewJSONStore returns a [JSONStore] rooted at dir. The directory is
// created with 0o755 if it does not exist.
func NewJSONStore(dir string) (*JSONStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("llm: JSONStore requires a non-empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("llm: JSONStore mkdir %q: %w", dir, err)
	}
	return &JSONStore{root: dir, states: make(map[string]*sessionState)}, nil
}

// Root returns the directory backing the store.
func (s *JSONStore) Root() string { return s.root }

// path returns the on-disk path for id after validating the id so a
// caller cannot escape Root() via path separators or parent references.
func (s *JSONStore) path(id string) (string, error) {
	if err := validateSessionID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, id+".jsonl"), nil
}

// validateSessionID rejects ids that would produce unsafe filenames:
// empty strings, ids containing path separators or ".." segments, and
// ids beginning with a dot (hidden files).
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("llm: session id must not be empty")
	}
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return fmt.Errorf("llm: session id %q contains path separators", id)
	}
	if id[0] == '.' {
		return fmt.Errorf("llm: session id %q must not start with '.'", id)
	}
	return nil
}

// getState returns the per-id state, lazily creating it.
func (s *JSONStore) getState(id string) *sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[id]; ok {
		return st
	}
	st := &sessionState{}
	s.states[id] = st
	return st
}

// Append implements [SessionStore]. It opens the session file with
// O_APPEND, takes an exclusive flock to coordinate with concurrent
// writers across processes, scans the existing tail to compute the
// next Seq, and writes a single JSON line followed by a newline.
func (s *JSONStore) Append(_ context.Context, id string, ev *Event) (int64, error) {
	if ev == nil {
		return 0, fmt.Errorf("llm: JSONStore.Append requires a non-nil event")
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
		return 0, fmt.Errorf("llm: JSONStore open %q: %w", p, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("llm: JSONStore flock %q: %w", p, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("llm: JSONStore stat %q: %w", p, err)
	}

	var lastSeq int64
	var hasInit bool

	if m.cached && info.Size() == m.size {
		lastSeq = m.lastSeq
		hasInit = m.hasInit
	} else {
		lastSeq, hasInit, err = scanLastSeq(p)
		if err != nil {
			return 0, err
		}
	}

	switch {
	case ev.Kind == EventInit && hasInit:
		return 0, ErrSessionAlreadyInitialised
	case ev.Kind != EventInit && !hasInit:
		return 0, ErrSessionNotInitialised
	}

	ev.Seq = lastSeq + 1
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return 0, fmt.Errorf("llm: JSONStore encode event: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		m.cached = false
		return 0, fmt.Errorf("llm: JSONStore write %q: %w", p, err)
	}
	if err := f.Sync(); err != nil {
		m.cached = false
		return 0, fmt.Errorf("llm: JSONStore sync %q: %w", p, err)
	}

	info, err = f.Stat()
	if err == nil {
		m.lastSeq = ev.Seq
		m.hasInit = hasInit || (ev.Kind == EventInit)
		m.size = info.Size()
		m.cached = true
	} else {
		m.cached = false
	}

	return ev.Seq, nil
}

// Load implements [SessionStore].
func (s *JSONStore) Load(_ context.Context, id string) ([]Event, error) {
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
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("llm: JSONStore read %q: %w", p, err)
	}
	defer f.Close()

	return decodeEvents(f, p)
}

// Truncate implements [SessionStore].
func (s *JSONStore) Truncate(_ context.Context, id string, toSeq int64) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}

	m := s.getState(id)
	m.Lock()
	defer m.Unlock()

	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("llm: JSONStore open %q: %w", p, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("llm: JSONStore flock %q: %w", p, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("llm: JSONStore seek %q: %w", p, err)
	}
	events, err := decodeEvents(f, p)
	if err != nil {
		return err
	}

	m.cached = false

	var buf bytes.Buffer
	for _, ev := range events {
		if toSeq <= 0 {
			if ev.Kind != EventInit {
				continue
			}
		} else if ev.Seq > toSeq {
			continue
		}
		line, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("llm: JSONStore encode event: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	tmp, err := os.CreateTemp(s.root, "."+id+".*.tmp")
	if err != nil {
		return fmt.Errorf("llm: JSONStore temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("llm: JSONStore write %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("llm: JSONStore close %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("llm: JSONStore rename %q: %w", p, err)
	}
	return nil
}

// Delete implements [SessionStore].
func (s *JSONStore) Delete(_ context.Context, id string) error {
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
			return ErrSessionNotFound
		}
		return fmt.Errorf("llm: JSONStore delete %q: %w", p, err)
	}
	return nil
}

// scanLastSeq reads p and returns the largest Seq among fully-decoded
// events plus whether an EventInit was observed. A non-existent file
// is reported as (0, false, nil) so a fresh session can be
// initialised. A trailing partial line (crash recovery) is silently
// ignored.
func scanLastSeq(p string) (int64, bool, error) {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("llm: JSONStore read %q: %w", p, err)
	}
	defer f.Close()
	events, err := decodeEvents(f, p)
	if err != nil {
		return 0, false, err
	}
	var last int64
	hasInit := false
	for _, ev := range events {
		if ev.Seq > last {
			last = ev.Seq
		}
		if ev.Kind == EventInit {
			hasInit = true
		}
	}
	return last, hasInit, nil
}

// decodeEvents reads JSON-Lines events from r. A trailing line that
// fails to unmarshal is treated as crash debris: every prior
// successfully-decoded event is returned with no error. Any decode
// failure on a non-final line is fatal because it indicates real
// corruption the store cannot safely recover from.
func decodeEvents(r io.Reader, p string) ([]Event, error) {
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
		// A previously-pending event becomes finalized once we know
		// another non-empty line follows it.
		if pendOK {
			out = append(out, pending)
		} else if pendLine > 0 {
			// The previous pending line failed to decode but a
			// later line exists; that is real corruption, not
			// crash debris.
			return nil, fmt.Errorf("llm: JSONStore decode %q: corrupt event at line %d", p, pendLine)
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
		return nil, fmt.Errorf("llm: JSONStore scan %q: %w", p, err)
	}
	if pendOK {
		out = append(out, pending)
	}
	return out, nil
}
