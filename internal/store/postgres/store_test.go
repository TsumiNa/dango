package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
)

const postgresTestDSNEnv = "DANGO_POSTGRES_TEST_DSN"

func TestOpen_RequiresDSN(t *testing.T) {
	t.Parallel()

	if _, err := Open(""); err == nil {
		t.Fatal("Open accepted empty dsn")
	}
}

func TestOpen_AppliesMigrationsAndReopen(t *testing.T) {
	dsn := postgresTestDSN(t)

	store, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(first): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	reopened, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(reopen): %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopen): %v", err)
		}
	}()
	if err := reopened.db.PingContext(context.Background()); err != nil {
		t.Fatalf("PingContext: %v", err)
	}
}

func postgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(postgresTestDSNEnv))
	if dsn == "" {
		t.Skipf("%s is not set; skipping postgres integration test", postgresTestDSNEnv)
	}
	return dsn
}

func mustPostgresStore(t *testing.T) (*Store, func()) {
	t.Helper()
	store, err := Open(postgresTestDSN(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanup := func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	return store, cleanup
}
