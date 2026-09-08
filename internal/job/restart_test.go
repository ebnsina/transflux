package job

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The control plane holds no scheduling state in memory, so restarting it must
// change nothing. A process that had to be drained before a deploy, or that
// lost work when it stopped, would make every release an operation.
func TestStateSurvivesAControlPlaneRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker(t)
	j, tasks := f.linearJob(t, "restart-"+uuid.NewString())

	assignment, err := f.store.Lease(ctx, f.facts(w, "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, assignment.AttemptID, w, leaseTTL); err != nil {
		t.Fatal(err)
	}

	// A fresh store against the same database is what a restarted process is.
	restarted := NewStore(f.pool)

	after, err := restarted.Get(ctx, f.tenant, j.ID)
	if err != nil {
		t.Fatalf("the job did not survive the restart: %v", err)
	}
	if after.State != JobRunning {
		t.Errorf("job state = %s, want running", after.State)
	}

	// The lease is still held, so the work is not handed out twice.
	if _, err := restarted.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL); !errors.Is(err, ErrNoWork) {
		t.Error("a leased task was handed out again after a restart")
	}

	// And the worker that holds it can still finish, because nothing about its
	// attempt lived in the old process.
	done, err := restarted.Complete(ctx, assignment.AttemptID, w, Result{Success: true})
	if err != nil {
		t.Fatalf("the worker could not finish after the restart: %v", err)
	}
	if !done.Succeeded {
		t.Error("the completion was not recorded")
	}
	if f.tasksByOp(t, j.ID)["encode"].State != TaskQueued {
		t.Error("the dependent task was not released after the restart")
	}
	_ = tasks
}

// Two control-plane instances sweeping at once must not reclaim the same lease
// twice, or one task becomes two attempts on two workers.
func TestConcurrentSweepersDoNotDoubleReclaim(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.linearJob(t, "sweep-"+uuid.NewString())

	a, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	f.expireNow(t, a.AttemptID)

	// A second instance, as a rolling deploy would have.
	other := NewStore(f.pool)

	first, err := f.store.ExpireLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := other.ExpireLeases(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first)+len(second) != 1 {
		t.Fatalf("the lease was reclaimed %d times, want once", len(first)+len(second))
	}
}
