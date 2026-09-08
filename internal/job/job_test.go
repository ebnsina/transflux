package job

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	store   *Store
	pool    *pgxpool.Pool
	tenant  uuid.UUID
	version uuid.UUID
	pipever uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	url := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL to run integration tests")
	}
	ctx := context.Background()

	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}

	f := &fixture{store: NewStore(pool), pool: pool, tenant: uuid.Must(uuid.NewV7())}
	if _, err := pool.Exec(ctx,
		`insert into tenants (id, name, status) values ($1, $2, 'active')`,
		f.tenant, "t-"+f.tenant.String()); err != nil {
		t.Fatal(err)
	}

	_, v, err := asset.NewStore(pool).Create(ctx, f.tenant, nil, nil, "managed")
	if err != nil {
		t.Fatal(err)
	}
	f.version = v.ID

	pipeID := uuid.Must(uuid.NewV7())
	f.pipever = uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx,
		`insert into pipelines (id, tenant_id, name) values ($1, $2, $3)`,
		pipeID, f.tenant, "p-"+pipeID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`insert into pipeline_versions (id, tenant_id, pipeline_id, version, definition)
		 values ($1, $2, $3, 1, '{}')`, f.pipever, f.tenant, pipeID); err != nil {
		t.Fatal(err)
	}
	return f
}

// probe -> encode -> package, the shape every real pipeline has.
func (f *fixture) linearJob(t *testing.T, key string) (Job, map[string]Task) {
	t.Helper()
	j, err := f.store.Create(context.Background(), f.tenant, f.version, f.pipever, key, 100,
		[]NewTask{
			{Key: "probe", Operation: "probe"},
			{Key: "encode", Operation: "encode", DependsOn: []string{"probe"}},
			{Key: "package", Operation: "package", DependsOn: []string{"encode"}},
		})
	if err != nil {
		t.Fatal(err)
	}
	return j, f.tasksByOp(t, j.ID)
}

func (f *fixture) tasksByOp(t *testing.T, jobID uuid.UUID) map[string]Task {
	t.Helper()
	tasks, err := f.store.Tasks(context.Background(), f.tenant, jobID)
	if err != nil {
		t.Fatal(err)
	}
	byOp := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		byOp[task.Operation] = task
	}
	return byOp
}

