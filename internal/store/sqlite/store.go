package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	sqldb "github.com/tsumina/dango/internal/store/sqlite/db"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite-backed persistence layer used by the orchestrator.
//
// Store values are safe to share across goroutines because the underlying
// sql.DB manages concurrent access.
type Store struct {
	db      *sql.DB
	queries *sqldb.Queries
}

// ToolRecord mirrors one row in the tools table.
type ToolRecord struct {
	// Name is the unique tool name from the merged tool specification.
	Name string `json:"name"`
	// Image is the registered image or runtime reference used to invoke the tool.
	Image string `json:"image"`
	// ConfigJSON stores the merged tool configuration as JSON.
	ConfigJSON string `json:"config_json"`
	// Registered is the timestamp recorded by SQLite for the latest registration.
	Registered string `json:"registered"`
}

// TaskRecord mirrors one row in the tasks table.
type TaskRecord struct {
	// ID is the task UUID.
	ID string `json:"id"`
	// Status is the persisted task lifecycle state.
	Status string `json:"status"`
	// Request is the original user request captured for the task.
	Request string `json:"request"`
	// DAGJSON stores the serialized plan when one has been applied.
	DAGJSON string `json:"dag_json"`
	// Created is the SQLite timestamp when the task row was inserted.
	Created string `json:"created"`
	// Updated is the SQLite timestamp for the last task mutation.
	Updated string `json:"updated"`
}

// EdgeRecord mirrors one row in the edges table.
type EdgeRecord struct {
	// ID is the edge UUID.
	ID string `json:"id"`
	// TaskID identifies the parent task.
	TaskID string `json:"task_id"`
	// ToolName identifies the tool assigned to the edge.
	ToolName string `json:"tool_name"`
	// Upstream points at the producing edge when this edge consumes prior output.
	Upstream string `json:"upstream"`
	// Status is the persisted edge lifecycle state.
	Status string `json:"status"`
	// SharedDir stores the host path used for output handoff files.
	SharedDir string `json:"shared_dir"`
	// HandoffYAML stores the parsed _handoff.md frontmatter for machine use.
	HandoffYAML string `json:"handoff_yaml"`
	// Started records the execution start timestamp when known.
	Started string `json:"started"`
	// Finished records the execution finish timestamp when known.
	Finished string `json:"finished"`
}

// Open opens or creates the SQLite database at path and applies schema
// migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := applyMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{
		db:      db,
		queries: sqldb.New(db),
	}, nil
}

// Close closes the underlying SQLite connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// UpsertTool inserts or replaces the stored metadata for one registered tool.
func (s *Store) UpsertTool(ctx context.Context, record ToolRecord) error {
	if err := s.queries.UpsertTool(ctx, sqldb.UpsertToolParams{
		Name:       record.Name,
		Image:      record.Image,
		ConfigJson: record.ConfigJSON,
	}); err != nil {
		return fmt.Errorf("upsert tool %q: %w", record.Name, err)
	}

	return nil
}

// DeleteTool removes the named tool row.
//
// DeleteTool returns sql.ErrNoRows when the tool does not exist.
func (s *Store) DeleteTool(ctx context.Context, name string) error {
	rows, err := s.queries.DeleteTool(ctx, name)
	if err != nil {
		return fmt.Errorf("delete tool %q: %w", name, err)
	}

	if rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// GetTool returns the stored row for one tool.
//
// GetTool returns sql.ErrNoRows when no row matches name.
func (s *Store) GetTool(ctx context.Context, name string) (ToolRecord, error) {
	row, err := s.queries.GetTool(ctx, name)
	if err != nil {
		return ToolRecord{}, fmt.Errorf("get tool %q: %w", name, err)
	}

	return toolRecordFromRow(row), nil
}

// ListTools returns all registered tools ordered by name.
func (s *Store) ListTools(ctx context.Context) ([]ToolRecord, error) {
	rows, err := s.queries.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	out := make([]ToolRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, toolRecordFromRow(row))
	}

	return out, nil
}

