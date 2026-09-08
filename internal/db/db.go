// Package db owns the Postgres pool and forward-only schema migrations.
//
// The migration runner is deliberately small: embedded .sql files applied in
// filename order, each in its own transaction, recorded in schema_migrations.
// No migration CLI to install, nothing extra to ship in the image.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// Migrate applies any migrations not yet recorded. Safe to run on every boot
// and safe to run concurrently: the advisory lock serialises competing
// control-plane instances, and each file is applied at most once.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// Hold the lock on one connection for the whole run so two booting
	// instances cannot apply the same file concurrently.
	if _, err := conn.Exec(ctx, `select pg_advisory_lock(4967213)`); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock(4967213)`)

	if _, err := conn.Exec(ctx, `
		create table if not exists schema_migrations (
			name       text primary key,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `select name from schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`insert into schema_migrations (name) values ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migration %s commit: %w", name, err)
		}
		log.Info("migration applied", "name", name)
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