// Only tasks whose dependencies are satisfied may be scheduled; the rest must
// wait, or a worker would encode before the source has been probed.
func TestDependenciesGateScheduling(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	j, tasks := f.linearJob(t, "dep-"+uuid.NewString())

	if tasks["probe"].State != TaskQueued {
		t.Errorf("probe state = %s, want queued", tasks["probe"].State)
	}
	for _, op := range []string{"encode", "package"} {
		if tasks[op].State != TaskPending {
			t.Errorf("%s state = %s, want pending", op, tasks[op].State)
		}
	}

	// Finishing probe releases encode, and only encode.
	mustRun(t, f, tasks["probe"].ID)
	if err := f.store.Transition(ctx, f.tenant, tasks["probe"].ID, TaskSucceeded); err != nil {
		t.Fatal(err)
	}
	now := f.tasksByOp(t, j.ID)
	if now["encode"].State != TaskQueued {
		t.Errorf("encode state = %s, want queued once probe succeeded", now["encode"].State)
	}
	if now["package"].State != TaskPending {
		t.Errorf("package state = %s, want still pending", now["package"].State)
	}

	// The job is running as soon as any task has progressed.
	job, err := f.store.Get(ctx, f.tenant, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != JobRunning {
		t.Errorf("job state = %s, want running", job.State)
	}
	if job.StartedAt == nil {
		t.Error("job has no started_at")
	}

	mustRun(t, f, now["encode"].ID)
	if err := f.store.Transition(ctx, f.tenant, now["encode"].ID, TaskSucceeded); err != nil {
		t.Fatal(err)
	}
	last := f.tasksByOp(t, j.ID)
	mustRun(t, f, last["package"].ID)
	if err := f.store.Transition(ctx, f.tenant, last["package"].ID, TaskSucceeded); err != nil {
		t.Fatal(err)
	}

	job, _ = f.store.Get(ctx, f.tenant, j.ID)
	if job.State != JobSucceeded {
		t.Errorf("job state = %s, want succeeded", job.State)
	}
	if job.FinishedAt == nil {
		t.Error("a finished job has no finished_at")
	}
}

func mustRun(t *testing.T, f *fixture, taskID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.Transition(ctx, f.tenant, taskID, TaskLeased); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Transition(ctx, f.tenant, taskID, TaskRunning); err != nil {
		t.Fatal(err)
	}
}

func TestIllegalTransitionsAreRejectedInTheDatabase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, tasks := f.linearJob(t, "illegal-"+uuid.NewString())
	probe := tasks["probe"].ID

	// queued -> running skips leasing.
	var ill ErrIllegalTransition
	if err := f.store.Transition(ctx, f.tenant, probe, TaskRunning); !errors.As(err, &ill) {
		t.Errorf("queued -> running returned %v, want an illegal transition", err)
	}
	// A pending task cannot be leased before its dependencies are done.
	if err := f.store.Transition(ctx, f.tenant, tasks["encode"].ID, TaskLeased); !errors.As(err, &ill) {
		t.Errorf("pending -> leased returned %v, want an illegal transition", err)
	}

	mustRun(t, f, probe)
	if err := f.store.Transition(ctx, f.tenant, probe, TaskSucceeded); err != nil {
		t.Fatal(err)
	}
	// A duplicate success report from a worker that retried its callback.
	if err := f.store.Transition(ctx, f.tenant, probe, TaskSucceeded); !errors.As(err, &ill) {
		t.Errorf("duplicate success returned %v, want an illegal transition", err)
	}
	if err := f.store.Transition(ctx, f.tenant, probe, TaskRunning); !errors.As(err, &ill) {
		t.Errorf("resurrecting a succeeded task returned %v, want an illegal transition", err)
	}
}

func TestRetryPolicy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	t.Run("a transient failure requeues until the budget runs out", func(t *testing.T) {
		_, tasks := f.linearJob(t, "retry-"+uuid.NewString())
		probe := tasks["probe"].ID

		for i := 1; i <= 3; i++ {
			mustRun(t, f, probe)
			retrying, err := f.store.Fail(ctx, f.tenant, probe, ClassTransient, "worker crashed")
			if err != nil {
				t.Fatal(err)
			}
			if !retrying {
				t.Fatalf("attempt %d did not retry a transient failure", i)
			}
		}
	})

	// Retrying media that will never decode just burns a worker three times.
	t.Run("a permanent failure never requeues", func(t *testing.T) {
		_, tasks := f.linearJob(t, "perm-"+uuid.NewString())
		probe := tasks["probe"].ID
		mustRun(t, f, probe)

		retrying, err := f.store.Fail(ctx, f.tenant, probe, ClassPermanentInput, "not decodable")
		if err != nil {
			t.Fatal(err)
		}
		if retrying {
			t.Fatal("a permanent input failure was retried")
		}

		after := f.tasksByOp(t, tasks["probe"].JobID)
		if after["probe"].State != TaskFailed {
			t.Errorf("task state = %s, want failed", after["probe"].State)
		}
		// Its dependents must never run.
		if after["encode"].State != TaskPending {
			t.Errorf("encode state = %s, want pending", after["encode"].State)
		}

		job, _ := f.store.Get(ctx, f.tenant, tasks["probe"].JobID)
		if job.State != JobFailed {
			t.Errorf("job state = %s, want failed", job.State)
		}
	})

	t.Run("an unrecognised class is treated as unknown and retried once", func(t *testing.T) {
		_, tasks := f.linearJob(t, "unk-"+uuid.NewString())
		mustRun(t, f, tasks["probe"].ID)
		retrying, err := f.store.Fail(ctx, f.tenant, tasks["probe"].ID, FailureClass("garbage"), "?")
		if err != nil {
			t.Fatal(err)
		}
		if !retrying {
			t.Error("an unclassified failure should retry rather than fail outright")
		}
	})
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	j, tasks := f.linearJob(t, "cancel-"+uuid.NewString())

	mustRun(t, f, tasks["probe"].ID)
	if err := f.store.Transition(ctx, f.tenant, tasks["probe"].ID, TaskSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Cancel(ctx, f.tenant, j.ID); err != nil {
		t.Fatal(err)
	}

	after := f.tasksByOp(t, j.ID)
	// A task that already succeeded keeps its result: its outputs exist.
	if after["probe"].State != TaskSucceeded {
		t.Errorf("probe state = %s, want succeeded", after["probe"].State)
	}
	for _, op := range []string{"encode", "package"} {
		if after[op].State != TaskCancelled {
			t.Errorf("%s state = %s, want cancelled", op, after[op].State)
		}
	}

	job, _ := f.store.Get(ctx, f.tenant, j.ID)
	if job.State != JobCancelled {
		t.Errorf("job state = %s, want cancelled", job.State)
	}
	// Cancelling twice is a no-op, not an error.
	if err := f.store.Cancel(ctx, f.tenant, j.ID); err != nil {
		t.Errorf("second cancel returned %v, want nil", err)
	}
}

