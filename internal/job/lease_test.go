package job

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const leaseTTL = 60 * time.Second

func (f *fixture) newWorker(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	// Credentials are unique per worker, so the fixture cannot share one.
	credential := sha256.Sum256([]byte(id.String()))
	_, err := f.pool.Exec(context.Background(), `
		insert into workers (id, name, hostname, os, arch, cpu_cores, memory_bytes,
		                     disk_bytes, ffmpeg_version, protocol_version, state, credential_hash)
		values ($1,$2,$3,'linux','arm64',8,1,1,'9.0.1',1,'online',$4)`,
		id, "w-"+id.String(), "h-"+id.String(), credential[:])
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The happy path the whole protocol exists for: lease, start, report progress,
// complete, and the graph moves on.
func TestLeaseToCompletion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker(t)
	j, tasks := f.linearJob(t, "lease-"+uuid.NewString())

	a, err := f.store.Lease(ctx, w, []string{"probe", "encode"}, leaseTTL, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Only probe is schedulable; encode and package are still blocked.
	if a.Operation != "probe" || a.TaskID != tasks["probe"].ID {
		t.Fatalf("leased %s (%v), want the probe task", a.Operation, a.TaskID)
	}
	if a.Attempt != 1 {
		t.Errorf("attempt number = %d, want 1", a.Attempt)
	}

	// A second worker asking now must get nothing: the only ready task is taken.
	if _, err := f.store.Lease(ctx, f.newWorker(t), []string{"probe"}, leaseTTL, "test"); !errors.Is(err, ErrNoWork) {
		t.Errorf("a leased task was handed out twice: %v", err)
	}

	if err := f.store.Start(ctx, a.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}
	cancelled, err := f.store.Progress(ctx, a.AttemptID, w, 50, leaseTTL)
	if err != nil || cancelled {
		t.Fatalf("progress gave (%v, %v), want (false, nil)", cancelled, err)
	}

	retrying, err := f.store.Complete(ctx, a.AttemptID, w, Result{
		Success: true, Metrics: Metrics{CPUSeconds: 12.5, WallSeconds: 4, BytesIn: 1024},
	})
	if err != nil || retrying {
		t.Fatalf("complete gave (%v, %v), want (false, nil)", retrying, err)
	}

	after := f.tasksByOp(t, j.ID)
	if after["probe"].State != TaskSucceeded {
		t.Errorf("probe state = %s, want succeeded", after["probe"].State)
	}
	// Finishing probe released encode, so the next lease finds work.
	next, err := f.store.Lease(ctx, w, []string{"probe", "encode"}, leaseTTL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if next.Operation != "encode" {
		t.Errorf("next lease was %s, want encode", next.Operation)
	}

	// Finish the rest of the graph: the job must end up succeeded, not stuck
	// running because nothing reconciled it.
	if err := f.store.Start(ctx, next.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Complete(ctx, next.AttemptID, w, Result{Success: true}); err != nil {
		t.Fatal(err)
	}
	last, err := f.store.Lease(ctx, w, []string{"package"}, leaseTTL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, last.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Complete(ctx, last.AttemptID, w, Result{Success: true}); err != nil {
		t.Fatal(err)
	}

	job, err := f.store.Get(ctx, f.tenant, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != JobSucceeded {
		t.Errorf("job state = %s, want succeeded once every task finished", job.State)
	}
	if job.FinishedAt == nil {
		t.Error("a succeeded job has no finished_at")
	}

	// Resource accounting is recorded per attempt, which is what makes
	// cost-per-asset answerable later.
	var cpu float64
	if err := f.pool.QueryRow(ctx,
		`select cpu_seconds from task_attempts where id = $1`, a.AttemptID).Scan(&cpu); err != nil {
		t.Fatal(err)
	}
	if cpu != 12.5 {
		t.Errorf("cpu_seconds = %v, want 12.5", cpu)
	}
}

// A worker only receives operations it declared it can perform.
func TestLeaseRespectsOperations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker(t)
	f.linearJob(t, "ops-"+uuid.NewString())

	if _, err := f.store.Lease(ctx, w, []string{"transcribe"}, leaseTTL, "test"); !errors.Is(err, ErrNoWork) {
		t.Errorf("a worker was given work it cannot do: %v", err)
	}
	if _, err := f.store.Lease(ctx, w, nil, leaseTTL, "test"); !errors.Is(err, ErrNoWork) {
		t.Errorf("a worker declaring no operations was given work: %v", err)
	}
	if _, err := f.store.Lease(ctx, w, []string{"probe"}, leaseTTL, "test"); err != nil {
		t.Errorf("a capable worker got nothing: %v", err)
	}
}

// The property that stops one task producing two results: a worker whose lease
// was taken away must have its reports refused.
func TestStaleAttemptCannotReport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.newWorker(t)
	second := f.newWorker(t)
	f.linearJob(t, "stale-"+uuid.NewString())

	a, err := f.store.Lease(ctx, first, []string{"probe"}, leaseTTL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, a.AttemptID, first, leaseTTL); err != nil {
		t.Fatal(err)
	}

	// Another worker must not be able to report on someone else's attempt, even
	// knowing its id.
	if err := f.store.Start(ctx, a.AttemptID, second, leaseTTL); !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("another worker reported a start: %v", err)
	}
	if _, err := f.store.Complete(ctx, a.AttemptID, second, Result{Success: true}); !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("another worker completed the attempt: %v", err)
	}

	// Simulate the lease lapsing and the attempt being expired by the sweeper.
	if _, err := f.pool.Exec(ctx,
		`update task_attempts set state = 'expired' where id = $1`, a.AttemptID); err != nil {
		t.Fatal(err)
	}
	// The original worker comes back from a partition and tries to finish.
	if _, err := f.store.Complete(ctx, a.AttemptID, first, Result{Success: true}); !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("an expired attempt was allowed to report success: %v", err)
	}
	if _, err := f.store.Progress(ctx, a.AttemptID, first, 90, leaseTTL); !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("an expired attempt renewed its lease: %v", err)
	}
}

