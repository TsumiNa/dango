package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store wraps the Postgres-backed persistence layer used by runner persistence.
type Store struct {
	db *sql.DB
}

// Open opens the Postgres database at dsn, applies migrations, and verifies
// that the configured database can accept writes.
func Open(dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("open postgres: dsn is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := probeWritable(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("probe postgres writable: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying Postgres connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func probeWritable(db *sql.DB) error {
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TEMP TABLE dango_write_probe(id BIGINT)`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO dango_write_probe(id) VALUES (1)`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE dango_write_probe`); err != nil {
		return err
	}
	return nil
}