func TestIdempotentCreate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "idem-" + uuid.NewString()

	first, _ := f.linearJob(t, key)
	second, err := f.store.Create(ctx, f.tenant, f.version, f.pipever, key, 100,
		[]NewTask{{Key: "probe", Operation: "probe"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("the same idempotency key created two jobs: %s and %s", first.ID, second.ID)
	}
	// And it must not have queued a second copy of the work.
	tasks, _ := f.store.Tasks(ctx, f.tenant, first.ID)
	if len(tasks) != 3 {
		t.Errorf("job has %d tasks, want 3", len(tasks))
	}
}

// Two workers reporting the same task at once: exactly one may win, or the
// same work gets counted twice.
func TestConcurrentTransitionsHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, tasks := f.linearJob(t, "race-"+uuid.NewString())
	probe := tasks["probe"].ID

	const racers = 8
	var wg sync.WaitGroup
	results := make([]error, racers)
	wg.Add(racers)
	for i := range racers {
		go func() {
			defer wg.Done()
			results[i] = f.store.Transition(ctx, f.tenant, probe, TaskLeased)
		}()
	}
	wg.Wait()

	won := 0
	for _, err := range results {
		if err == nil {
			won++
			continue
		}
		var ill ErrIllegalTransition
		if !errors.As(err, &ill) {
			t.Errorf("loser got %v, want an illegal transition", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d racers won the lease, want exactly 1", won)
	}
}

func TestJobsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	j, tasks := f.linearJob(t, "scope-"+uuid.NewString())

	other := uuid.Must(uuid.NewV7())
	if _, err := f.pool.Exec(ctx,
		`insert into tenants (id, name, status) values ($1, $2, 'active')`,
		other, "t-"+other.String()); err != nil {
		t.Fatal(err)
	}

	if _, err := f.store.Get(ctx, other, j.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant get returned %v, want ErrNotFound", err)
	}
	if err := f.store.Cancel(ctx, other, j.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant cancel returned %v, want ErrNotFound", err)
	}
	if err := f.store.Transition(ctx, other, tasks["probe"].ID, TaskLeased); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant transition returned %v, want ErrNotFound", err)
	}
	// The owner is unaffected.
	if _, err := f.store.Get(ctx, f.tenant, j.ID); err != nil {
		t.Errorf("owner lost access to their own job: %v", err)
	}
}
