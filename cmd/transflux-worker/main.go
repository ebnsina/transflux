// Command transflux-worker is the data plane: it polls the control plane for
// work, runs media tools as supervised subprocesses, and reports results.
//
// It is deployed independently of the control plane and needs only an outbound
// route to it — no inbound connectivity, no orchestrator, and no database
// credentials of its own.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ebnsina/transflux/internal/agent"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := agent.Config{
		ControlPlaneURL: os.Getenv("TRANSFLUX_CONTROL_PLANE_URL"),
		BootstrapToken:  os.Getenv("TRANSFLUX_WORKER_BOOTSTRAP_TOKEN"),
		Name:            env("TRANSFLUX_WORKER_NAME", defaultName()),
		WorkDir:         env("TRANSFLUX_WORKER_DIR", "."),
		FFmpegBin:       env("TRANSFLUX_FFMPEG", "ffmpeg"),
		FFprobeBin:      env("TRANSFLUX_FFPROBE", "ffprobe"),
		PollInterval:    3 * time.Second,
	}
	if cfg.ControlPlaneURL == "" {
		return fmt.Errorf("TRANSFLUX_CONTROL_PLANE_URL is required")
	}
	if cfg.BootstrapToken == "" {
		return fmt.Errorf("TRANSFLUX_WORKER_BOOTSTRAP_TOKEN is required")
	}

	// Slot capacity is declared, not derived: a 32-core host is not 32
	// concurrent encodes. The default is conservative and meant to be replaced
	// by numbers measured on this hardware.
	if raw := os.Getenv("TRANSFLUX_WORKER_SLOTS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.SlotCapacity); err != nil {
			return fmt.Errorf("TRANSFLUX_WORKER_SLOTS must be a JSON object of class to count: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return agent.New(cfg, log).Run(ctx)
}

// defaultName keeps a restarting worker's identity stable, so a redeploy does
// not add a row to the fleet.
func defaultName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "transflux-worker"
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
