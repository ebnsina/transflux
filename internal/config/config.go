// Package config loads runtime configuration from the environment.
//
// Environment only: the same binary must run under Compose, systemd, Nomad or
// Kubernetes without knowing which (ADR-008). No config files, no flags.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	Env             string // "dev" | "prod"
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
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
