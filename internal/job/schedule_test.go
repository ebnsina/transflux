package job

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// requireJob creates a one-task job whose task carries the given requirements.
func (f *fixture) requireJob(t *testing.T, op string, req Requirements) Task {
	t.Helper()
	j, err := f.store.Create(context.Background(), f.tenant, f.version, f.pipever,
		"req-"+uuid.NewString(), 100,
		[]NewTask{{Key: "only", Operation: op, Requirements: req}})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := f.store.Tasks(context.Background(), f.tenant, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tasks[0]
}

// A worker must never receive work it cannot perform. Each case is the one
// constraint that should exclude it, with everything else satisfiable.
func TestHardConstraintsExcludeIncapableWorkers(t *testing.T) {
	capable := WorkerFacts{
		Arch: "amd64", Operations: []string{"encode"}, Encoders: []string{"libx264", "hevc_nvenc"},
		HasGPU: true, MemoryBytes: 64 << 30, DiskFree: 500 << 30,
		SlotCapacity: map[string]int{"encode": 4},
	}

	tests := []struct {
		name    string
		req     Requirements
		degrade func(WorkerFacts) WorkerFacts
	}{
		{"missing encoder", Requirements{Encoder: "hevc_nvenc"},
			func(w WorkerFacts) WorkerFacts { w.Encoders = []string{"libx264"}; return w }},
		{"wrong architecture", Requirements{Arch: "amd64"},
			func(w WorkerFacts) WorkerFacts { w.Arch = "arm64"; return w }},
		{"no GPU", Requirements{GPU: true},
			func(w WorkerFacts) WorkerFacts { w.HasGPU = false; return w }},
		{"not enough memory", Requirements{MemoryBytes: 32 << 30},
			func(w WorkerFacts) WorkerFacts { w.MemoryBytes = 8 << 30; return w }},
		{"not enough disk", Requirements{DiskBytes: 100 << 30},
			func(w WorkerFacts) WorkerFacts { w.DiskFree = 1 << 30; return w }},
		{"operation not supported", Requirements{},
			func(w WorkerFacts) WorkerFacts { w.Operations = []string{"probe"}; return w }},
		{"no capacity for the slot class", Requirements{},
			func(w WorkerFacts) WorkerFacts { w.SlotCapacity = map[string]int{"probe": 4}; return w }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.requireJob(t, "encode", tc.req)

			unfit := f.newWorkerWith(t, tc.degrade(capable))
			if _, err := f.store.Lease(ctx, unfit, leaseTTL); !errors.Is(err, ErrNoWork) {
				t.Fatalf("an incapable worker was given the task: %v", err)
			}

			// The same task must still be leasable by a worker that qualifies,
			// so the test proves the constraint and not a broken query.
			fit := f.newWorkerWith(t, capable)
			if _, err := f.store.Lease(ctx, fit, leaseTTL); err != nil {
				t.Fatalf("a capable worker got nothing: %v", err)
			}
		})
	}
}

