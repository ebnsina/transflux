package db

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestMigrateIsIdempotent runs against a database it creates and drops itself.
// It must not share one with the other packages: Go runs package tests in
// parallel, and this test needs an empty schema.
func TestMigrateIsIdempotent(t *testing.T) {
	url := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL to run migration tests")
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	name := "transflux_migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "drop database if exists "+name); err != nil {
			t.Logf("could not drop %s: %v", name, err)
		}
	})

	pool, err := Open(ctx, swapDatabase(url, name))
	if err != nil {
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

	// Every migration must actually have run, not merely been recorded.
	var tables int
	if err := pool.QueryRow(ctx,
		`select count(*) from pg_tables where schemaname = 'public'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables < 10 {
		t.Errorf("only %d tables exist after migrating", tables)
	}
	pool.Close()
}

// swapDatabase replaces the database name in a connection URL.
func swapDatabase(url, name string) string {
	base, query, hasQuery := strings.Cut(url, "?")
	slash := strings.LastIndex(base, "/")
	if slash < 0 {
		return url
	}
	out := base[:slash+1] + name
	if hasQuery {
		return fmt.Sprintf("%s?%s", out, query)
	}
	return out
}
