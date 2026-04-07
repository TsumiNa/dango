package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tools (
  name         TEXT PRIMARY KEY,
  image        TEXT NOT NULL,
  config_json  TEXT NOT NULL,
  registered   DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tasks (
  id           TEXT PRIMARY KEY,
  status       TEXT NOT NULL,
  request      TEXT,
  dag_json     TEXT,
  created      DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated      DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS edges (
  id           TEXT PRIMARY KEY,
  task_id      TEXT NOT NULL REFERENCES tasks(id),
  tool_name    TEXT NOT NULL REFERENCES tools(name),
  upstream     TEXT,
  status       TEXT NOT NULL,
  shared_dir   TEXT,
  handoff_yaml TEXT,
  started      DATETIME,
  finished     DATETIME
);

CREATE TABLE IF NOT EXISTS logs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  edge_id      TEXT NOT NULL REFERENCES edges(id),
  level        TEXT NOT NULL,
  message      TEXT NOT NULL,
  timestamp    DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

type Store struct {
	db *sql.DB
}

type ToolRecord struct {
	Name       string `json:"name"`
	Image      string `json:"image"`
	ConfigJSON string `json:"config_json"`
	Registered string `json:"registered"`
}

type TaskRecord struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Request string `json:"request"`
	DAGJSON string `json:"dag_json"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

type EdgeRecord struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	ToolName    string `json:"tool_name"`
	Upstream    string `json:"upstream"`
	Status      string `json:"status"`
	SharedDir   string `json:"shared_dir"`
	HandoffYAML string `json:"handoff_yaml"`
	Started     string `json:"started"`
	Finished    string `json:"finished"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func (s *Store) UpsertTool(ctx context.Context, record ToolRecord) error {
	const query = `
INSERT INTO tools (name, image, config_json)
VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
  image = excluded.image,
  config_json = excluded.config_json,
  registered = CURRENT_TIMESTAMP
`

	if _, err := s.db.ExecContext(ctx, query, record.Name, record.Image, record.ConfigJSON); err != nil {
		return fmt.Errorf("upsert tool %q: %w", record.Name, err)
	}

	return nil
}

func (s *Store) DeleteTool(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM tools WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete tool %q: %w", name, err)
	}

	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (s *Store) GetTool(ctx context.Context, name string) (ToolRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT name, image, config_json, registered FROM tools WHERE name = ?`, name)

	var record ToolRecord
	if err := row.Scan(&record.Name, &record.Image, &record.ConfigJSON, &record.Registered); err != nil {
		return ToolRecord{}, fmt.Errorf("get tool %q: %w", name, err)
	}

	return record, nil
}

func (s *Store) ListTools(ctx context.Context) ([]ToolRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, image, config_json, registered FROM tools ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	defer rows.Close()

	var out []ToolRecord
	for rows.Next() {
		var record ToolRecord
		if err := rows.Scan(&record.Name, &record.Image, &record.ConfigJSON, &record.Registered); err != nil {
			return nil, fmt.Errorf("scan tool: %w", err)
		}
		out = append(out, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tools: %w", err)
	}

	return out, nil
}

func (s *Store) CreateTask(ctx context.Context, record TaskRecord) error {
	const query = `
INSERT INTO tasks (id, status, request, dag_json)
VALUES (?, ?, ?, ?)
`

	if _, err := s.db.ExecContext(ctx, query, record.ID, record.Status, record.Request, record.DAGJSON); err != nil {
		return fmt.Errorf("create task %q: %w", record.ID, err)
	}

	return nil
}

func (s *Store) GetTask(ctx context.Context, id string) (TaskRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, status, request, dag_json, created, updated FROM tasks WHERE id = ?`, id)

	var record TaskRecord
	if err := row.Scan(&record.ID, &record.Status, &record.Request, &record.DAGJSON, &record.Created, &record.Updated); err != nil {
		return TaskRecord{}, fmt.Errorf("get task %q: %w", id, err)
	}

	return record, nil
}

func (s *Store) UpdateTaskStatus(ctx context.Context, id string, status string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ?, updated = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("update task %q status: %w", id, err)
	}

	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (s *Store) UpdateTaskPlan(ctx context.Context, id, status, dagJSON string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE tasks SET status = ?, dag_json = ?, updated = CURRENT_TIMESTAMP WHERE id = ?`,
		status,
		nullable(dagJSON),
		id,
	)
	if err != nil {
		return fmt.Errorf("update task %q plan: %w", id, err)
	}

	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (s *Store) UpsertEdge(ctx context.Context, record EdgeRecord) error {
	const query = `
INSERT INTO edges (id, task_id, tool_name, upstream, status, shared_dir, handoff_yaml, started, finished)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  task_id = excluded.task_id,
  tool_name = excluded.tool_name,
  upstream = excluded.upstream,
  status = excluded.status,
  shared_dir = excluded.shared_dir,
  handoff_yaml = excluded.handoff_yaml,
  started = excluded.started,
  finished = excluded.finished
`

	if _, err := s.db.ExecContext(
		ctx,
		query,
		record.ID,
		record.TaskID,
		record.ToolName,
		nullable(record.Upstream),
		record.Status,
		nullable(record.SharedDir),
		nullable(record.HandoffYAML),
		nullable(record.Started),
		nullable(record.Finished),
	); err != nil {
		return fmt.Errorf("upsert edge %q: %w", record.ID, err)
	}

	return nil
}

func (s *Store) UpdateEdgeResult(ctx context.Context, edgeID, status, handoffYAML string, finished time.Time) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE edges SET status = ?, handoff_yaml = ?, finished = ? WHERE id = ?`,
		status,
		nullable(handoffYAML),
		finished.Format(time.RFC3339),
		edgeID,
	)
	if err != nil {
		return fmt.Errorf("update edge %q result: %w", edgeID, err)
	}

	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (s *Store) InsertLog(ctx context.Context, edgeID, level, message string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO logs (edge_id, level, message) VALUES (?, ?, ?)`, edgeID, level, message); err != nil {
		return fmt.Errorf("insert log for edge %q: %w", edgeID, err)
	}
	return nil
}

func (s *Store) IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
