package worker

import (
	"context"
	"log/slog"
	"time"
)

// Sweep marks workers offline once their heartbeats stop, until the context is
// cancelled. It is deliberately separate from lease expiry: losing sight of a
// worker and reclaiming the work it held are different decisions, and conflating
// them would abandon work that is still running behind a slow network.
func Sweep(ctx context.Context, s *Store, every, staleAfter time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.MarkStaleOffline(ctx, staleAfter)
			if err != nil {
				// A failed sweep is not fatal: the next tick retries, and the
				// stale worker simply stays listed for another interval.
				log.ErrorContext(ctx, "worker sweep failed", "err", err)
				continue
			}
			if n > 0 {
				log.WarnContext(ctx, "workers marked offline", "count", n, "stale_after", staleAfter)
			}
		}
	}
}
