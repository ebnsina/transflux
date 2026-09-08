package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// ExpiredLease records one reclaimed task, for logging and metrics.
type ExpiredLease struct {
	TaskID    uuid.UUID
	AttemptID uuid.UUID
	WorkerID  uuid.UUID
	Attempt   int
	Requeued  bool
}

// ExpireLeases reclaims work whose worker stopped reporting.
//
// This is the mechanism that makes a worker disposable: a crash, a kill -9, a
// network partition or a vanished spot instance all look the same from here,
// and all end with the task available to somebody else. It is deliberately
// separate from marking a worker offline — a worker can be unreachable to us
// while its work is still running, and reclaiming that work early would run it
// twice.
func (s *Store) ExpireLeases(ctx context.Context, limit int) ([]ExpiredLease, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// SKIP LOCKED so a sweep never blocks a worker mid-report, and so two
	// control-plane instances can sweep at once without fighting.
	rows, err := tx.Query(ctx, `
		select a.id, a.task_id, a.worker_id, a.attempt_number, a.tenant_id,
		       t.state, t.job_id, t.attempt_count, t.max_attempts
		  from task_attempts a
		  join tasks t on t.id = a.task_id
		 where a.state in ('leased','running')
		   and a.lease_expires_at < now()
		 order by a.lease_expires_at
		   for update of a skip locked
		 limit $1`, limit)
	if err != nil {
		return nil, err
	}

	type row struct {
		attemptID, taskID, workerID, tenantID, jobID uuid.UUID
		attempt, attemptCount, maxAttempts           int
		taskState                                    TaskState
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.attemptID, &r.taskID, &r.workerID, &r.attempt, &r.tenantID,
			&r.taskState, &r.jobID, &r.attemptCount, &r.maxAttempts); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []ExpiredLease
	for _, r := range found {
		if _, err := tx.Exec(ctx, `
			update task_attempts
			   set state = 'expired', finished_at = now(),
			       failure_class = 'transient',
			       failure_reason = 'lease expired: the worker stopped reporting'
			 where id = $1`, r.attemptID); err != nil {
			return nil, err
		}

		// A cancelled task keeps its cancelled state; the lease lapsing is not
		// news, and requeueing it would restart work someone asked us to stop.
		if r.taskState == TaskCancelled || r.taskState.IsTerminal() {
			out = append(out, ExpiredLease{TaskID: r.taskID, AttemptID: r.attemptID,
				WorkerID: r.workerID, Attempt: r.attempt})
			continue
		}

		// Losing a worker is transient by definition, so the only question is
		// whether any budget is left. Budget was consumed at lease time, so a
		// worker that dies silently still counts.
		requeue := r.attemptCount < r.maxAttempts
		to := TaskFailed
		if requeue {
			to = TaskQueued
		}
		if err := checkTask(r.taskState, to); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			update tasks
			   set state = $2,
			       queued_at   = case when $2 = 'queued' then now() else queued_at end,
			       finished_at = case when $2 = 'failed' then now() else finished_at end,
			       failure_class = 'transient',
			       failure_reason = case when $2 = 'failed'
			            then 'lease expired and the retry budget is exhausted'
			            else failure_reason end
			 where id = $1`, r.taskID, string(to)); err != nil {
			return nil, err
		}
		if err := s.reconcileJob(ctx, tx, r.tenantID, r.jobID); err != nil {
			return nil, err
		}

		out = append(out, ExpiredLease{TaskID: r.taskID, AttemptID: r.attemptID,
			WorkerID: r.workerID, Attempt: r.attempt, Requeued: requeue})
	}

	return out, tx.Commit(ctx)
}

// SweepLeases reclaims expired leases until the context is cancelled.
func SweepLeases(ctx context.Context, s *Store, every time.Duration, log *slog.Logger) {
	const batch = 100

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired, err := s.ExpireLeases(ctx, batch)
			if err != nil {
				// The next tick retries. A failed sweep delays recovery; it
				// does not corrupt anything.
				log.ErrorContext(ctx, "lease sweep failed", "err", err)
				continue
			}
			for _, e := range expired {
				log.WarnContext(ctx, "lease expired",
					"task_id", e.TaskID, "attempt", e.Attempt,
					"worker_id", e.WorkerID, "requeued", e.Requeued)
			}
		}
	}
}
