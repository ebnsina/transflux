package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Run("requires a database url", func(t *testing.T) {
		t.Setenv("TRANSFLUX_DATABASE_URL", "")
		if _, err := Load(); err == nil {
			t.Fatal("want error when TRANSFLUX_DATABASE_URL is unset")
		}
	})

	t.Run("rejects an unknown env", func(t *testing.T) {
		t.Setenv("TRANSFLUX_DATABASE_URL", "postgres://x")
		t.Setenv("TRANSFLUX_ENV", "staging")
		if _, err := Load(); err == nil {
			t.Fatal("want error for an unknown TRANSFLUX_ENV")
		}
	})

	t.Run("rejects an unknown log level", func(t *testing.T) {
		t.Setenv("TRANSFLUX_DATABASE_URL", "postgres://x")
		t.Setenv("TRANSFLUX_LOG_LEVEL", "chatty")
		if _, err := Load(); err == nil {
			t.Fatal("want error for an unknown TRANSFLUX_LOG_LEVEL")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		t.Setenv("TRANSFLUX_DATABASE_URL", "postgres://x")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.Env != "dev" || c.HTTPAddr != ":8080" || c.LogLevel != slog.LevelInfo {
			t.Fatalf("unexpected defaults: %+v", c)
		}
	})
}
