// Package config loads runtime configuration from the environment.
//
// Environment only: the same binary must run under Compose, systemd, Nomad or
// Kubernetes without knowing which (ADR-008). No config files, no flags.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ebnsina/transflux/internal/storage"
)

type Config struct {
	Env             string // "dev" | "prod"
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	Storage         storage.Config

	// WorkerBootstrapToken authenticates worker registration only. It is
	// shared across the fleet, so it is exchanged immediately for a per-worker
	// credential and is never accepted for anything else.
	WorkerBootstrapToken string
	HeartbeatInterval    time.Duration
	// A worker is presumed lost after this long without a heartbeat. Three
	// missed beats, so one slow tick does not evict a healthy worker.
	WorkerStaleAfter time.Duration
	// How long a lease survives without a progress report. Long enough that a
	// slow chunk does not lose its lease, short enough that a dead worker's
	// work is reclaimed promptly.
	LeaseTTL time.Duration
	// How often expired leases are reclaimed. Frequent enough that a dead
	// worker's task restarts promptly, cheap enough to run forever.
	LeaseSweepInterval time.Duration
	// How long a worker's presigned source URL stays valid. Comfortably longer
	// than a lease, so a long encode does not lose its input part way through.
	SourceURLTTL time.Duration
}

func Load() (Config, error) {
	c := Config{
		Env:             env("TRANSFLUX_ENV", "dev"),
		HTTPAddr:        env("TRANSFLUX_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("TRANSFLUX_DATABASE_URL"),
		ShutdownTimeout: 20 * time.Second,
	}

	if c.DatabaseURL == "" {
		return c, fmt.Errorf("TRANSFLUX_DATABASE_URL is required")
	}
	if c.Env != "dev" && c.Env != "prod" {
		return c, fmt.Errorf("TRANSFLUX_ENV must be dev or prod, got %q", c.Env)
	}

	c.Storage = storage.Config{
		Endpoint:       os.Getenv("TRANSFLUX_S3_ENDPOINT"), // empty means real AWS S3
		PublicEndpoint: os.Getenv("TRANSFLUX_S3_PUBLIC_ENDPOINT"),
		WorkerEndpoint: os.Getenv("TRANSFLUX_S3_WORKER_ENDPOINT"),
		Region:         env("TRANSFLUX_S3_REGION", "us-east-1"),
		Bucket:         os.Getenv("TRANSFLUX_S3_BUCKET"),
		AccessKey:      os.Getenv("TRANSFLUX_S3_ACCESS_KEY"),
		SecretKey:      os.Getenv("TRANSFLUX_S3_SECRET_KEY"),
		PathStyle:      os.Getenv("TRANSFLUX_S3_PATH_STYLE") == "true",
	}
	if c.Storage.Bucket == "" {
		return c, fmt.Errorf("TRANSFLUX_S3_BUCKET is required")
	}

	c.WorkerBootstrapToken = os.Getenv("TRANSFLUX_WORKER_BOOTSTRAP_TOKEN")
	if c.WorkerBootstrapToken == "" {
		return c, fmt.Errorf("TRANSFLUX_WORKER_BOOTSTRAP_TOKEN is required")
	}
	c.HeartbeatInterval = 10 * time.Second
	c.WorkerStaleAfter = 3 * c.HeartbeatInterval
	c.LeaseTTL = 60 * time.Second
	if raw := os.Getenv("TRANSFLUX_LEASE_TTL_SECONDS"); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil || secs < 5 {
			return c, fmt.Errorf("TRANSFLUX_LEASE_TTL_SECONDS must be an integer >= 5, got %q", raw)
		}
		c.LeaseTTL = time.Duration(secs) * time.Second
	}
	c.LeaseSweepInterval = 5 * time.Second
	c.SourceURLTTL = 6 * time.Hour

	lvl := env("TRANSFLUX_LOG_LEVEL", "info")
	if err := c.LogLevel.UnmarshalText([]byte(strings.ToUpper(lvl))); err != nil {
		return c, fmt.Errorf("invalid TRANSFLUX_LOG_LEVEL %q: %w", lvl, err)
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
