// Package scheduler holds time-driven jobs. Its only job now is the optional
// automatic daily draw: it holds the draw of a finished day, but never
// confirms it. Winners are notified only after an admin runs /draw_confirm.
package scheduler

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"sync"
	"time"

	"chat_bot/internal/secret"
	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

// DayLister lists campaign days by status (implemented by storage.Store).
type DayLister interface {
	ListCampaignDaysByStatus(ctx context.Context, status string) ([]db.CampaignDay, error)
}

// DrawRunner holds a draw (implemented by *service.DailyDraw).
type DrawRunner interface {
	Run(ctx context.Context, adminID int64, date time.Time, force bool) (service.DrawResult, error)
}

// AutoDraw periodically draws every finished day that has not been drawn.
type AutoDraw struct {
	Log   *slog.Logger
	Days  DayLister
	Draw  DrawRunner
	Today func() time.Time // current campaign day as a date
	// Notify tells the admins something (HTML).
	Notify func(ctx context.Context, html string)
	// Preview renders a held draw for the admins.
	Preview func(service.DrawResult) string

	Interval   time.Duration // between passes; default 1 minute
	RetryAfter time.Duration // before retrying a day whose draw failed; default 30 minutes
	now        func() time.Time

	mu     sync.Mutex
	failed map[time.Time]time.Time // day -> last failed attempt
}

// Run ticks until ctx is done.
func (a *AutoDraw) Run(ctx context.Context) {
	interval := a.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		a.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one pass: it draws every pending day before today.
func (a *AutoDraw) Tick(ctx context.Context) {
	if a.now == nil {
		a.now = time.Now
	}
	retry := a.RetryAfter
	if retry <= 0 {
		retry = 30 * time.Minute
	}

	days, err := a.Days.ListCampaignDaysByStatus(ctx, "pending")
	if err != nil {
		a.Log.Error("auto draw: list pending days", "err", err)
		return
	}
	today := a.Today()
	for _, day := range days {
		date := day.Day.Time
		if !date.Before(today) {
			continue // the day is not over
		}
		if !a.mayAttempt(date, retry) {
			continue
		}

		res, err := a.Draw.Run(ctx, service.SystemAdminID, date, false)
		if err != nil {
			first := a.recordFailure(date)
			a.Log.Error("auto draw failed", "day", date.Format(time.DateOnly), "err", err)
			if first {
				a.Notify(ctx, fmt.Sprintf("⚠️ Автоматический розыгрыш дня %s не удался: %s\nПовторю попытку позже; "+
					"можно запустить вручную: <code>/draw_run %s</code>",
					date.Format(time.DateOnly), html.EscapeString(secret.Redact(err.Error())), date.Format(time.DateOnly)))
			}
			continue
		}
		a.clearFailure(date)
		if res.Already {
			continue // an admin got there first
		}
		a.Log.Info("auto draw held", "day", date.Format(time.DateOnly), "winners", len(res.Winners))
		a.Notify(ctx, "🤖 Розыгрыш проведён автоматически.\n\n"+a.Preview(res))
	}
}

func (a *AutoDraw) mayAttempt(date time.Time, retry time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	last, failed := a.failed[date]
	return !failed || a.now().Sub(last) >= retry
}

// recordFailure notes a failed attempt and reports whether it is the first for the day.
func (a *AutoDraw) recordFailure(date time.Time) (first bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed == nil {
		a.failed = map[time.Time]time.Time{}
	}
	_, seen := a.failed[date]
	a.failed[date] = a.now()
	return !seen
}

func (a *AutoDraw) clearFailure(date time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failed, date)
}
