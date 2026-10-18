package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// PrizeSweeper expires overdue prize claims (implemented by *service.FinalPrize).
type PrizeSweeper interface {
	SweepExpired(ctx context.Context) (int, error)
}

// PrizeDeadlines checks the response deadlines of the final draw winners and
// hands the prizes of those who did not answer to the next reserve.
type PrizeDeadlines struct {
	Log      *slog.Logger
	Prize    PrizeSweeper
	Interval time.Duration // between passes; default 1 minute
}

// Run ticks until ctx is done.
func (j *PrizeDeadlines) Run(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		j.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one pass.
func (j *PrizeDeadlines) Tick(ctx context.Context) {
	n, err := j.Prize.SweepExpired(ctx)
	if err != nil {
		j.Log.Error("prize deadlines", "err", err)
		return
	}
	if n > 0 {
		j.Log.Info("prize deadlines passed", "expired", n)
	}
}
