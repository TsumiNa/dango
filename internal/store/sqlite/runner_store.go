package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	sqldb "github.com/tsumina/dango/internal/store/sqlite/db"
)

const runnerStoreLockRetryDelay = 10 * time.Millisecond

var _ runnerpkg.RunnerStore = (*RunnerStore)(nil)

// RunnerStore persists append-only runner checkpoint records in SQLite.
//
// RunnerStore keeps a reference to the shared [Store]. Append acquires a
// SQLite immediate transaction before reading or writing sequence state so
// concurrent writers preserve monotonic sequence assignment across store
// instances that share the same database.
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

	conn, queries, err := s.beginImmediateRunnerRecordTx(ctx, runnerID)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = rollbackImmediate(ctx, conn)
	}()

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
	if err := commitImmediate(ctx, conn); err != nil {
		return 0, fmt.Errorf("sqlite: commit runner record %q/%d: %w", runnerID, rec.Seq, err)
	}
	committed = true
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

func (s *RunnerStore) beginImmediateRunnerRecordTx(ctx context.Context, runnerID string) (*sql.Conn, *sqldb.Queries, error) {
	conn, err := s.store.db.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite: open runner record connection for %q: %w", runnerID, err)
	}
	if err := beginImmediate(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("sqlite: begin immediate runner record transaction for %q: %w", runnerID, err)
	}
	return conn, sqldb.New(conn), nil
}

func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	for {
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
			return nil
		} else if !isSQLiteLockError(err) {
			return err
		}

		timer := time.NewTimer(runnerStoreLockRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func commitImmediate(ctx context.Context, conn *sql.Conn) error {
	for {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err == nil {
			return nil
		} else if !isSQLiteLockError(err) {
			return err
		}

		timer := time.NewTimer(runnerStoreLockRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func rollbackImmediate(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "ROLLBACK")
	return err
}

func isSQLiteLockError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy") || strings.Contains(message, "sqlite_locked")
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
