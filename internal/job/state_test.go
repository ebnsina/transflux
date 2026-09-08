package job

import (
	"strings"
	"testing"
)

// The expected machine is written out independently of the implementation, so
// an accidental edit to the transition map fails here rather than silently
// widening what a worker is allowed to do.
var wantTask = map[TaskState]map[TaskState]bool{
	TaskPending:   {TaskQueued: true, TaskCancelled: true},
	TaskQueued:    {TaskLeased: true, TaskCancelled: true},
	TaskLeased:    {TaskRunning: true, TaskQueued: true, TaskFailed: true, TaskCancelled: true},
	TaskRunning:   {TaskSucceeded: true, TaskFailed: true, TaskCancelled: true, TaskQueued: true},
	TaskFailed:    {TaskQueued: true},
	TaskSucceeded: {},
	TaskCancelled: {},
}

var allTaskStates = []TaskState{
	TaskPending, TaskQueued, TaskLeased, TaskRunning,
	TaskSucceeded, TaskFailed, TaskCancelled,
}

// Every ordered pair of states is checked, so illegal transitions are covered
// exhaustively rather than by the handful someone thought to write down.
func TestTaskTransitionMatrix(t *testing.T) {
	for _, from := range allTaskStates {
		for _, to := range allTaskStates {
			want := wantTask[from][to]
			if got := CanTransitionTask(from, to); got != want {
				t.Errorf("CanTransitionTask(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTaskSelfTransitionsAreIllegal(t *testing.T) {
	// A task moving to the state it is already in means something reported
	// twice; it must be an error, not an accepted no-op.
	for _, s := range allTaskStates {
		if CanTransitionTask(s, s) {
			t.Errorf("%s -> %s was allowed", s, s)
		}
	}
}

func TestTerminalTaskStatesAreFinal(t *testing.T) {
	for _, s := range []TaskState{TaskSucceeded, TaskCancelled} {
		if !s.IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
		for _, to := range allTaskStates {
			if CanTransitionTask(s, to) {
				t.Errorf("%s -> %s was allowed from a terminal state", s, to)
			}
		}
	}
	// Failed is terminal for the job's purposes but is the one state a retry
	// can leave, so it is deliberately not a dead end.
	if !TaskFailed.IsTerminal() {
		t.Error("failed should count as terminal")
	}
	if !CanTransitionTask(TaskFailed, TaskQueued) {
		t.Error("a failed task must be able to requeue for a retry")
	}
}

// Recovery from a lost worker is the property the whole lease design exists
// for, so it gets its own check rather than being buried in the matrix.
func TestExpiredLeaseCanRecover(t *testing.T) {
	if !CanTransitionTask(TaskLeased, TaskQueued) {
		t.Error("a lease that expired before the worker started must requeue")
	}
	if !CanTransitionTask(TaskRunning, TaskQueued) {
		t.Error("a lease that expired mid-run must requeue")
	}
}

var wantAttempt = map[AttemptState]map[AttemptState]bool{
	AttemptLeased:    {AttemptRunning: true, AttemptFailed: true, AttemptCancelled: true, AttemptExpired: true},
	AttemptRunning:   {AttemptSucceeded: true, AttemptFailed: true, AttemptCancelled: true, AttemptExpired: true},
	AttemptSucceeded: {},
	AttemptFailed:    {},
	AttemptCancelled: {},
	AttemptExpired:   {},
}

var allAttemptStates = []AttemptState{
	AttemptLeased, AttemptRunning, AttemptSucceeded,
	AttemptFailed, AttemptCancelled, AttemptExpired,
}

func TestAttemptTransitionMatrix(t *testing.T) {
	for _, from := range allAttemptStates {
		for _, to := range allAttemptStates {
			want := wantAttempt[from][to]
			if got := CanTransitionAttempt(from, to); got != want {
				t.Errorf("CanTransitionAttempt(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestAttemptTerminalStatesAreFinal(t *testing.T) {
	// An attempt is one execution: once it ends it never resumes. A retry is a
	// new attempt, which is what makes per-attempt accounting meaningful.
	for _, s := range []AttemptState{AttemptSucceeded, AttemptFailed, AttemptCancelled, AttemptExpired} {
		if !s.IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
		for _, to := range allAttemptStates {
			if CanTransitionAttempt(s, to) {
				t.Errorf("%s -> %s was allowed from a terminal state", s, to)
			}
		}
	}
	// An attempt cannot succeed without having started: a worker that reports
	// success straight from leased skipped telling us it began.
	if CanTransitionAttempt(AttemptLeased, AttemptSucceeded) {
		t.Error("leased -> succeeded was allowed without a start report")
	}
}

func TestIllegalTransitionErrorNamesBothStates(t *testing.T) {
	err := checkTask(TaskSucceeded, TaskRunning)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, want := range []string{"task", "succeeded", "running"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if err := checkTask(TaskQueued, TaskLeased); err != nil {
		t.Errorf("legal transition returned %v", err)
	}
}

func TestFailureClassification(t *testing.T) {
	retryable := []FailureClass{ClassTransient, ClassResource, ClassInfrastructure, ClassUnknown}
	permanent := []FailureClass{ClassPermanentInput, ClassPermanentConfig}

	for _, c := range retryable {
		if !c.Retryable() {
			t.Errorf("%s should be retryable", c)
		}
		if !c.Valid() {
			t.Errorf("%s should be valid", c)
		}
	}
	// Retrying these burns a worker repeatedly on media or configuration that
	// will never succeed.
	for _, c := range permanent {
		if c.Retryable() {
			t.Errorf("%s must not be retried", c)
		}
		if !c.Valid() {
			t.Errorf("%s should be valid", c)
		}
	}
	if FailureClass("made_up").Valid() || FailureClass("").Valid() {
		t.Error("an unrecognised failure class was accepted")
	}
}
