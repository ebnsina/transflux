package job

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Requirements is what a task needs from a worker. Everything is optional: a
// task that requires nothing runs anywhere, which is the common case.
type Requirements struct {
	Encoder     string `json:"encoder,omitempty"`
	Arch        string `json:"arch,omitempty"`
	GPU         bool   `json:"gpu,omitempty"`
	MemoryBytes int64  `json:"memory_bytes,omitempty"`
	DiskBytes   int64  `json:"disk_bytes,omitempty"`
	// SlotClass is the capacity pool this task draws from. Defaults to the
	// operation, so encodes of different codecs can be limited separately.
	SlotClass string `json:"slot_class,omitempty"`
}

// WorkerFacts is what the scheduler knows about the worker asking for work.
// The scheduler matches on declared capability, never on machine identity, so
// a heterogeneous fleet needs no central inventory.
type WorkerFacts struct {
	ID           uuid.UUID
	Name         string
	Arch         string
	Operations   []string
	Encoders     []string
	HasGPU       bool
	MemoryBytes  int64
	DiskFree     int64
	SlotCapacity map[string]int
}

// scoring weights. Tuned against measured throughput later; documented here so
// a change is a deliberate act rather than a guess.
const (
	// Waiting adds to priority rather than multiplying it, and is deliberately
	// unbounded: a capped multiplier cannot prevent starvation, because a large
	// enough priority gap always wins no matter how long the loser waits.
	// A task gains the equivalent of 100 priority points for every 10 minutes
	// it has been queued, so low-priority work eventually goes first.
	agePointsPerSecond = 100.0 / 600.0
	// A worker that already ran part of this job has the source on disk.
	localityBonus = 1.25
	// A GPU worker taking work that needs no GPU is capacity wasted; it may
	// still do it when there is nothing else, hence a penalty and not a filter.
	gpuIdlePenalty = 0.4
)

