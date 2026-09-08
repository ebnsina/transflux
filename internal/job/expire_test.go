package job

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// expireNow makes an attempt's lease look lapsed, standing in for a worker
// that crashed, was killed, or fell off the network.
func (f *fixture) expireNow(t *testing.T, attemptID uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`update task_attempts set lease_expires_at = now() - interval '1 second' where id = $1`,
		attemptID); err != nil {
		t.Fatal(err)
	}
}

// The property the whole lease design exists for: kill a worker mid-task and
// the job still finishes, on a different worker, without the dead one being
// able to interfere.
func TestWorkerDeathMidTaskIsRecovered(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	dead := f.newWorker(t)
	alive := f.newWorker(t)
	j, tasks := f.linearJob(t, "death-"+uuid.NewString())

	a, err := f.store.Lease(ctx, f.facts(dead, "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, a.AttemptID, dead, leaseTTL); err != nil {
		t.Fatal(err)
	}

	// While it holds the lease, nobody else may have the task.
	if _, err := f.store.Lease(ctx, f.facts(alive, "probe"), leaseTTL); !errors.Is(err, ErrNoWork) {
		t.Fatal("a running task was handed to a second worker")
	}

	// The worker dies. Its lease lapses and the sweeper reclaims the task.
	f.expireNow(t, a.AttemptID)
	expired, err := f.store.ExpireLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || !expired[0].Requeued {
		t.Fatalf("sweep returned %+v, want one requeued task", expired)
	}

	after := f.tasksByOp(t, j.ID)
	if after["probe"].State != TaskQueued {
		t.Fatalf("task state = %s, want queued", after["probe"].State)
	}

	// A second worker picks it up as attempt 2.
	b, err := f.store.Lease(ctx, f.facts(alive, "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if b.TaskID != tasks["probe"].ID {
		t.Errorf("a different task was leased: %v", b.TaskID)
	}
	if b.Attempt != 2 {
		t.Errorf("attempt number = %d, want 2", b.Attempt)
	}

	// The dead worker comes back and tries to finish. It must be refused, or
	// one task produces two results.
	if _, err := f.store.Complete(ctx, a.AttemptID, dead, Result{Success: true}); !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("the dead worker completed its stale attempt: %v", err)
	}

	if err := f.store.Start(ctx, b.AttemptID, alive, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Complete(ctx, b.AttemptID, alive, Result{Success: true}); err != nil {
		t.Fatal(err)
	}

	// Exactly one attempt succeeded; the other is on record as expired.
	var succeeded, expiredCount int
	if err := f.pool.QueryRow(ctx, `
		select count(*) filter (where state = 'succeeded'),
		       count(*) filter (where state = 'expired')
		  from task_attempts where task_id = $1`, tasks["probe"].ID,
	).Scan(&succeeded, &expiredCount); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || expiredCount != 1 {
		t.Errorf("attempts: %d succeeded, %d expired; want 1 and 1", succeeded, expiredCount)
	}

	// The graph moved on, so recovery was real and not just bookkeeping.
	if f.tasksByOp(t, j.ID)["encode"].State != TaskQueued {
		t.Error("the dependent task was not released after recovery")
	}
}

// A task that kills every worker it touches must eventually stop, or it
// consumes the fleet forever.
func TestRepeatedWorkerDeathExhaustsBudget(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	j, _ := f.linearJob(t, "budget-"+uuid.NewString())

	for i := 1; i <= 3; i++ {
		a, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL)
		if err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
		f.expireNow(t, a.AttemptID)
		expired, err := f.store.ExpireLeases(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		wantRequeue := i < 3
		if expired[0].Requeued != wantRequeue {
			t.Fatalf("attempt %d requeued = %v, want %v", i, expired[0].Requeued, wantRequeue)
		}
	}

	after := f.tasksByOp(t, j.ID)
	if after["probe"].State != TaskFailed {
		t.Errorf("task state = %s, want failed once the budget was spent", after["probe"].State)
	}
	if after["probe"].FailureClass == nil || *after["probe"].FailureClass != string(ClassTransient) {
		t.Errorf("failure class = %v, want transient", after["probe"].FailureClass)
	}
	job, _ := f.store.Get(ctx, f.tenant, j.ID)
	if job.State != JobFailed {
		t.Errorf("job state = %s, want failed", job.State)
	}
	// And it must not be handed out again.
	if _, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL); !errors.Is(err, ErrNoWork) {
		t.Error("a permanently failed task was leased again")
	}
}

// A lease that lapses on a cancelled task must not restart work someone asked
// us to stop.
func TestExpiryDoesNotResurrectCancelledWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker(t)
	j, _ := f.linearJob(t, "cancel-expire-"+uuid.NewString())

	a, err := f.store.Lease(ctx, f.facts(w, "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, a.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Cancel(ctx, f.tenant, j.ID); err != nil {
		t.Fatal(err)
	}

	f.expireNow(t, a.AttemptID)
	expired, err := f.store.ExpireLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].Requeued {
		t.Fatalf("sweep returned %+v, want the cancelled task left alone", expired)
	}
	if f.tasksByOp(t, j.ID)["probe"].State != TaskCancelled {
		t.Error("a cancelled task was requeued by the sweeper")
	}
	if _, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL); !errors.Is(err, ErrNoWork) {
		t.Error("a cancelled task was leased")
	}
}

// A worker that finishes just as a cancel lands did nothing wrong. Failing its
// report would only make it retry.
func TestCompletingACancelledTaskIsNotAnError(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker(t)
	j, _ := f.linearJob(t, "cancel-race-"+uuid.NewString())

	a, err := f.store.Lease(ctx, f.facts(w, "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, a.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Cancel(ctx, f.tenant, j.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.store.Complete(ctx, a.AttemptID, w, Result{Success: true}); err != nil {
		t.Fatalf("completing a cancelled task returned %v, want it accepted quietly", err)
	}
	// The result is discarded: the task stays cancelled.
	if f.tasksByOp(t, j.ID)["probe"].State != TaskCancelled {
		t.Error("a cancelled task was marked succeeded by a late report")
	}

	var state string
	if err := f.pool.QueryRow(ctx,
		`select state from task_attempts where id = $1`, a.AttemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(AttemptCancelled) {
		t.Errorf("attempt state = %s, want cancelled", state)
	}
}

// A live lease must survive a sweep, or every long encode would be restarted.
func TestSweepLeavesLiveLeasesAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker(t)
	f.linearJob(t, "live-"+uuid.NewString())

	a, err := f.store.Lease(ctx, f.facts(w, "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, a.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}

	expired, err := f.store.ExpireLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("a live lease was reclaimed: %+v", expired)
	}

	// A progress report renews the lease, which is what keeps a slow encode
	// from being taken away mid-run.
	f.expireNow(t, a.AttemptID)
	if _, err := f.store.Progress(ctx, a.AttemptID, w, 50, leaseTTL); err != nil {
		t.Fatal(err)
	}
	expired, err = f.store.ExpireLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Errorf("a renewed lease was still reclaimed: %+v", expired)
	}
}
