package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound  = errors.New("job not found")
	ErrNoTasks   = errors.New("a job must have at least one task")
	ErrBadDepend = errors.New("task depends on an unknown task")
)

type Job struct {
	ID             uuid.UUID  `json:"id"`
	AssetVersionID uuid.UUID  `json:"asset_version_id"`
	State          JobState   `json:"state"`
	Priority       int        `json:"priority"`
	FailureReason  *string    `json:"failure_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

type Task struct {
	ID            uuid.UUID   `json:"id"`
	JobID         uuid.UUID   `json:"job_id"`
	Operation     string      `json:"operation"`
	State         TaskState   `json:"state"`
	DependsOn     []uuid.UUID `json:"depends_on"`
	Priority      int         `json:"priority"`
	AttemptCount  int         `json:"attempt_count"`
	MaxAttempts   int         `json:"max_attempts"`
	FailureClass  *string     `json:"failure_class,omitempty"`
	FailureReason *string     `json:"failure_reason,omitempty"`
}

// NewTask describes a task before it exists. Dependencies are given by Key,
// a name local to this job, so a planner can build a graph without inventing
// identifiers.
type NewTask struct {
	Key       string
	Operation string
	// Spec is always JSON: typing it stops callers having to guess whether a
	// []byte here is a document or a value to be encoded.
	Spec         json.RawMessage
	Requirements any
	DependsOn    []string
	Priority     int
	MaxAttempts  int
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Create writes a job and its whole task graph in one transaction. A job with
// half a graph would be a job that can never complete.
//
// idempotencyKey makes retried submissions safe: the same key returns the
// existing job rather than queueing the work twice.
func (s *Store) Create(ctx context.Context, tenantID, versionID, pipelineVersionID uuid.UUID,
	idempotencyKey string, priority int, tasks []NewTask) (Job, error) {

	if len(tasks) == 0 {
		return Job{}, ErrNoTasks
	}

	ids := make(map[string]uuid.UUID, len(tasks))
	for _, t := range tasks {
		ids[t.Key] = uuid.Must(uuid.NewV7())
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := ids[dep]; !ok {
				return Job{}, fmt.Errorf("%w: %q depends on %q", ErrBadDepend, t.Key, dep)
			}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback(ctx)

	j := Job{ID: uuid.Must(uuid.NewV7()), AssetVersionID: versionID, State: JobPending, Priority: priority}
	err = tx.QueryRow(ctx, `
		insert into jobs (id, tenant_id, asset_version_id, pipeline_version_id,
		                  idempotency_key, priority, state)
		values ($1, $2, $3, $4, $5, $6, 'pending')
		on conflict (tenant_id, idempotency_key) do nothing
		returning created_at`,
		j.ID, tenantID, versionID, pipelineVersionID, nullable(idempotencyKey), priority,
	).Scan(&j.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		// The key already exists: return the job it created, so a retried
		// submission is a no-op rather than a duplicate pipeline run.
		return s.byIdempotencyKey(ctx, tenantID, idempotencyKey)
	}
	if err != nil {
		return Job{}, fmt.Errorf("create job: %w", err)
	}

	for _, t := range tasks {
		deps := make([]uuid.UUID, 0, len(t.DependsOn))
		for _, d := range t.DependsOn {
			deps = append(deps, ids[d])
		}

		// Only tasks with nothing to wait for are schedulable immediately.
		state, queuedAt := TaskPending, (*time.Time)(nil)
		if len(deps) == 0 {
			now := time.Now()
			state, queuedAt = TaskQueued, &now
		}

		spec := t.Spec
		if len(spec) == 0 {
			spec = json.RawMessage("{}")
		}
		reqs, err := toJSON(t.Requirements)
		if err != nil {
			return Job{}, err
		}

		maxAttempts := t.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		prio := t.Priority
		if prio <= 0 {
			prio = priority
		}

		if _, err := tx.Exec(ctx, `
			insert into tasks (id, tenant_id, job_id, operation, spec, requirements,
			                   depends_on, priority, state, max_attempts, queued_at)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			ids[t.Key], tenantID, j.ID, t.Operation, spec, reqs,
			deps, prio, state, maxAttempts, queuedAt); err != nil {
			return Job{}, fmt.Errorf("create task %q: %w", t.Key, err)
		}
	}

	return j, tx.Commit(ctx)
}

