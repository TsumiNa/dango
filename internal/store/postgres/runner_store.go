package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
)

var _ runnerpkg.RunnerStore = (*RunnerStore)(nil)

// RunnerStore persists append-only runner checkpoint records in Postgres.
//
// Append acquires a transaction-scoped advisory lock keyed by runner id so
// concurrent writers preserve monotonic sequence assignment across processes.
type RunnerStore struct {
	store *Store
}

// NewRunnerStore returns a Postgres-backed checkpoint store for runner records.
func NewRunnerStore(store *Store) *RunnerStore {
	return &RunnerStore{store: store}
}

// Append implements runner.RunnerStore.
func (s *RunnerStore) Append(ctx context.Context, runnerID string, rec *runnerpkg.RunnerRecord) (int64, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return 0, fmt.Errorf("postgres: RunnerStore.Append called on nil store")
	}
	if rec == nil {
		return 0, fmt.Errorf("postgres: RunnerStore.Append requires a non-nil record")
	}
	if err := validatePostgresRunnerID(runnerID); err != nil {
		return 0, err
	}

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("postgres: begin runner record transaction for %q: %w", runnerID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, runnerAdvisoryKey(runnerID)); err != nil {
		return 0, fmt.Errorf("postgres: lock runner record transaction for %q: %w", runnerID, err)
	}

	var lastSequence int64
	var hasInit int64
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COALESCE(MAX(sequence_number), 0) AS last_sequence,
			COALESCE(MAX(CASE WHEN kind = 'init' THEN 1 ELSE 0 END), 0) AS has_init
		FROM runner_records
		WHERE runner_id = $1
	`, runnerID).Scan(&lastSequence, &hasInit); err != nil {
		return 0, fmt.Errorf("postgres: load runner record state for %q: %w", runnerID, err)
	}

	if rec.Kind == runnerpkg.RunnerRecordInit {
		if hasInit != 0 {
			return 0, runnerpkg.ErrRunnerLogAlreadyInitialised
		}
	} else if hasInit == 0 {
		return 0, runnerpkg.ErrRunnerLogNotInitialised
	}

	rec.Seq = lastSequence + 1
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return 0, fmt.Errorf("postgres: encode runner record %q/%d: %w", runnerID, rec.Seq, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runner_records (runner_id, sequence_number, kind, timestamp, record_json)
		VALUES ($1, $2, $3, $4, $5::jsonb)
	`, runnerID, rec.Seq, string(rec.Kind), rec.Timestamp.UTC(), string(raw)); err != nil {
		return 0, fmt.Errorf("postgres: insert runner record %q/%d: %w", runnerID, rec.Seq, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("postgres: commit runner record %q/%d: %w", runnerID, rec.Seq, err)
	}
	committed = true
	return rec.Seq, nil
}

// Load implements runner.RunnerStore.
func (s *RunnerStore) Load(ctx context.Context, runnerID string) ([]runnerpkg.RunnerRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, fmt.Errorf("postgres: RunnerStore.Load called on nil store")
	}
	if err := validatePostgresRunnerID(runnerID); err != nil {
		return nil, err
	}

	rows, err := s.store.db.QueryContext(ctx, `
		SELECT sequence_number, kind, record_json::text
		FROM runner_records
		WHERE runner_id = $1
		ORDER BY sequence_number ASC
	`, runnerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: load runner records for %q: %w", runnerID, err)
	}
	defer rows.Close()

	out := make([]runnerpkg.RunnerRecord, 0)
	for rows.Next() {
		var seq int64
		var kind string
		var raw string
		if err := rows.Scan(&seq, &kind, &raw); err != nil {
			return nil, fmt.Errorf("postgres: scan runner record row %q: %w", runnerID, err)
		}
		var rec runnerpkg.RunnerRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, fmt.Errorf("postgres: decode runner record %q/%d: %w", runnerID, seq, err)
		}
		if rec.Seq != seq {
			return nil, fmt.Errorf("postgres: decode runner record %q/%d: stored seq %d does not match row", runnerID, seq, rec.Seq)
		}
		if string(rec.Kind) != kind {
			return nil, fmt.Errorf("postgres: decode runner record %q/%d: stored kind %q does not match row %q", runnerID, seq, rec.Kind, kind)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate runner records for %q: %w", runnerID, err)
	}
	if len(out) == 0 {
		return nil, runnerpkg.ErrRunnerLogNotFound
	}
	return out, nil
}

// Delete implements runner.RunnerStore.
func (s *RunnerStore) Delete(ctx context.Context, runnerID string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return fmt.Errorf("postgres: RunnerStore.Delete called on nil store")
	}
	if err := validatePostgresRunnerID(runnerID); err != nil {
		return err
	}
	result, err := s.store.db.ExecContext(ctx, `DELETE FROM runner_records WHERE runner_id = $1`, runnerID)
	if err != nil {
		return fmt.Errorf("postgres: delete runner records for %q: %w", runnerID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres: rows affected while deleting runner records for %q: %w", runnerID, err)
	}
	if rows == 0 {
		return runnerpkg.ErrRunnerLogNotFound
	}
	return nil
}

func runnerAdvisoryKey(runnerID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(runnerID))
	return int64(h.Sum64())
}

func validatePostgresRunnerID(id string) error {
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
