package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/worker"
	"github.com/google/uuid"
)

// Outcome is what an executor produces: the result to report, plus any outputs
// to register as artifacts. Artifacts are registered before the task completes,
// so a set is never closed with an output missing from it.
type Outcome struct {
	Result    job.Result
	Artifacts []artifact.Registration
}

// Executor runs one kind of task. A failure it can describe is returned as an
// unsuccessful Result; only something that stopped it from trying at all is
// returned as an error.
type Executor func(ctx context.Context, bin string, spec json.RawMessage, onProgress func(Progress)) (Outcome, error)

type Config struct {
	ControlPlaneURL string
	BootstrapToken  string
	Name            string
	WorkDir         string
	FFmpegBin       string
	FFprobeBin      string
	SlotCapacity    map[string]int
	// PollInterval is how long to wait after an empty lease before asking
	// again. Work arriving is not signalled, so this bounds pickup latency.
	PollInterval time.Duration
}

type Agent struct {
	cfg      Config
	client   *Client
	log      *slog.Logger
	workerID uuid.UUID

	executors map[string]Executor

	mu      sync.Mutex
	running int
	slots   map[string]int
}

func New(cfg Config, log *slog.Logger) *Agent {
	a := &Agent{
		cfg:    cfg,
		client: NewClient(cfg.ControlPlaneURL),
		log:    log,
		slots:  map[string]int{},
	}
	a.executors = map[string]Executor{
		"probe": func(ctx context.Context, _ string, spec json.RawMessage, p func(Progress)) (Outcome, error) {
			res, err := probe(ctx, cfg.FFprobeBin, spec, p)
			return Outcome{Result: res}, err
		},
		"encode": func(ctx context.Context, bin string, spec json.RawMessage, p func(Progress)) (Outcome, error) {
			return encodeTask(ctx, cfg.WorkDir, bin, spec, p)
		},
	}
	return a
}

// Operations reports what this agent can actually execute. Declaring anything
// else would have the scheduler send work that fails on arrival.
func (a *Agent) Operations() []string {
	ops := make([]string, 0, len(a.executors))
	for op := range a.executors {
		ops = append(ops, op)
	}
	return ops
}

// Run registers, then heartbeats and polls until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	caps, err := Detect(ctx, a.cfg.FFmpegBin)
	if err != nil {
		return err
	}
	// Fail here rather than on the first task, and on this machine rather than
	// only on the one that happens to get the work.
	if err := CheckVersion(caps.FFmpegVersion); err != nil {
		return err
	}
	host := Host(a.cfg.WorkDir)

	slots := a.cfg.SlotCapacity
	if len(slots) == 0 {
		slots = DefaultSlots(host.CPUCores)
	}
	// Never advertise capacity for work we cannot run.
	a.slots = map[string]int{}
	for _, op := range a.Operations() {
		if n, ok := slots[op]; ok && n > 0 {
			a.slots[op] = n
		}
	}

	workerID, heartbeatEvery, err := a.client.Register(ctx, a.cfg.BootstrapToken, worker.Registration{
		Name: a.cfg.Name, Hostname: host.Hostname, OS: host.OS, Arch: host.Arch,
		CPUCores: host.CPUCores, MemoryBytes: host.MemoryBytes, DiskBytes: host.DiskBytes,
		FFmpegVersion: caps.FFmpegVersion, ProtocolVersion: worker.ProtocolVersion,
		Capabilities: worker.Capabilities{
			Operations: a.Operations(), Encoders: caps.Encoders,
			Decoders: caps.Decoders, Containers: caps.Containers,
		},
		SlotCapacity: a.slots,
	})
	if err != nil {
		return err
	}
	a.workerID = workerID
	a.log.Info("registered", "worker_id", workerID, "ffmpeg", caps.FFmpegVersion,
		"encoders", len(caps.Encoders), "slots", a.slots)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.heartbeatLoop(ctx, heartbeatEvery) }()
	go func() { defer wg.Done(); a.pollLoop(ctx) }()
	wg.Wait()
	return nil
}

func (a *Agent) heartbeatLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 10 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, err := a.client.Heartbeat(ctx, worker.Report{
				Healthy:       true,
				DiskFreeBytes: freeDisk(a.cfg.WorkDir),
				SlotsInUse:    a.slotsInUse(),
			})
			if err != nil {
				// Losing contact is survivable: our lease will expire and the
				// work will be redone elsewhere. Keep trying.
				a.log.Warn("heartbeat failed", "err", err)
				continue
			}
			if state == worker.StateDraining {
				a.log.Info("asked to drain; finishing current work and taking no more")
			}
		}
	}
}

func (a *Agent) pollLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		assignment, err := a.client.Lease(ctx)
		switch {
		case errors.Is(err, ErrNoWork):
			a.sleep(ctx, a.cfg.PollInterval)
			continue
		case err != nil:
			a.log.Warn("lease failed", "err", err)
			a.sleep(ctx, a.cfg.PollInterval)
			continue
		}

		a.execute(ctx, assignment)
	}
}

