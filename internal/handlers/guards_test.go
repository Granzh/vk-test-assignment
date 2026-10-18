package handlers

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"
)

// updCtx is a stub context with a chosen update id.
type updCtx struct {
	*stubCtx
	id int
}

func (u *updCtx) Update() tele.Update { return tele.Update{ID: u.id} }

func newUpdCtx(id int) *updCtx { return &updCtx{stubCtx: newStubCtx(&tele.User{ID: 7}), id: id} }

type countingMetrics struct{ dups, panics atomic.Int32 }

func (m *countingMetrics) Duplicate() { m.dups.Add(1) }
func (m *countingMetrics) Panic()     { m.panics.Add(1) }

func TestDedupFirstSeesEachIDOnceUntilTheTTLEnds(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	d := newDedup()
	d.now = func() time.Time { return now }

	if !d.First(10) || d.First(10) || d.First(10) {
		t.Fatal("an update id must be accepted exactly once")
	}
	if !d.First(11) {
		t.Error("another id is a different update")
	}
	first, second := d.First(0), d.First(0)
	if !first || !second {
		t.Error("id 0 is not a Telegram update and is never a duplicate")
	}
	now = now.Add(dedupTTL + time.Second)
	if !d.First(10) {
		t.Error("after the TTL the id is forgotten")
	}
}

func TestDedupPrunesWhenFull(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	d := newDedup()
	d.now = func() time.Time { return now }
	for i := 1; i <= maxDedupEntries; i++ {
		d.seen[i] = now.Add(-2 * dedupTTL)
	}
	if !d.First(maxDedupEntries + 1) {
		t.Fatal("a new id must be accepted")
	}
	if len(d.seen) > 2 {
		t.Errorf("%d entries remain, the expired ones must be dropped", len(d.seen))
	}
}

func TestDedupMWSkipsRedeliveredUpdates(t *testing.T) {
	m := &countingMetrics{}
	var ran atomic.Int32
	h := dedupMW(newDedup(), discard, m)(func(tele.Context) error { ran.Add(1); return nil })

	for i := 0; i < 3; i++ { // the same update delivered three times
		if err := h(newUpdCtx(500)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h(newUpdCtx(501)); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 2 || m.dups.Load() != 2 {
		t.Errorf("handler ran %d times, duplicates counted %d; want 2 and 2", ran.Load(), m.dups.Load())
	}
}

func TestDedupMWConcurrentRedeliveryRunsOnce(t *testing.T) {
	var ran atomic.Int32
	h := dedupMW(newDedup(), discard, &countingMetrics{})(func(tele.Context) error { ran.Add(1); return nil })
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h(newUpdCtx(900))
		}()
	}
	wg.Wait()
	if ran.Load() != 1 {
		t.Errorf("handler ran %d times for one update", ran.Load())
	}
}

func TestLimitMWBoundsConcurrency(t *testing.T) {
	const limit, total = 3, 20
	var running, peak atomic.Int32
	release := make(chan struct{})
	h := limitMW(limit, 5*time.Second)(func(tele.Context) error {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return nil
	})

	var wg sync.WaitGroup
	errs := make([]error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = h(newUpdCtx(i + 1))
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for running.Load() < limit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond) // let the others queue up
	if running.Load() != limit {
		t.Errorf("running = %d, want exactly %d", running.Load(), limit)
	}
	close(release)
	wg.Wait()

	if peak.Load() > limit {
		t.Errorf("peak concurrency %d exceeded the limit %d", peak.Load(), limit)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("update %d: %v (waiting updates must be served, not dropped)", i, err)
		}
	}
}

func TestLimitMWDropsUpdatesThatWaitTooLong(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	h := limitMW(1, 30*time.Millisecond)(func(tele.Context) error {
		close(started)
		<-release
		return nil
	})
	go func() { _ = h(newUpdCtx(1)) }()
	<-started

	if err := h(newUpdCtx(2)); !errors.Is(err, ErrOverloaded) {
		t.Errorf("err = %v, want ErrOverloaded", err)
	}
	close(release)
}

func TestLimitMWZeroMeansUnlimited(t *testing.T) {
	var ran atomic.Int32
	h := limitMW(0, time.Millisecond)(func(tele.Context) error { ran.Add(1); return nil })
	for i := 0; i < 5; i++ {
		_ = h(newUpdCtx(i + 1))
	}
	if ran.Load() != 5 {
		t.Errorf("ran %d", ran.Load())
	}
}

func TestRecoverMWCountsPanics(t *testing.T) {
	m := &countingMetrics{}
	h := recoverMW(discard, m)(func(tele.Context) error { panic("boom") })
	c := newStubCtx(nil)
	if err := h(c); err == nil {
		t.Error("a panic must come back as an error")
	}
	if m.panics.Load() != 1 {
		t.Errorf("panics counted = %d", m.panics.Load())
	}
}
