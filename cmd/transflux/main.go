// Command transflux is the control plane: API, scheduling and job state.
//
// It never invokes FFmpeg. Media runs in transflux-worker (ADR-004), which is
// the isolation property the whole design leans on.
//
// Usage:
//
//	transflux                            run the control plane
//	transflux bootstrap -name "Acme"     create a tenant and print one admin key
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ebnsina/transflux/internal/api"
	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/audit"
	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/config"
	"github.com/ebnsina/transflux/internal/db"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/pipeline"
	"github.com/ebnsina/transflux/internal/probe"
	"github.com/ebnsina/transflux/internal/storage"
	"github.com/ebnsina/transflux/internal/tenant"
	"github.com/ebnsina/transflux/internal/upload"
	"github.com/ebnsina/transflux/internal/validate"
	"github.com/ebnsina/transflux/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("control plane exited", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
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

	if len(args) > 0 && args[0] == "bootstrap" {
		return bootstrap(ctx, pool, cfg.Env, args[1:])
	}
	store, err := storage.NewS3(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	// Fail the boot on a missing or unreachable bucket rather than letting it
	// surface as a 500 on someone's first upload. Auto-create in dev only.
	if err := store.Verify(ctx, cfg.Env == "dev"); err != nil {
		return err
	}
	return serve(ctx, cfg, pool, store, log)
}

func serve(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, store storage.Store, log *slog.Logger) error {
	workers := worker.NewStore(pool, cfg.WorkerBootstrapToken)

	// Take workers offline once their heartbeats stop. Reclaiming the work they
	// held is a separate mechanism, so a slow network does not abandon work that
	// is still running.
	go worker.Sweep(ctx, workers, cfg.HeartbeatInterval, cfg.WorkerStaleAfter, log)

	// Reclaim work whose worker stopped reporting. This is what makes a worker
	// disposable: a crash, a kill, a partition and a vanished spot instance all
	// end with the task available to somebody else.
	jobs := job.NewStore(pool)
	go job.SweepLeases(ctx, jobs, cfg.LeaseSweepInterval, log)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.New(api.Deps{
			Auth:              auth.NewStore(pool),
			Ping:              pool.Ping,
			Assets:            asset.NewStore(pool),
			Uploads:           upload.NewService(pool, store),
			Workers:           workers,
			Jobs:              jobs,
			HeartbeatInterval: cfg.HeartbeatInterval,
			LeaseTTL:          cfg.LeaseTTL,
			Pipelines:         pipeline.NewStore(pool),
			Probes:            probe.NewStore(pool),
			Artifacts:         artifact.NewStore(pool, store),
			Validations:       validate.NewStore(pool),
			Storage:           store,
			SourceURLTTL:      cfg.SourceURLTTL,
			DownloadURLTTL:    cfg.DownloadURLTTL,
		}).Handler(),
	}

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

// bootstrap creates the first tenant and its admin key. There is no self-serve
// signup: tenants are provisioned deliberately.
func bootstrap(ctx context.Context, pool *pgxpool.Pool, env string, args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	name := fs.String("name", "", "tenant name (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		fs.Usage()
		return errors.New("bootstrap: -name is required")
	}

	keyEnv := "live"
	if env == "dev" {
		keyEnv = "test"
	}

	t, err := tenant.NewStore(pool).Create(ctx, *name)
	if err != nil {
		return err
	}
	key, keyID, err := auth.NewStore(pool).Issue(ctx, t.ID, "bootstrap", keyEnv,
		[]string{auth.ScopeAdmin}, nil)
	if err != nil {
		return err
	}

	rec := audit.NewRecorder(pool)
	rec.Record(ctx, audit.Event{
		TenantID: &t.ID, ActorType: "system", Action: "tenant.created",
		SubjectType: "tenant", SubjectID: &t.ID,
		Detail: map[string]any{"name": t.Name},
	})
	rec.Record(ctx, audit.Event{
		TenantID: &t.ID, ActorType: "system", Action: "api_key.issued",
		SubjectType: "api_key", SubjectID: &keyID,
		Detail: map[string]any{"name": "bootstrap", "scopes": []string{auth.ScopeAdmin}},
	})

	// Printed to stdout exactly once. Only its hash is stored; there is no
	// recovery path, by design.
	fmt.Printf("tenant_id:  %s\napi_key:    %s\n\nStore the key now — it is not recoverable.\n",
		t.ID, key.Secret)
	return nil
}