func (s *Store) Get(ctx context.Context, tenantID, jobID uuid.UUID) (Job, error) {
	var j Job
	err := s.pool.QueryRow(ctx, `
		select id, asset_version_id, state, priority, failure_reason,
		       created_at, started_at, finished_at
		  from jobs where id = $1 and tenant_id = $2`, jobID, tenantID,
	).Scan(&j.ID, &j.AssetVersionID, &j.State, &j.Priority, &j.FailureReason,
		&j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

func (s *Store) Tasks(ctx context.Context, tenantID, jobID uuid.UUID) ([]Task, error) {
	rows, err := s.pool.Query(ctx, `
		select id, job_id, operation, state, depends_on, priority,
		       attempt_count, max_attempts, failure_class, failure_reason
		  from tasks where job_id = $1 and tenant_id = $2 order by created_at`,
		jobID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.JobID, &t.Operation, &t.State, &t.DependsOn,
			&t.Priority, &t.AttemptCount, &t.MaxAttempts, &t.FailureClass, &t.FailureReason); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// Transition moves a task, rejecting anything the state machine does not allow.
// The row is locked for the whole check-and-set, so two concurrent reports
// cannot both believe they won.
func (s *Store) Transition(ctx context.Context, tenantID, taskID uuid.UUID, to TaskState) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	jobID, err := s.transitionTx(ctx, tx, tenantID, taskID, to, nil, "")
	if err != nil {
		return err
	}
	if err := s.reconcileJob(ctx, tx, tenantID, jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Fail applies the retry policy: a retryable class with budget left goes back
// to queued, anything else is final. Blanket retries burn a worker three times
// on media that will never decode.
func (s *Store) Fail(ctx context.Context, tenantID, taskID uuid.UUID,
	class FailureClass, reason string) (retrying bool, err error) {

	if !class.Valid() {
		class = ClassUnknown
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var (
		state        TaskState
		jobID        uuid.UUID
		attemptCount int
		maxAttempts  int
	)
	err = tx.QueryRow(ctx, `
		select state, job_id, attempt_count, max_attempts from tasks
		 where id = $1 and tenant_id = $2 for update`, taskID, tenantID,
	).Scan(&state, &jobID, &attemptCount, &maxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}

	retrying = class.Retryable() && attemptCount < maxAttempts
	to := TaskFailed
	if retrying {
		to = TaskQueued
	}
	// Legality is checked before the retry decision is applied, so a report
	// against an already-finished task is refused either way.
	if err := checkTask(state, to); err != nil {
		return false, err
	}

	if retrying {
		_, err = tx.Exec(ctx, `
			update tasks set state = 'queued', queued_at = now(),
			                 failure_class = $2, failure_reason = $3
			 where id = $1`, taskID, string(class), reason)
	} else {
		_, err = tx.Exec(ctx, `
			update tasks set state = 'failed', finished_at = now(),
			                 failure_class = $2, failure_reason = $3
			 where id = $1`, taskID, string(class), reason)
	}
	if err != nil {
		return false, err
	}

	if err := s.reconcileJob(ctx, tx, tenantID, jobID); err != nil {
		return false, err
	}
	return retrying, tx.Commit(ctx)
}

// Cancel stops a job and every task not already finished. Tasks that already
// succeeded are left alone: their outputs exist.
func (s *Store) Cancel(ctx context.Context, tenantID, jobID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var state JobState
	err = tx.QueryRow(ctx,
		`select state from jobs where id = $1 and tenant_id = $2 for update`,
		jobID, tenantID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Cancelling a finished job is a no-op, not an error: the caller's intent
	// is already satisfied.
	if state == JobSucceeded || state == JobFailed || state == JobCancelled {
		return nil
	}

	if _, err := tx.Exec(ctx, `
		update tasks set state = 'cancelled', finished_at = now()
		 where job_id = $1 and state in ('pending','queued','leased','running')`,
		jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		update jobs set state = 'cancelled', finished_at = now() where id = $1`,
		jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// transitionTx locks and moves one task, then releases any dependents the move
// unblocked. Returns the job id so the caller can reconcile it.
func (s *Store) transitionTx(ctx context.Context, tx pgx.Tx, tenantID, taskID uuid.UUID,
	to TaskState, class *FailureClass, reason string) (uuid.UUID, error) {

	var (
		state TaskState
		jobID uuid.UUID
	)
	err := tx.QueryRow(ctx,
		`select state, job_id from tasks where id = $1 and tenant_id = $2 for update`,
		taskID, tenantID).Scan(&state, &jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if err := checkTask(state, to); err != nil {
		return uuid.Nil, err
	}

	var finishedAt, queuedAt any
	if to.IsTerminal() {
		finishedAt = time.Now()
	}
	if to == TaskQueued {
		queuedAt = time.Now()
	}
	if _, err := tx.Exec(ctx, `
		update tasks set state = $2,
		                 finished_at = coalesce($3, finished_at),
		                 queued_at   = coalesce($4, queued_at)
		 where id = $1`, taskID, string(to), finishedAt, queuedAt); err != nil {
		return uuid.Nil, err
	}

	if to == TaskSucceeded {
		if err := s.releaseDependents(ctx, tx, jobID, taskID); err != nil {
			return uuid.Nil, err
		}
	}
	return jobID, nil
}

// releaseDependents queues pending tasks whose dependencies have now all
// succeeded. Done in the same transaction as the success that unblocked them,
// so the graph can never stall with everything satisfied but nothing queued.
func (s *Store) releaseDependents(ctx context.Context, tx pgx.Tx, jobID, doneID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		update tasks t set state = 'queued', queued_at = now()
		 where t.job_id = $1
		   and t.state = 'pending'
		   and $2 = any(t.depends_on)
		   and not exists (
		         select 1 from tasks d
		          where d.id = any(t.depends_on) and d.state <> 'succeeded')`,
		jobID, doneID)
	return err
}

// reconcileJob derives job state from its tasks. The job is never set
// independently, so it cannot disagree with the work it represents.
func (s *Store) reconcileJob(ctx context.Context, tx pgx.Tx, tenantID, jobID uuid.UUID) error {
	var total, succeeded, failed, cancelled, active int
	err := tx.QueryRow(ctx, `
		select count(*),
		       count(*) filter (where state = 'succeeded'),
		       count(*) filter (where state = 'failed'),
		       count(*) filter (where state = 'cancelled'),
		       count(*) filter (where state in ('leased','running'))
		  from tasks where job_id = $1`, jobID,
	).Scan(&total, &succeeded, &failed, &cancelled, &active)
	if err != nil {
		return err
	}

	var next JobState
	switch {
	case failed > 0:
		next = JobFailed
	case cancelled > 0:
		next = JobCancelled
	case succeeded == total:
		next = JobSucceeded
	case active > 0 || succeeded > 0:
		next = JobRunning
	default:
		next = JobPending
	}

	terminal := next == JobSucceeded || next == JobFailed || next == JobCancelled
	_, err = tx.Exec(ctx, `
		update jobs set state = $2,
		                started_at  = coalesce(started_at, case when $2 <> 'pending' then now() end),
		                finished_at = case when $3 then coalesce(finished_at, now()) else finished_at end
		 where id = $1 and tenant_id = $4`, jobID, string(next), terminal, tenantID)
	return err
}

// QueueCount is one bucket of the queue.
type QueueCount struct {
	Operation string
	State     string
	Count     int
}

// QueueStats reports the queue by operation and state, for gauges. It is
// aggregate and carries no tenant, because per-tenant series are unbounded.
func (s *Store) QueueStats(ctx context.Context) ([]QueueCount, error) {
	rows, err := s.pool.Query(ctx, `
		select operation, state, count(*)
		  from tasks
		 where state in ('pending','queued','leased','running')
		 group by operation, state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []QueueCount{}
	for rows.Next() {
		var c QueueCount
		if err := rows.Scan(&c.Operation, &c.State, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UnschedulableCount reports queued tasks that no online worker can run.
//
// A task nothing is capable of taking looks exactly like a busy queue from
// outside, and waiting for someone to notice is how a misconfigured fleet goes
// unseen for a day. It is worth its own number.
func (s *Store) UnschedulableCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		select count(*) from tasks t
		 where t.state = 'queued'
		   and not exists (
		     select 1 from workers w
		      where w.state = 'online'
		        and w.capabilities->'operations' ? t.operation
		        and (t.requirements->>'encoder' is null
		             or w.capabilities->'encoders' ? (t.requirements->>'encoder'))
		        and (t.requirements->>'arch' is null or w.arch = t.requirements->>'arch')
		        and ((t.requirements->>'gpu')::boolean is not true or w.gpu_model is not null)
		        and coalesce((t.requirements->>'memory_bytes')::bigint, 0) <= w.memory_bytes)`,
	).Scan(&n)
	return n, err
}

func (s *Store) byIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (Job, error) {
	var j Job
	err := s.pool.QueryRow(ctx, `
		select id, asset_version_id, state, priority, failure_reason,
		       created_at, started_at, finished_at
		  from jobs where tenant_id = $1 and idempotency_key = $2`, tenantID, key,
	).Scan(&j.ID, &j.AssetVersionID, &j.State, &j.Priority, &j.FailureReason,
		&j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

func toJSON(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
