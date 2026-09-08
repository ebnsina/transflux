package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrNoWork is not a failure: an idle fleet asking for work is the normal
	// case, and a worker must be able to tell it apart from an error.
	ErrNoWork = errors.New("no work available")
	// ErrStaleAttempt means the caller no longer holds the lease — its lease
	// expired and the task was given to someone else. Its reports must be
	// refused, or two workers would both write results for one task.
	ErrStaleAttempt = errors.New("attempt no longer holds the lease")
)

// Assignment is what a worker receives when it wins a lease. It carries
// everything needed to execute, so the worker never queries the domain.
type Assignment struct {
	TaskID    uuid.UUID       `json:"task_id"`
	AttemptID uuid.UUID       `json:"attempt_id"`
	JobID     uuid.UUID       `json:"job_id"`
	TenantID  uuid.UUID       `json:"tenant_id"`
	Operation string          `json:"operation"`
	Spec      json.RawMessage `json:"spec"`
	Attempt   int             `json:"attempt_number"`
	LeaseTTL  int             `json:"lease_ttl_seconds"`
}

// Result is what a worker reports when a task finishes.
type Result struct {
	Success       bool         `json:"success"`
	FailureClass  FailureClass `json:"failure_class,omitempty"`
	FailureReason string       `json:"failure_reason,omitempty"`
	Metrics       Metrics      `json:"metrics"`
	LogStorageKey string       `json:"log_storage_key,omitempty"`
}

// Metrics is per-attempt resource accounting, which is what makes
// cost-per-asset answerable later. Not billing.
type Metrics struct {
	CPUSeconds      float64 `json:"cpu_seconds"`
	WallSeconds     float64 `json:"wall_seconds"`
	PeakMemoryBytes int64   `json:"peak_memory_bytes"`
	GPUSeconds      float64 `json:"gpu_seconds"`
	BytesIn         int64   `json:"bytes_in"`
	BytesOut        int64   `json:"bytes_out"`
}

// Lease hands one task to a worker, or ErrNoWork.
//
// SELECT ... FOR UPDATE SKIP LOCKED is what lets many workers ask at once
// without queueing behind each other: each takes a different row rather than
// contending for the same one.
//
// Capability matching here is only the operation. The full constraint set and
// scoring arrive with the scheduler; this is the transaction they will use.
func (s *Store) Lease(ctx context.Context, workerID uuid.UUID, operations []string,
	leaseTTL time.Duration, reason string) (Assignment, error) {

	if len(operations) == 0 {
		return Assignment{}, ErrNoWork
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Assignment{}, err
	}
	defer tx.Rollback(ctx)

	var (
		a            Assignment
		attemptCount int
	)
	err = tx.QueryRow(ctx, `
		select t.id, t.job_id, t.tenant_id, t.operation, t.spec, t.attempt_count
		  from tasks t
		 where t.state = 'queued'
		   and t.operation = any($1)
		 order by t.priority desc, t.queued_at
		   for update skip locked
		 limit 1`, operations,
	).Scan(&a.TaskID, &a.JobID, &a.TenantID, &a.Operation, &a.Spec, &attemptCount)
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

	if _, err := tx.Exec(ctx, `
		update tasks set state = 'leased', attempt_count = $2 where id = $1`,
		a.TaskID, a.Attempt); err != nil {
		return Assignment{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into task_attempts (id, tenant_id, task_id, attempt_number, worker_id,
		                           state, lease_expires_at, schedule_reason)
		values ($1,$2,$3,$4,$5,'leased', now() + $6::interval, $7)`,
		a.AttemptID, a.TenantID, a.TaskID, a.Attempt, workerID,
		intervalOf(leaseTTL), reason); err != nil {
		return Assignment{}, err
	}

	if err := s.reconcileJob(ctx, tx, a.TenantID, a.JobID); err != nil {
		return Assignment{}, err
	}
	return a, tx.Commit(ctx)
}

// Start records that execution actually began. Until this, the task is only
// leased: a worker that crashes between winning and starting is distinguishable
// from one that crashed mid-encode.
func (s *Store) Start(ctx context.Context, attemptID, workerID uuid.UUID, leaseTTL time.Duration) error {
	return s.withAttempt(ctx, attemptID, workerID, func(tx pgx.Tx, at attemptRow) error {
		if err := checkAttempt(at.state, AttemptRunning); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update task_attempts set state = 'running', started_at = now(),
			                         lease_expires_at = now() + $2::interval
			 where id = $1`, attemptID, intervalOf(leaseTTL)); err != nil {
			return err
		}
		_, err := s.transitionTx(ctx, tx, at.tenantID, at.taskID, TaskRunning, nil, "")
		return err
	})
}