// execute runs one assignment to completion, keeping the lease alive and
// stopping immediately if the task is cancelled.
func (a *Agent) execute(ctx context.Context, as job.Assignment) {
	log := a.log.With("task_id", as.TaskID, "attempt", as.Attempt, "operation", as.Operation)

	exec, ok := a.executors[as.Operation]
	if !ok {
		// The scheduler should never send this. Report rather than drop it, or
		// the task would sit leased until its lease expires.
		log.Error("no executor for operation")
		a.report(ctx, as, failure(job.ClassPermanentConfig,
			"this worker has no executor for operation "+as.Operation))
		return
	}

	a.track(as.Operation, +1)
	defer a.track(as.Operation, -1)

	if err := a.client.Started(ctx, as.AttemptID); err != nil {
		log.Warn("could not report start", "err", err)
		if errors.Is(err, job.ErrStaleAttempt) {
			return
		}
	}

	// runCtx is cancelled by the progress loop when the control plane says the
	// task is cancelled, or when our lease is gone. Cancelling it kills the
	// media tool's whole process group.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var progress struct {
		sync.Mutex
		pct float32
	}
	done := make(chan struct{})
	go a.keepAlive(runCtx, as, done, cancel, func() float32 {
		progress.Lock()
		defer progress.Unlock()
		return progress.pct
	}, log)

	started := time.Now()
	total := sourceDuration(as.Spec)
	outcome, err := exec(runCtx, a.cfg.FFmpegBin, as.Spec, func(p Progress) {
		progress.Lock()
		progress.pct = Percent(p.OutTime, total)
		progress.Unlock()
	})
	close(done)

	result := outcome.Result
	if err != nil {
		if runCtx.Err() != nil && ctx.Err() == nil {
			// Cancelled by the control plane. It already knows; saying the task
			// failed would only start a retry of work nobody wants.
			log.Info("task cancelled; media tool stopped")
			return
		}
		result = failure(job.ClassUnknown, err.Error())
	}
	result.Metrics.WallSeconds = time.Since(started).Seconds()

	// Register outputs before reporting completion. A set closed with an
	// artifact missing looks complete and is not.
	if result.Success {
		for _, reg := range outcome.Artifacts {
			if err := a.client.RegisterArtifact(ctx, as.AttemptID, reg); err != nil {
				if errors.Is(err, job.ErrStaleAttempt) {
					log.Warn("lease lost before the output could be registered")
					return
				}
				log.Error("could not register artifact", "label", reg.Label, "err", err)
				result = failure(job.ClassTransient,
					fmt.Sprintf("could not register %s: %v", reg.Label, err))
				break
			}
		}
	}

	log.Info("task finished", "success", result.Success, "seconds", result.Metrics.WallSeconds)
	a.report(ctx, as, result)
}

// keepAlive renews the lease while work runs, and cancels the run the moment
// the control plane says to stop. Renewal has to be frequent enough that a slow
// encode is never reclaimed out from under us.
func (a *Agent) keepAlive(ctx context.Context, as job.Assignment, done <-chan struct{},
	cancel context.CancelFunc, pct func() float32, log *slog.Logger) {

	every := time.Duration(as.LeaseTTL) * time.Second / 3
	if every < time.Second {
		every = time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			cancelled, err := a.client.Progress(ctx, as.AttemptID, pct())
			if errors.Is(err, job.ErrStaleAttempt) {
				// Someone else owns this task now. Stop at once: finishing it
				// would waste the machine on output nobody will accept.
				log.Warn("lease lost; stopping work")
				cancel()
				return
			}
			if err != nil {
				log.Warn("progress report failed", "err", err)
				continue
			}
			if cancelled {
				log.Info("task cancelled by the control plane")
				cancel()
				return
			}
		}
	}
}

// report delivers the outcome, retrying briefly: losing the report after the
// work is done means redoing all of it.
func (a *Agent) report(ctx context.Context, as job.Assignment, result job.Result) {
	backoff := time.Second
	for attempt := 1; attempt <= 5; attempt++ {
		err := a.client.Complete(ctx, as.AttemptID, result)
		if err == nil {
			return
		}
		if errors.Is(err, job.ErrStaleAttempt) || ctx.Err() != nil {
			return
		}
		a.log.Warn("could not report completion", "err", err, "attempt", attempt)
		if !a.sleep(ctx, backoff) {
			return
		}
		backoff *= 2
	}
}

// sourceDuration lets progress be reported as a percentage. Media tools report
// position, not completion, so without the total there is nothing to divide by.
func sourceDuration(spec json.RawMessage) time.Duration {
	var s struct {
		DurationMS int64 `json:"duration_ms"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		return 0
	}
	return time.Duration(s.DurationMS) * time.Millisecond
}

func (a *Agent) track(operation string, delta int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running += delta
	if a.slots == nil {
		return
	}
}

func (a *Agent) slotsInUse() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]int{"total": a.running}
}

func (a *Agent) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