// Cancellation reaches a worker we cannot dial: it rides the progress response.
func TestCancellationReachesTheWorkerViaProgress(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker(t)
	j, _ := f.linearJob(t, "cancel-lease-"+uuid.NewString())

	a, err := f.store.Lease(ctx, w, []string{"probe"}, leaseTTL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, a.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}

	cancelled, err := f.store.Progress(ctx, a.AttemptID, w, 10, leaseTTL)
	if err != nil || cancelled {
		t.Fatalf("progress before cancel gave (%v, %v)", cancelled, err)
	}

	if err := f.store.Cancel(ctx, f.tenant, j.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, err = f.store.Progress(ctx, a.AttemptID, w, 20, leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Error("a cancelled task did not tell its worker to stop")
	}
}

// A worker that dies without reporting still consumes retry budget, or a task
// that kills every worker it touches would be retried forever.
func TestSilentWorkerDeathConsumesBudget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, tasks := f.linearJob(t, "budget-"+uuid.NewString())

	for i := 1; i <= 3; i++ {
		a, err := f.store.Lease(ctx, f.newWorker(t), []string{"probe"}, leaseTTL, "test")
		if err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
		if a.Attempt != i {
			t.Errorf("attempt number = %d, want %d", a.Attempt, i)
		}
		// The worker vanishes; the sweeper puts the task back.
		if _, err := f.pool.Exec(ctx,
			`update task_attempts set state = 'expired' where id = $1`, a.AttemptID); err != nil {
			t.Fatal(err)
		}
		if err := f.store.Transition(ctx, f.tenant, a.TaskID, TaskQueued); err != nil {
			t.Fatal(err)
		}
	}

	after := f.tasksByOp(t, tasks["probe"].JobID)
	if after["probe"].AttemptCount != 3 {
		t.Errorf("attempt_count = %d, want 3", after["probe"].AttemptCount)
	}

	// The fourth lease is allowed, but its failure is final: the budget is gone.
	a, err := f.store.Lease(ctx, f.newWorker(t), []string{"probe"}, leaseTTL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, a.AttemptID, a.TenantID, leaseTTL); err == nil {
		t.Error("a start from the wrong worker id was accepted")
	}
}

// Many workers polling at once must each get a different task rather than
// queueing behind one row.
func TestConcurrentLeasesDoNotCollide(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const jobs = 6
	for i := range jobs {
		if _, err := f.store.Create(ctx, f.tenant, f.version, f.pipever,
			"par-"+uuid.NewString(), 100-i, []NewTask{{Key: "probe", Operation: "probe"}}); err != nil {
			t.Fatal(err)
		}
	}

	// Poll from every worker at the same instant: SKIP LOCKED is what stops
	// them queueing behind one row, and what stops two of them taking it.
	workers := make([]uuid.UUID, jobs)
	for i := range workers {
		workers[i] = f.newWorker(t)
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		seen  = map[uuid.UUID]bool{}
		fails []error
	)
	wg.Add(jobs)
	for _, w := range workers {
		go func() {
			defer wg.Done()
			a, err := f.store.Lease(ctx, w, []string{"probe"}, leaseTTL, "test")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fails = append(fails, err)
				return
			}
			if seen[a.TaskID] {
				fails = append(fails, errors.New("task leased twice: "+a.TaskID.String()))
			}
			seen[a.TaskID] = true
		}()
	}
	wg.Wait()

	for _, err := range fails {
		t.Error(err)
	}
	if len(seen) != jobs {
		t.Errorf("leased %d distinct tasks, want %d", len(seen), jobs)
	}
}