// Progress renews the lease and reports how far along the work is. It returns
// whether the task has been cancelled, which is how a cancel reaches a worker
// we have no route to (ADR-006). Worst-case latency is one heartbeat interval.
func (s *Store) Progress(ctx context.Context, attemptID, workerID uuid.UUID,
	pct float32, leaseTTL time.Duration) (cancelled bool, err error) {

	err = s.withAttempt(ctx, attemptID, workerID, func(tx pgx.Tx, at attemptRow) error {
		// A cancelled task must not have its lease renewed: the worker is being
		// told to stop, not to keep going.
		if at.taskState == TaskCancelled {
			cancelled = true
			return nil
		}
		_, err := tx.Exec(ctx, `
			update task_attempts set progress_pct = $2,
			                         lease_expires_at = now() + $3::interval
			 where id = $1`, attemptID, pct, intervalOf(leaseTTL))
		return err
	})
	return cancelled, err
}

// Complete finishes an attempt and applies the retry policy to its task.
func (s *Store) Complete(ctx context.Context, attemptID, workerID uuid.UUID, res Result) (retrying bool, err error) {
	err = s.withAttempt(ctx, attemptID, workerID, func(tx pgx.Tx, at attemptRow) error {
		attemptTo := AttemptSucceeded
		if !res.Success {
			attemptTo = AttemptFailed
		}
		if err := checkAttempt(at.state, attemptTo); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			update task_attempts set state = $2, finished_at = now(), progress_pct = $3,
			       cpu_seconds = $4, wall_seconds = $5, peak_memory_bytes = $6,
			       gpu_seconds = $7, bytes_in = $8, bytes_out = $9,
			       failure_class = $10, failure_reason = $11, log_storage_key = $12
			 where id = $1`,
			attemptID, string(attemptTo), completionPct(res.Success),
			res.Metrics.CPUSeconds, res.Metrics.WallSeconds, res.Metrics.PeakMemoryBytes,
			res.Metrics.GPUSeconds, res.Metrics.BytesIn, res.Metrics.BytesOut,
			nullable(string(res.FailureClass)), nullable(res.FailureReason),
			nullable(res.LogStorageKey)); err != nil {
			return err
		}

		if res.Success {
			if _, err := s.transitionTx(ctx, tx, at.tenantID, at.taskID, TaskSucceeded, nil, ""); err != nil {
				return err
			}
			// Reconcile on the success path too: without this a job whose last
			// task just succeeded stays "running" forever.
			return s.reconcileJob(ctx, tx, at.tenantID, at.jobID)
		}

		class := res.FailureClass
		if !class.Valid() {
			class = ClassUnknown
		}
		var attempts, maxAttempts int
		if err := tx.QueryRow(ctx,
			`select attempt_count, max_attempts from tasks where id = $1 for update`,
			at.taskID).Scan(&attempts, &maxAttempts); err != nil {
			return err
		}
		retrying = class.Retryable() && attempts < maxAttempts

		to := TaskFailed
		if retrying {
			to = TaskQueued
		}
		if err := checkTask(at.taskState, to); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update tasks set state = $2,
			                 queued_at   = case when $2 = 'queued' then now() else queued_at end,
			                 finished_at = case when $2 = 'failed' then now() else finished_at end,
			                 failure_class = $3, failure_reason = $4
			 where id = $1`, at.taskID, string(to), string(class), res.FailureReason); err != nil {
			return err
		}
		return s.reconcileJob(ctx, tx, at.tenantID, at.jobID)
	})
	return retrying, err
}

type attemptRow struct {
	tenantID  uuid.UUID
	taskID    uuid.UUID
	jobID     uuid.UUID
	state     AttemptState
	taskState TaskState
}

// withAttempt locks an attempt and refuses anyone who is not its owner or whose
// lease has already been taken away. This is what stops a worker that came back
// from a network partition writing over a result someone else produced.
func (s *Store) withAttempt(ctx context.Context, attemptID, workerID uuid.UUID,
	fn func(pgx.Tx, attemptRow) error) error {

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var at attemptRow
	err = tx.QueryRow(ctx, `
		select a.tenant_id, a.task_id, t.job_id, a.state, t.state
		  from task_attempts a
		  join tasks t on t.id = a.task_id
		 where a.id = $1 and a.worker_id = $2
		   for update of a`, attemptID, workerID,
	).Scan(&at.tenantID, &at.taskID, &at.jobID, &at.state, &at.taskState)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the attempt does not exist, or it belongs to another worker.
		// Both are the same answer to the caller.
		return ErrStaleAttempt
	}
	if err != nil {
		return err
	}
	// An attempt that already ended holds nothing. Expired is the common case:
	// the lease lapsed and the task was reassigned while this worker was away.
	if at.state.IsTerminal() {
		return ErrStaleAttempt
	}

	if err := fn(tx, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func completionPct(success bool) *float32 {
	if !success {
		return nil
	}
	full := float32(100)
	return &full
}

func intervalOf(d time.Duration) string {
	return fmt.Sprintf("%d milliseconds", d.Milliseconds())
}
