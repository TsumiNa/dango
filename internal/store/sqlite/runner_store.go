package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	sqldb "github.com/tsumina/dango/internal/store/sqlite/db"
)

var _ runnerpkg.RunnerStore = (*RunnerStore)(nil)

// RunnerStore persists append-only runner checkpoint records in SQLite.
//
// RunnerStore keeps a reference to the shared [Store] and serializes writes per
// runner id so concurrent appends preserve monotonic sequence assignment.
type RunnerStore struct {
	store *Store

	mu     sync.Mutex
	states map[string]*runnerRecordState
}

type runnerRecordState struct {
	sync.Mutex
}

// NewRunnerStore returns a SQLite-backed checkpoint store for runner records.
func NewRunnerStore(store *Store) *RunnerStore {
	return &RunnerStore{store: store, states: make(map[string]*runnerRecordState)}
}

// Append implements [runner.RunnerStore].
func (s *RunnerStore) Append(ctx context.Context, runnerID string, rec *runnerpkg.RunnerRecord) (int64, error) {
	if s == nil || s.store == nil || s.store.db == nil || s.store.queries == nil {
		return 0, fmt.Errorf("sqlite: RunnerStore.Append called on nil store")
	}
	if rec == nil {
		return 0, fmt.Errorf("sqlite: RunnerStore.Append requires a non-nil record")
	}
	if err := validateSQLiteRunnerID(runnerID); err != nil {
		return 0, err
	}

	state := s.getState(runnerID)
	state.Lock()
	defer state.Unlock()

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("sqlite: begin runner record transaction for %q: %w", runnerID, err)
	}
	defer func() { _ = tx.Rollback() }()

	queries := s.store.queries.WithTx(tx)
	current, err := queries.GetRunnerRecordState(ctx, runnerID)
	if err != nil {
		return 0, fmt.Errorf("sqlite: load runner record state for %q: %w", runnerID, err)
	}
	hasInit := current.HasInit != 0
	if rec.Kind == runnerpkg.RunnerRecordInit {
		if hasInit {
			return 0, runnerpkg.ErrRunnerLogAlreadyInitialised
		}
	} else if !hasInit {
		return 0, runnerpkg.ErrRunnerLogNotInitialised
	}

	rec.Seq = current.LastSequenceNumber + 1
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return 0, fmt.Errorf("sqlite: encode runner record %q/%d: %w", runnerID, rec.Seq, err)
	}
	if err := queries.InsertRunnerRecord(ctx, sqldb.InsertRunnerRecordParams{
		RunnerID:       runnerID,
		SequenceNumber: rec.Seq,
		Kind:           string(rec.Kind),
		Timestamp:      rec.Timestamp.UTC().Format(time.RFC3339Nano),
		RecordJson:     string(raw),
	}); err != nil {
		return 0, fmt.Errorf("sqlite: insert runner record %q/%d: %w", runnerID, rec.Seq, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("sqlite: commit runner record %q/%d: %w", runnerID, rec.Seq, err)
	}
	return rec.Seq, nil
}

// Load implements [runner.RunnerStore].
func (s *RunnerStore) Load(ctx context.Context, runnerID string) ([]runnerpkg.RunnerRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil || s.store.queries == nil {
		return nil, fmt.Errorf("sqlite: RunnerStore.Load called on nil store")
	}
	if err := validateSQLiteRunnerID(runnerID); err != nil {
		return nil, err
	}

	state := s.getState(runnerID)
	state.Lock()
	defer state.Unlock()

	rows, err := s.store.queries.ListRunnerRecords(ctx, runnerID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load runner records for %q: %w", runnerID, err)
	}
	if len(rows) == 0 {
		return nil, runnerpkg.ErrRunnerLogNotFound
	}

	out := make([]runnerpkg.RunnerRecord, 0, len(rows))
	for _, row := range rows {
		var rec runnerpkg.RunnerRecord
		if err := json.Unmarshal([]byte(row.RecordJson), &rec); err != nil {
			return nil, fmt.Errorf("sqlite: decode runner record %q/%d: %w", runnerID, row.SequenceNumber, err)
		}
		if rec.Seq != row.SequenceNumber {
			return nil, fmt.Errorf(
				"sqlite: decode runner record %q/%d: stored seq %d does not match row",
				runnerID,
				row.SequenceNumber,
				rec.Seq,
			)
		}
		if string(rec.Kind) != row.Kind {
			return nil, fmt.Errorf(
				"sqlite: decode runner record %q/%d: stored kind %q does not match row %q",
				runnerID,
				row.SequenceNumber,
				rec.Kind,
				row.Kind,
			)
		}
		out = append(out, rec)
	}
	return out, nil
}

// Delete implements [runner.RunnerStore].
func (s *RunnerStore) Delete(ctx context.Context, runnerID string) error {
	if s == nil || s.store == nil || s.store.db == nil || s.store.queries == nil {
		return fmt.Errorf("sqlite: RunnerStore.Delete called on nil store")
	}
	if err := validateSQLiteRunnerID(runnerID); err != nil {
		return err
	}

	state := s.getState(runnerID)
	state.Lock()
	defer state.Unlock()

	rows, err := s.store.queries.DeleteRunnerRecords(ctx, runnerID)
	if err != nil {
		return fmt.Errorf("sqlite: delete runner records for %q: %w", runnerID, err)
	}
	if rows == 0 {
		return runnerpkg.ErrRunnerLogNotFound
	}
	return nil
}

func (s *RunnerStore) getState(runnerID string) *runnerRecordState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.states[runnerID]; ok {
		return state
	}
	state := &runnerRecordState{}
	s.states[runnerID] = state
	return state
}

func validateSQLiteRunnerID(id string) error {
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