// CreateTask inserts a new task row.
func (s *Store) CreateTask(ctx context.Context, record TaskRecord) error {
	if err := s.queries.CreateTask(ctx, sqldb.CreateTaskParams{
		ID:      record.ID,
		Status:  record.Status,
		Request: nullableString(record.Request),
		DagJson: nullableString(record.DAGJSON),
	}); err != nil {
		return fmt.Errorf("create task %q: %w", record.ID, err)
	}

	return nil
}

// GetTask returns the stored row for one task.
//
// GetTask returns sql.ErrNoRows when no row matches id.
func (s *Store) GetTask(ctx context.Context, id string) (TaskRecord, error) {
	row, err := s.queries.GetTask(ctx, id)
	if err != nil {
		return TaskRecord{}, fmt.Errorf("get task %q: %w", id, err)
	}

	return taskRecordFromRow(row), nil
}

// UpdateTaskStatus updates only the task lifecycle state.
//
// UpdateTaskStatus returns sql.ErrNoRows when no task matches id.
func (s *Store) UpdateTaskStatus(ctx context.Context, id string, status string) error {
	rows, err := s.queries.UpdateTaskStatus(ctx, sqldb.UpdateTaskStatusParams{
		Status: status,
		ID:     id,
	})
	if err != nil {
		return fmt.Errorf("update task %q status: %w", id, err)
	}

	if rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// UpdateTaskPlan persists both task status and serialized plan content.
//
// UpdateTaskPlan returns sql.ErrNoRows when no task matches id.
func (s *Store) UpdateTaskPlan(ctx context.Context, id, status, dagJSON string) error {
	rows, err := s.queries.UpdateTaskPlan(ctx, sqldb.UpdateTaskPlanParams{
		Status:  status,
		DagJson: nullableString(dagJSON),
		ID:      id,
	})
	if err != nil {
		return fmt.Errorf("update task %q plan: %w", id, err)
	}

	if rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// UpsertEdge inserts or replaces the stored state for one edge.
func (s *Store) UpsertEdge(ctx context.Context, record EdgeRecord) error {
	if err := s.queries.UpsertEdge(ctx, sqldb.UpsertEdgeParams{
		ID:          record.ID,
		TaskID:      record.TaskID,
		ToolName:    record.ToolName,
		Upstream:    nullableString(record.Upstream),
		Status:      record.Status,
		SharedDir:   nullableString(record.SharedDir),
		HandoffYaml: nullableString(record.HandoffYAML),
		Started:     nullableString(record.Started),
		Finished:    nullableString(record.Finished),
	}); err != nil {
		return fmt.Errorf("upsert edge %q: %w", record.ID, err)
	}

	return nil
}

// UpdateEdgeResult updates the result metadata for one edge.
//
// UpdateEdgeResult returns sql.ErrNoRows when no edge matches edgeID.
func (s *Store) UpdateEdgeResult(ctx context.Context, edgeID, status, handoffYAML string, finished time.Time) error {
	rows, err := s.queries.UpdateEdgeResult(ctx, sqldb.UpdateEdgeResultParams{
		Status:      status,
		HandoffYaml: nullableString(handoffYAML),
		Finished:    nullableString(finished.Format(time.RFC3339)),
		EdgeID:      edgeID,
	})
	if err != nil {
		return fmt.Errorf("update edge %q result: %w", edgeID, err)
	}

	if rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// InsertLog appends one log row for an edge execution.
func (s *Store) InsertLog(ctx context.Context, edgeID, level, message string) error {
	if err := s.queries.InsertLog(ctx, sqldb.InsertLogParams{
		EdgeID:  edgeID,
		Level:   level,
		Message: message,
	}); err != nil {
		return fmt.Errorf("insert log for edge %q: %w", edgeID, err)
	}
	return nil
}

// IsNotFound reports whether err maps to sql.ErrNoRows.
func (s *Store) IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func toolRecordFromRow(row sqldb.Tool) ToolRecord {
	return ToolRecord{
		Name:       row.Name,
		Image:      row.Image,
		ConfigJSON: row.ConfigJson,
		Registered: row.Registered,
	}
}

func taskRecordFromRow(row sqldb.Task) TaskRecord {
	return TaskRecord{
		ID:      row.ID,
		Status:  row.Status,
		Request: stringValue(row.Request),
		DAGJSON: stringValue(row.DagJson),
		Created: row.Created,
		Updated: row.Updated,
	}
}

func nullableString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func stringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
