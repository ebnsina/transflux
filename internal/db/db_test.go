package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
)

// Integration test. Skips unless TRANSFLUX_TEST_DATABASE_URL points at a
// throwaway database — this drops the public schema before running.
func TestMigrateIsIdempotent(t *testing.T) {
	url := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL to run migration tests")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `drop schema public cascade; create schema public`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Running again must be a no-op, not an error: every boot calls this.
	if err := Migrate(ctx, pool, log); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	want, err := migrationNames()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	if err := pool.QueryRow(ctx, `select count(*) from schema_migrations`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != len(want) {
		t.Fatalf("schema_migrations has %d rows, want %d", got, len(want))
	}
}