// Slot capacity is per workload class and declared by the worker: a 32-core
// host is not 32 concurrent encodes.
func TestSlotCapacityLimitsConcurrentWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for range 5 {
		f.requireJob(t, "encode", Requirements{})
	}
	w := f.newWorkerWith(t, WorkerFacts{
		Arch: "arm64", Operations: []string{"encode"}, Encoders: []string{"libx264"},
		MemoryBytes: 32 << 30, DiskFree: 500 << 30,
		SlotCapacity: map[string]int{"encode": 2},
	})

	for i := 1; i <= 2; i++ {
		if _, err := f.store.Lease(ctx, w, leaseTTL); err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
	}
	// The third must be refused even though work is waiting.
	if _, err := f.store.Lease(ctx, w, leaseTTL); !errors.Is(err, ErrNoWork) {
		t.Fatalf("a worker exceeded its declared capacity: %v", err)
	}

	// Slot usage is counted from live attempts, so finishing one frees a slot.
	var attemptID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`select id from task_attempts where worker_id = $1 limit 1`, w.ID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Start(ctx, attemptID, w.ID, leaseTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Complete(ctx, attemptID, w.ID, Result{Success: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Lease(ctx, w, leaseTTL); err != nil {
		t.Errorf("a freed slot was not reused: %v", err)
	}
}

// Separate classes have separate budgets, so a saturated encode pool does not
// stop the same worker probing.
func TestSlotClassesAreIndependent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.requireJob(t, "encode", Requirements{})
	f.requireJob(t, "encode", Requirements{})
	f.requireJob(t, "probe", Requirements{})

	w := f.newWorkerWith(t, WorkerFacts{
		Arch: "arm64", Operations: []string{"encode", "probe"}, Encoders: []string{"libx264"},
		MemoryBytes: 32 << 30, DiskFree: 500 << 30,
		SlotCapacity: map[string]int{"encode": 1, "probe": 1},
	})

	first, err := f.store.Lease(ctx, w, leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.Lease(ctx, w, leaseTTL)
	if err != nil {
		t.Fatalf("a free probe slot was not used while encode was full: %v", err)
	}
	if first.Operation == second.Operation {
		t.Errorf("both leases were %s; the classes did not limit independently", first.Operation)
	}
	// Now both pools are full.
	if _, err := f.store.Lease(ctx, w, leaseTTL); !errors.Is(err, ErrNoWork) {
		t.Errorf("a third lease was granted with every pool full: %v", err)
	}
}

// A GPU worker may do CPU work, but should prefer work that needs its GPU:
// spending an expensive node on a 360p encode is capacity wasted.
func TestGPUWorkerPrefersGPUWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	cpuTask := f.requireJob(t, "encode", Requirements{})
	gpuTask := f.requireJob(t, "encode", Requirements{GPU: true, Encoder: "hevc_nvenc"})

	gpu := f.newWorkerWith(t, WorkerFacts{
		Arch: "amd64", Operations: []string{"encode"},
		Encoders: []string{"libx264", "hevc_nvenc"}, HasGPU: true,
		MemoryBytes: 64 << 30, DiskFree: 500 << 30,
		SlotCapacity: map[string]int{"encode": 4},
	})

	got, err := f.store.Lease(ctx, gpu, leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != gpuTask.ID {
		t.Errorf("GPU worker took the CPU task %v, want the GPU task %v", got.TaskID, gpuTask.ID)
	}

	// With only CPU work left it still takes it: the penalty is a preference,
	// not a filter, so an idle GPU node is better than an idle queue.
	next, err := f.store.Lease(ctx, gpu, leaseTTL)
	if err != nil {
		t.Fatalf("GPU worker refused CPU work when nothing else was left: %v", err)
	}
	if next.TaskID != cpuTask.ID {
		t.Errorf("leased %v, want the remaining CPU task %v", next.TaskID, cpuTask.ID)
	}
}

// Waiting must eventually win, or a steadily busy tenant starves a quiet one.
func TestAgeDefeatsPriority(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	old, err := f.store.Create(ctx, f.tenant, f.version, f.pipever, "old-"+uuid.NewString(), 10,
		[]NewTask{{Key: "only", Operation: "probe"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx,
		`update tasks set queued_at = now() - interval '1 hour' where job_id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	// A brand new, much higher priority task.
	if _, err := f.store.Create(ctx, f.tenant, f.version, f.pipever, "new-"+uuid.NewString(), 100,
		[]NewTask{{Key: "only", Operation: "probe"}}); err != nil {
		t.Fatal(err)
	}

	got, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != old.ID {
		t.Error("a long-waiting task lost to a fresh higher-priority one; low priority work will starve")
	}
}

// The whole point of ADR-007: the decision is stored, so "why did this take an
// hour" is a query rather than an investigation.
func TestScheduleReasonIsStoredAndExplains(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.requireJob(t, "encode", Requirements{GPU: true, Encoder: "hevc_nvenc"})

	gpu := f.newWorkerWith(t, WorkerFacts{
		Arch: "amd64", Operations: []string{"encode"},
		Encoders: []string{"hevc_nvenc"}, HasGPU: true,
		MemoryBytes: 64 << 30, DiskFree: 500 << 30,
		SlotCapacity: map[string]int{"encode": 4},
	})

	a, err := f.store.Lease(ctx, gpu, leaseTTL)
	if err != nil {
		t.Fatal(err)
	}

	var reason string
	if err := f.pool.QueryRow(ctx,
		`select schedule_reason from task_attempts where id = $1`, a.AttemptID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{gpu.Name, "encode", "hevc_nvenc", "amd64",
		"GPU required and present", "slots free", "priority", "fair share", "score"} {
		if !strings.Contains(reason, want) {
			t.Errorf("explanation does not mention %q:\n%s", want, reason)
		}
	}
}

// A tenant cannot occupy the whole fleet: their quota caps concurrent work.
func TestTenantConcurrencyQuota(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx,
		`update tenants set quotas = '{"concurrent_tasks": 1}' where id = $1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	f.requireJob(t, "probe", Requirements{})
	f.requireJob(t, "probe", Requirements{})

	if _, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL); err != nil {
		t.Fatal(err)
	}
	// A different worker with free capacity still gets nothing: the limit is
	// the tenant's, not the worker's.
	if _, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL); !errors.Is(err, ErrNoWork) {
		t.Errorf("a tenant exceeded its concurrency quota: %v", err)
	}
}

// Between two tenants with equal priority, the one already using the fleet
// yields to the one that is not.
func TestFairShareBetweenTenants(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	busy := f.tenant
	quiet := f.newTenantWithAssets(t)

	// The busy tenant already has work running, and more waiting.
	f.requireJob(t, "probe", Requirements{})
	if _, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL); err != nil {
		t.Fatal(err)
	}
	f.requireJob(t, "probe", Requirements{})
	quietTask := f.requireJobFor(t, quiet, "probe")

	got, err := f.store.Lease(ctx, f.facts(f.newWorker(t), "probe"), leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != quietTask.ID {
		t.Errorf("the busy tenant %v was served again while %v waited", busy, quiet)
	}
}
