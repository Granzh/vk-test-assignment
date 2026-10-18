package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

type fakeDays struct {
	days []db.CampaignDay
	err  error
}

func (f fakeDays) ListCampaignDaysByStatus(_ context.Context, status string) ([]db.CampaignDay, error) {
	if status != "pending" {
		panic("only pending days are drawn")
	}
	return f.days, f.err
}

type fakeDraw struct {
	calls   []time.Time
	admins  []int64
	forces  []bool
	err     error
	already bool
}

func (f *fakeDraw) Run(_ context.Context, adminID int64, date time.Time, force bool) (service.DrawResult, error) {
	f.calls = append(f.calls, date)
	f.admins = append(f.admins, adminID)
	f.forces = append(f.forces, force)
	return service.DrawResult{Already: f.already}, f.err
}

func day(n int) db.CampaignDay {
	return db.CampaignDay{Day: pgtype.Date{Time: time.Date(2026, 9, n, 0, 0, 0, 0, time.UTC), Valid: true}}
}

func newAuto(days []db.CampaignDay, draw *fakeDraw, today int, notes *[]string) *AutoDraw {
	clock := time.Date(2026, 9, today, 12, 0, 0, 0, time.UTC)
	return &AutoDraw{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Days:    fakeDays{days: days},
		Draw:    draw,
		Today:   func() time.Time { return time.Date(2026, 9, today, 0, 0, 0, 0, time.UTC) },
		Notify:  func(_ context.Context, s string) { *notes = append(*notes, s) },
		Preview: func(service.DrawResult) string { return "PREVIEW" },
		now:     func() time.Time { return clock },
	}
}

func TestTickDrawsOnlyFinishedDaysAsSystemWithoutForce(t *testing.T) {
	var notes []string
	draw := &fakeDraw{}
	a := newAuto([]db.CampaignDay{day(3), day(4), day(5)}, draw, 5, &notes)
	a.Tick(context.Background())

	if len(draw.calls) != 2 || draw.calls[0].Day() != 3 || draw.calls[1].Day() != 4 {
		t.Errorf("drew %v, want days 3 and 4 (day 5 is today)", draw.calls)
	}
	for i := range draw.calls {
		if draw.admins[i] != service.SystemAdminID || draw.forces[i] {
			t.Errorf("call %d: admin %d force %v", i, draw.admins[i], draw.forces[i])
		}
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "PREVIEW") || !strings.Contains(notes[0], "автоматически") {
		t.Errorf("notes = %q", notes)
	}
}

func TestTickSkipsDrawsAlreadyHeldByAnAdmin(t *testing.T) {
	var notes []string
	a := newAuto([]db.CampaignDay{day(3)}, &fakeDraw{already: true}, 5, &notes)
	a.Tick(context.Background())
	if len(notes) != 0 {
		t.Errorf("admins notified about a draw they held themselves: %q", notes)
	}
}

func TestTickFailureNotifiesOnceAndRetriesLater(t *testing.T) {
	var notes []string
	draw := &fakeDraw{err: errors.New("telegram down")}
	a := newAuto([]db.CampaignDay{day(3)}, draw, 5, &notes)
	clock := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }

	a.Tick(context.Background())
	a.Tick(context.Background()) // too soon: not retried
	if len(draw.calls) != 1 || len(notes) != 1 || !strings.Contains(notes[0], "telegram down") {
		t.Fatalf("calls = %d, notes = %q", len(draw.calls), notes)
	}

	clock = clock.Add(31 * time.Minute)
	a.Tick(context.Background()) // retried, still failing: no second notification
	if len(draw.calls) != 2 || len(notes) != 1 {
		t.Errorf("calls = %d, notes = %d; want 2 and 1", len(draw.calls), len(notes))
	}

	draw.err = nil
	clock = clock.Add(31 * time.Minute)
	a.Tick(context.Background())
	if len(draw.calls) != 3 || len(notes) != 2 || !strings.Contains(notes[1], "PREVIEW") {
		t.Errorf("after recovery: calls = %d, notes = %q", len(draw.calls), notes)
	}
}

func TestTickListError(t *testing.T) {
	var notes []string
	draw := &fakeDraw{}
	a := newAuto(nil, draw, 5, &notes)
	a.Days = fakeDays{err: errors.New("db down")}
	a.Tick(context.Background())
	if len(draw.calls) != 0 || len(notes) != 0 {
		t.Error("nothing must happen when the days cannot be listed")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	var notes []string
	a := newAuto(nil, &fakeDraw{}, 5, &notes)
	a.Interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
