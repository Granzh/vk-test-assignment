package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeSweeper struct {
	calls int
	n     int
	err   error
}

func (f *fakeSweeper) SweepExpired(context.Context) (int, error) {
	f.calls++
	return f.n, f.err
}

func TestPrizeDeadlinesTick(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, s := range []*fakeSweeper{{n: 2}, {err: errors.New("db down")}, {}} {
		(&PrizeDeadlines{Log: log, Prize: s}).Tick(context.Background())
		if s.calls != 1 {
			t.Errorf("sweeper called %d times", s.calls)
		}
	}
}

func TestPrizeDeadlinesRunStopsOnCancel(t *testing.T) {
	s := &fakeSweeper{}
	j := &PrizeDeadlines{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Prize: s, Interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if s.calls < 2 {
		t.Errorf("only %d passes in 20ms at 1ms interval", s.calls)
	}
}
