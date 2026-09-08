package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	setRequired := func(t *testing.T) {
		t.Setenv("TRANSFLUX_DATABASE_URL", "postgres://x")
		t.Setenv("TRANSFLUX_S3_BUCKET", "transflux")
		t.Setenv("TRANSFLUX_WORKER_BOOTSTRAP_TOKEN", "bootstrap-secret")
		t.Setenv("TRANSFLUX_PLAYBACK_SECRET", "playback-secret")
	}

	t.Run("requires a database url", func(t *testing.T) {
		setRequired(t)
		t.Setenv("TRANSFLUX_DATABASE_URL", "")
		if _, err := Load(); err == nil {
			t.Fatal("want error when TRANSFLUX_DATABASE_URL is unset")
		}
	})

	t.Run("requires a bucket", func(t *testing.T) {
		setRequired(t)
		t.Setenv("TRANSFLUX_S3_BUCKET", "")
		if _, err := Load(); err == nil {
			t.Fatal("want error when TRANSFLUX_S3_BUCKET is unset")
		}
	})

	t.Run("rejects an unknown env", func(t *testing.T) {
		setRequired(t)
		t.Setenv("TRANSFLUX_ENV", "staging")
		if _, err := Load(); err == nil {
			t.Fatal("want error for an unknown TRANSFLUX_ENV")
		}
	})

	t.Run("rejects an unknown log level", func(t *testing.T) {
		setRequired(t)
		t.Setenv("TRANSFLUX_LOG_LEVEL", "chatty")
		if _, err := Load(); err == nil {
			t.Fatal("want error for an unknown TRANSFLUX_LOG_LEVEL")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		setRequired(t)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.Env != "dev" || c.HTTPAddr != ":8080" || c.LogLevel != slog.LevelInfo {
			t.Fatalf("unexpected defaults: %+v", c)
		}
	})
}
