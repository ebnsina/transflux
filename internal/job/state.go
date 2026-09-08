// Package job owns jobs, tasks, task attempts and the state machine that
// governs them. Nothing outside this package writes tasks.state.
package job

import "fmt"

// TaskState is the lifecycle of one executable unit of work.
type TaskState string

const (
	// Dependencies are not yet satisfied.
	TaskPending TaskState = "pending"
	// Dependencies are satisfied; the task is schedulable.
	TaskQueued TaskState = "queued"
	// A worker holds a lease but has not reported starting.
	TaskLeased TaskState = "leased"
	// The worker reported starting; heartbeats renew the lease.
	TaskRunning TaskState = "running"

	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
	TaskCancelled TaskState = "cancelled"
)

// AttemptState is the lifecycle of one execution of a task. A task may have
// many attempts; only the attempt currently holding the lease may report.
type AttemptState string

const (
	AttemptLeased    AttemptState = "leased"
	AttemptRunning   AttemptState = "running"
	AttemptSucceeded AttemptState = "succeeded"
	AttemptFailed    AttemptState = "failed"
	AttemptCancelled AttemptState = "cancelled"
	// The lease elapsed without a heartbeat: the worker crashed, was
	// partitioned, or died. The task becomes recoverable.
	AttemptExpired AttemptState = "expired"
)

// JobState is derived from its tasks rather than set independently.
type JobState string

const (
	JobPending   JobState = "pending"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

// legalTaskTransitions is the whole task state machine. Anything absent here is
// a bug — a duplicate worker report, a stale attempt, or our own mistake — and
// must surface as an error rather than a silent no-op.
var legalTaskTransitions = map[TaskState][]TaskState{
	TaskPending: {TaskQueued, TaskCancelled},
	TaskQueued:  {TaskLeased, TaskCancelled},
	// Back to queued when a lease expires before the worker ever started.
	TaskLeased: {TaskRunning, TaskQueued, TaskFailed, TaskCancelled},
	// Back to queued when a lease expires mid-run; the work is redone.
	TaskRunning: {TaskSucceeded, TaskFailed, TaskCancelled, TaskQueued},
	// Retry. Whether a retry is *allowed* is policy (retryable class, budget
	// remaining); this only says the move is structurally legal.
	TaskFailed: {TaskQueued},

	TaskSucceeded: nil,
	TaskCancelled: nil,
}

var legalAttemptTransitions = map[AttemptState][]AttemptState{
	AttemptLeased:  {AttemptRunning, AttemptFailed, AttemptCancelled, AttemptExpired},
	AttemptRunning: {AttemptSucceeded, AttemptFailed, AttemptCancelled, AttemptExpired},

	AttemptSucceeded: nil,
	AttemptFailed:    nil,
	AttemptCancelled: nil,
	AttemptExpired:   nil,
}

// ErrIllegalTransition names both states, because the useful question when this
// fires is always "what did we think the task was doing".
type ErrIllegalTransition struct {
	Kind string
	From string
	To   string
}

func (e ErrIllegalTransition) Error() string {
	return fmt.Sprintf("illegal %s transition: %s -> %s", e.Kind, e.From, e.To)
}

func (s TaskState) IsTerminal() bool {
	return s == TaskSucceeded || s == TaskCancelled || s == TaskFailed
}

func (s AttemptState) IsTerminal() bool {
	return len(legalAttemptTransitions[s]) == 0
}

// CanTransitionTask reports whether the move is structurally legal. A
// transition to the same state is not: it means something reported twice.
func CanTransitionTask(from, to TaskState) bool {
	for _, s := range legalTaskTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

func CanTransitionAttempt(from, to AttemptState) bool {
	for _, s := range legalAttemptTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

func checkTask(from, to TaskState) error {
	if !CanTransitionTask(from, to) {
		return ErrIllegalTransition{Kind: "task", From: string(from), To: string(to)}
	}
	return nil
}

func checkAttempt(from, to AttemptState) error {
	if !CanTransitionAttempt(from, to) {
		return ErrIllegalTransition{Kind: "attempt", From: string(from), To: string(to)}
	}
	return nil
}

// ── failure classification ────────────────────────────────────────────────

// FailureClass decides whether a failure is worth retrying. Blanket retries
// burn a worker three times on media that will never decode.
type FailureClass string

const (
	// Retry: the work is fine, the environment was not.
	ClassTransient FailureClass = "transient"
	// Retry, possibly somewhere with more of whatever ran out.
	ClassResource FailureClass = "resource"
	// Do not retry: the source media is unusable.
	ClassPermanentInput FailureClass = "permanent_input"
	// Do not retry: the requested configuration is invalid or unsupported.
	ClassPermanentConfig FailureClass = "permanent_config"
	// Retry: our own dependency was unavailable.
	ClassInfrastructure FailureClass = "infrastructure"
	// Retry once, and treat its appearance as a bug to classify.
	ClassUnknown FailureClass = "unknown"
)

func (c FailureClass) Retryable() bool {
	switch c {
	case ClassTransient, ClassResource, ClassInfrastructure, ClassUnknown:
		return true
	default:
		return false
	}
}

func (c FailureClass) Valid() bool {
	switch c {
	case ClassTransient, ClassResource, ClassPermanentInput,
		ClassPermanentConfig, ClassInfrastructure, ClassUnknown:
		return true
	default:
		return false
	}
}
