// Command transflux is the control plane: API, scheduling and job state.
//
// It never invokes FFmpeg. Media runs in transflux-worker (ADR-004), which is
// the isolation property the whole design leans on.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ebnsina/transflux/internal/config"
	"github.com/ebnsina/transflux/internal/db"
)

func main() {
	if err := run(); err != nil {
		slog.Error("control plane exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: routes(pool.Ping)}

	errc := make(chan error, 1)
	go func() {
		log.Info("control plane listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Drain in-flight requests. Worker long-polls hold connections, so this
	// deadline is what stops a deploy hanging on them.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// routes takes the readiness probe as a function rather than a pool so the
// handlers stay testable without a database.
func routes(ping func(context.Context) error) http.Handler {
	mux := http.NewServeMux()

	// Liveness: the process is up. Never touches the database, so a database
	// blip does not get the container killed and restarted into the same blip.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	// Readiness: dependencies are usable, so take me out of rotation if not.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := ping(r.Context()); err != nil {
			slog.WarnContext(r.Context(), "readiness check failed", "err", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})

	return mux
}
