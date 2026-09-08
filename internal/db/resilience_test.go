package db

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// The control plane must survive its database going away and coming back.
//
// A restart, a failover or a connection reset all look the same from here: the
// pool's connections are gone and the next query has to make new ones. If it
// does not, a database blip becomes an outage that lasts until someone
// restarts the process.
func TestPoolRecoversFromConnectionLoss(t *testing.T) {
	url := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL to run integration tests")
	}
	ctx := context.Background()

	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := pool.QueryRow(ctx, `select 1`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// Terminate our own backends, which is what a restart does to them.
	if _, err := pool.Exec(ctx, `
		select pg_terminate_backend(pid) from pg_stat_activity
		 where datname = current_database() and pid <> pg_backend_pid()`); err != nil {
		t.Fatalf("could not terminate connections: %v", err)
	}

	// The first query afterwards may fail on a dead connection; what matters is
	// that the pool recovers rather than staying broken.
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		var got int
		if err := pool.QueryRow(ctx, `select 1`).Scan(&got); err == nil && got == 1 {
			return
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the pool never recovered: %v", lastErr)
}

// Readiness has to reflect reality, or a broken instance keeps taking traffic.
func TestPingReportsAClosedPool(t *testing.T) {
	url := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL to run integration tests")
	}
	ctx := context.Background()

	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("a healthy pool failed its ping: %v", err)
	}

	pool.Close()
	if err := pool.Ping(ctx); err == nil {
		t.Error("a closed pool reported itself healthy, so readiness would lie")
	}
}
