package main

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// jobs runs the background jobs (schedulers, the broadcast worker, the metrics
// server). A job that panics is logged and restarted, so one bug cannot silently
// stop broadcasts or deadline checks; Wait lets shutdown finish them before
// the database pool is closed.
type jobs struct {
	wg    sync.WaitGroup
	log   *slog.Logger
	delay time.Duration // pause before restarting a job after a panic
}

func newJobs(log *slog.Logger) *jobs { return &jobs{log: log, delay: 5 * time.Second} }

// Go starts fn in the background. fn should return when ctx is done.
func (j *jobs) Go(ctx context.Context, name string, fn func(ctx context.Context)) {
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		for {
			if !j.runOnce(ctx, name, fn) || ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(j.delay):
			}
		}
	}()
}

// runOnce runs fn and reports whether it panicked.
func (j *jobs) runOnce(ctx context.Context, name string, fn func(ctx context.Context)) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			j.log.Error("background job panicked, restarting", "job", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn(ctx)
	return false
}

// Wait blocks until every job has stopped.
func (j *jobs) Wait() { j.wg.Wait() }
