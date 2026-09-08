package obs

import (
	"context"
	"log/slog"
	"time"
)

// QueueSource is what the collector needs from the domain. Declaring it here
// keeps obs from importing the job and worker packages, and keeps those
// packages from knowing metrics exist.
type QueueSource interface {
	QueueStats(ctx context.Context) ([]QueueBucket, error)
	UnschedulableCount(ctx context.Context) (int, error)
	WorkerStateCounts(ctx context.Context) (map[string]int, error)
}

type QueueBucket struct {
	Operation string
	State     string
	Count     int
}

// Collect refreshes the gauges until the context is cancelled.
//
// Gauges are sampled rather than maintained incrementally: an incremental
// counter drifts the first time a process restarts or a transaction rolls
// back, and a queue depth that is quietly wrong is worse than one that is a
// few seconds stale.
func Collect(ctx context.Context, m *Metrics, src QueueSource, every time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := collectOnce(ctx, m, src); err != nil {
				log.WarnContext(ctx, "metrics collection failed", "err", err)
			}
		}
	}
}

func collectOnce(ctx context.Context, m *Metrics, src QueueSource) error {
	buckets, err := src.QueueStats(ctx)
	if err != nil {
		return err
	}
	// Reset first: a bucket that has emptied would otherwise keep reporting
	// its last non-zero value forever.
	m.TasksByState.Reset()
	for _, b := range buckets {
		m.TasksByState.WithLabelValues(b.Operation, b.State).Set(float64(b.Count))
	}

	stuck, err := src.UnschedulableCount(ctx)
	if err != nil {
		return err
	}
	m.Unschedulable.Set(float64(stuck))

	workers, err := src.WorkerStateCounts(ctx)
	if err != nil {
		return err
	}
	m.Workers.Reset()
	for state, n := range workers {
		m.Workers.WithLabelValues(state).Set(float64(n))
	}
	return nil
}