// Lease hands one task to a worker, or ErrNoWork.
//
// Hard constraints filter to what this worker can actually run; a weighted
// score then ranks what is left. SELECT ... FOR UPDATE SKIP LOCKED is what lets
// many workers poll at once without queueing behind each other.
//
// The decision's explanation is stored on the attempt, so "why did this take an
// hour" is a query rather than an investigation (ADR-007).
func (s *Store) Lease(ctx context.Context, w WorkerFacts, leaseTTL time.Duration) (Assignment, error) {
	if len(w.Operations) == 0 {
		return Assignment{}, ErrNoWork
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Assignment{}, err
	}
	defer tx.Rollback(ctx)

	// Serialise this worker's own leases so two concurrent polls cannot both
	// see the same free slot and both take it. Other workers are unaffected.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, lockKey(w.ID)); err != nil {
		return Assignment{}, err
	}

	free, inUse, err := s.freeSlotClasses(ctx, tx, w)
	if err != nil {
		return Assignment{}, err
	}
	if len(free) == 0 {
		// Every capacity pool is full. Not an error: come back later.
		return Assignment{}, ErrNoWork
	}

	var (
		a            Assignment
		attemptCount int
		priority     int
		ageSeconds   float64
		tenantActive int
		needsGPU     bool
		needsEncoder *string
		slotClass    string
		local        bool
		score        float64
	)
	err = tx.QueryRow(ctx, leaseQuery,
		w.ID, w.Operations, w.Arch, w.Encoders, w.HasGPU,
		w.MemoryBytes, w.DiskFree, free,
		agePointsPerSecond, localityBonus, gpuIdlePenalty,
	).Scan(&a.TaskID, &a.JobID, &a.TenantID, &a.Operation, &a.Spec, &attemptCount,
		&priority, &ageSeconds, &tenantActive, &needsGPU, &needsEncoder, &slotClass,
		&local, &score)
	if errors.Is(err, pgx.ErrNoRows) {
		return Assignment{}, ErrNoWork
	}
	if err != nil {
		return Assignment{}, err
	}

	// attempt_count is incremented at lease time, not at failure, so a worker
	// that dies without reporting still consumes budget. Otherwise a task that
	// kills every worker it touches would retry forever.
	a.Attempt = attemptCount + 1
	a.AttemptID = uuid.Must(uuid.NewV7())
	a.LeaseTTL = int(leaseTTL.Seconds())

	reason := explain(w, a, decision{
		priority: priority, ageSeconds: ageSeconds, tenantActive: tenantActive,
		needsGPU: needsGPU, encoder: needsEncoder, slotClass: slotClass,
		slotsInUse: inUse[slotClass], slotCapacity: w.SlotCapacity[slotClass],
		local: local, score: score,
	})

	if _, err := tx.Exec(ctx, `
		update tasks set state = 'leased', attempt_count = $2 where id = $1`,
		a.TaskID, a.Attempt); err != nil {
		return Assignment{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into task_attempts (id, tenant_id, task_id, attempt_number, worker_id,
		                           state, lease_expires_at, schedule_reason)
		values ($1,$2,$3,$4,$5,'leased', now() + $6::interval, $7)`,
		a.AttemptID, a.TenantID, a.TaskID, a.Attempt, w.ID,
		intervalOf(leaseTTL), reason); err != nil {
		return Assignment{}, err
	}

	if err := s.reconcileJob(ctx, tx, a.TenantID, a.JobID); err != nil {
		return Assignment{}, err
	}
	return a, tx.Commit(ctx)
}

// leaseQuery filters by hard constraint, then ranks. Constraints and
// preferences are deliberately different things: conflating them produces
// either infeasible placements or unexplainable ones.
const leaseQuery = `
with candidate as (
    select t.id, t.job_id, t.tenant_id, t.operation, t.spec, t.attempt_count, t.priority,
           extract(epoch from (now() - coalesce(t.queued_at, t.created_at))) as age_seconds,
           (t.requirements->>'gpu')::boolean is true                          as needs_gpu,
           t.requirements->>'encoder'                                         as needs_encoder,
           coalesce(t.requirements->>'slot_class', t.operation)               as slot_class,
           (select count(*) from task_attempts a2
              join tasks t2 on t2.id = a2.task_id
             where t2.tenant_id = t.tenant_id
               and a2.state in ('leased','running'))                          as tenant_active,
           exists (select 1 from task_attempts pa
                     join tasks pt on pt.id = pa.task_id
                    where pa.worker_id = $1 and pt.job_id = t.job_id
                      and pa.state = 'succeeded')                             as local
      from tasks t
     where t.state = 'queued'
       -- hard constraints
       and t.operation = any($2)
       and (t.requirements->>'arch' is null or t.requirements->>'arch' = $3)
       and (t.requirements->>'encoder' is null or t.requirements->>'encoder' = any($4))
       and ((t.requirements->>'gpu')::boolean is not true or $5)
       and coalesce((t.requirements->>'memory_bytes')::bigint, 0) <= $6
       and coalesce((t.requirements->>'disk_bytes')::bigint, 0)   <= $7
       and coalesce(t.requirements->>'slot_class', t.operation)   = any($8)
)
select c.id, c.job_id, c.tenant_id, c.operation, c.spec, c.attempt_count,
       c.priority, c.age_seconds, c.tenant_active, c.needs_gpu, c.needs_encoder,
       c.slot_class, c.local,
       (c.priority + c.age_seconds * $9)               -- waiting, so nothing starves
         * (1.0 / (1 + c.tenant_active))               -- fair share between tenants
         * (case when c.local then $10 else 1.0 end)   -- source already on disk
         * (case when $5 and not c.needs_gpu then $11 else 1.0 end)
                                                       -- do not spend a GPU on CPU work
         as score
  from candidate c
  join tasks t on t.id = c.id
     -- The state check is repeated here, outside the CTE, on purpose. Under
     -- READ COMMITTED, FOR UPDATE re-evaluates only this query level's
     -- conditions after taking the lock. Without it, a task another
     -- transaction just leased would be handed out a second time.
 where t.state = 'queued'
   -- tenant concurrency quota, applied last so it reads as the gate it is
   and c.tenant_active < coalesce(
         (select (quotas->>'concurrent_tasks')::int from tenants where id = c.tenant_id),
         2147483647)
 order by score desc, c.age_seconds desc
   for update of t skip locked
 limit 1`

// freeSlotClasses returns the capacity pools with room, and current usage.
// Usage is counted from live attempts rather than from the worker's own
// heartbeat, which can be seconds stale and is reported by the party with an
// interest in the answer.
func (s *Store) freeSlotClasses(ctx context.Context, tx pgx.Tx, w WorkerFacts) ([]string, map[string]int, error) {
	rows, err := tx.Query(ctx, `
		select coalesce(t.requirements->>'slot_class', t.operation) as slot_class, count(*)
		  from task_attempts a
		  join tasks t on t.id = a.task_id
		 where a.worker_id = $1 and a.state in ('leased','running')
		 group by 1`, w.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	inUse := map[string]int{}
	for rows.Next() {
		var class string
		var n int
		if err := rows.Scan(&class, &n); err != nil {
			return nil, nil, err
		}
		inUse[class] = n
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	free := make([]string, 0, len(w.SlotCapacity))
	for class, capacity := range w.SlotCapacity {
		if inUse[class] < capacity {
			free = append(free, class)
		}
	}
	return free, inUse, nil
}

type decision struct {
	priority     int
	ageSeconds   float64
	tenantActive int
	needsGPU     bool
	encoder      *string
	slotClass    string
	slotsInUse   int
	slotCapacity int
	local        bool
	score        float64
}

// explain renders why this worker got this task. Stored on the attempt and
// exposed by the API: an unexplainable scheduler is one you cannot debug.
func explain(w WorkerFacts, a Assignment, d decision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "worker %s <- %s (score %.1f)\n", w.Name, a.Operation, d.score)
	fmt.Fprintf(&b, "+ operation %s supported\n", a.Operation)
	if d.encoder != nil {
		fmt.Fprintf(&b, "+ encoder %s available\n", *d.encoder)
	}
	if w.Arch != "" {
		fmt.Fprintf(&b, "+ arch %s\n", w.Arch)
	}
	if d.needsGPU {
		fmt.Fprintf(&b, "+ GPU required and present\n")
	} else if w.HasGPU {
		fmt.Fprintf(&b, "- GPU worker on non-GPU work (penalty %.2f)\n", gpuIdlePenalty)
	}
	fmt.Fprintf(&b, "+ %d of %d %s slots free\n",
		d.slotCapacity-d.slotsInUse, d.slotCapacity, d.slotClass)
	fmt.Fprintf(&b, "+ priority %d, queued %.0fs\n", d.priority, d.ageSeconds)
	fmt.Fprintf(&b, "+ tenant has %d task(s) running (fair share)\n", d.tenantActive)
	if d.local {
		fmt.Fprintf(&b, "+ prior task of this job ran here (source likely local)\n")
	}
	return b.String()
}

// lockKey maps a worker to an advisory lock slot. Collisions between two
// workers only cost a little serialisation, never correctness.
func lockKey(id uuid.UUID) int64 {
	h := fnv.New64a()
	h.Write(id[:])
	return int64(h.Sum64() >> 1)
}
