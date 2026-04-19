package llm

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

// ErrSessionNotFound is returned by a [SessionStore] when the requested
// session id does not exist.
var ErrSessionNotFound = errors.New("llm: session not found")

// Session is a persistent wrapper around a [Conversation]. The ID is
// chosen by the caller and is also the storage key used by
// [SessionStore] implementations. CreatedAt is set the first time a
// session is written; UpdatedAt is refreshed on every save.
type Session struct {
	ID        string        `json:"id"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Conv      *Conversation `json:"conversation"`
}

// NewSession constructs an empty [Session] with id wrapping conv. conv is
// stored by reference so later mutations flow through to the next
// [SessionStore.Save].
func NewSession(id string, conv *Conversation) *Session {
	return &Session{ID: id, Conv: conv}
}

// SessionStore persists and reloads [Session] values by id.
//
// Implementations must be safe to call from multiple goroutines when the
// same [SessionStore] is shared, but a single [Session] instance is not
// itself safe for concurrent mutation; callers typically own one session
// at a time.
type SessionStore interface {
	// Load returns the session identified by id, or [ErrSessionNotFound]
	// if no such session exists.
	Load(ctx context.Context, id string) (*Session, error)
	// Save writes sess to the store. CreatedAt is preserved on the first
	// save and UpdatedAt is refreshed to the current time.
	Save(ctx context.Context, sess *Session) error
	// Delete removes the session identified by id. Deleting an unknown
	// id must return [ErrSessionNotFound].
	Delete(ctx context.Context, id string) error
}

// JSONStore is a filesystem-backed [SessionStore] that writes each
// session as a single JSON file under a root directory. Writes go through
// a temp file + rename to avoid leaving partial files behind on crash.
//
// The zero value is not usable; construct one with [NewJSONStore].
type JSONStore struct {
	root string
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
	return &JSONStore{root: dir}, nil
}

// Root returns the directory backing the store.
func (s *JSONStore) Root() string { return s.root }

// path returns the on-disk path for id after validating the id so a
// caller cannot escape Root() via path separators or parent references.
func (s *JSONStore) path(id string) (string, error) {
	if err := validateSessionID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, id+".json"), nil
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

// Load implements [SessionStore].
func (s *JSONStore) Load(_ context.Context, id string) (*Session, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("llm: JSONStore read %q: %w", p, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("llm: JSONStore decode %q: %w", p, err)
	}
	return &sess, nil
}

// Save implements [SessionStore].
func (s *JSONStore) Save(_ context.Context, sess *Session) error {
	if sess == nil {
		return fmt.Errorf("llm: JSONStore.Save requires a non-nil session")
	}
	p, err := s.path(sess.ID)
	if err != nil {
		return err
	}
	now := time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("llm: JSONStore encode %q: %w", sess.ID, err)
	}
	tmp, err := os.CreateTemp(s.root, "."+sess.ID+".*.tmp")
	if err != nil {
		return fmt.Errorf("llm: JSONStore temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
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
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("llm: JSONStore delete %q: %w", p, err)
	}
	return nil
}
