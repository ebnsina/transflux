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

// Start records that execution actually began. Until this, the task is only
// leased: a worker that crashes between winning and starting is distinguishable
// from one that crashed mid-encode.
func (s *Store) Start(ctx context.Context, attemptID, workerID uuid.UUID, leaseTTL time.Duration) error {
	return s.withAttempt(ctx, attemptID, workerID, func(tx pgx.Tx, at attemptRow) error {
		// Cancelled between the lease and the start report. Close the attempt
		// rather than erroring: the worker did nothing wrong, and it will learn
		// to stop from its next progress call.
		if at.taskState == TaskCancelled {
			return closeAttemptCancelled(ctx, tx, attemptID)
		}
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
		// The task was cancelled while this worker was still running it. Its
		// result is discarded, but reporting is not an error — a worker that
		// finished just before the cancel landed did nothing wrong, and
		// failing its call would only make it retry.
		if at.taskState == TaskCancelled {
			return closeAttemptCancelled(ctx, tx, attemptID)
		}

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

// closeAttemptCancelled ends an attempt whose task was cancelled underneath it.
func closeAttemptCancelled(ctx context.Context, tx pgx.Tx, attemptID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		update task_attempts set state = 'cancelled', finished_at = now(),
		       failure_reason = 'the task was cancelled while this attempt was running'
		 where id = $1`, attemptID)
	return err
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
