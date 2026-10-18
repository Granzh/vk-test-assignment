package subscription

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"
)

type fakeLookup struct {
	mu      sync.Mutex
	member  *tele.ChatMember
	errs    []error // consumed one per call, then member is returned
	calls   int
	chat    string
	user    string
	started chan struct{}
	release chan struct{}
}

func (f *fakeLookup) ChatMemberOf(chat, user tele.Recipient) (*tele.ChatMember, error) {
	f.mu.Lock()
	f.calls++
	f.chat, f.user = chat.Recipient(), user.Recipient()
	var err error
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	started, release := f.started, f.release
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if err != nil {
		return nil, err
	}
	return f.member, nil
}

func (f *fakeLookup) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type recObserver struct {
	mu      sync.Mutex
	results []string
	limited int
}

func (o *recObserver) CheckDone(result string, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.results = append(o.results, result)
}

func (o *recObserver) RateLimited(string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.limited++
}

func (o *recObserver) count(result string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, r := range o.results {
		if r == result {
			n++
		}
	}
	return n
}

// clock is a virtual clock: sleeping moves time instead of waiting.
type clock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newClock() *clock { return &clock{t: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return ctx.Err()
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func withClock(c *Checker, cl *clock) *Checker {
	c.now, c.sleep = cl.now, cl.sleep
	if c.lim != nil {
		c.lim.now, c.lim.sleep = cl.now, cl.sleep
	}
	return c
}

func TestIsSubscribedRoles(t *testing.T) {
	tests := []struct {
		role tele.MemberStatus
		want bool
	}{
		{tele.Member, true},
		{tele.Administrator, true},
		{tele.Creator, true},
		{tele.Left, false},
		{tele.Kicked, false},
		{tele.Restricted, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			f := &fakeLookup{member: &tele.ChatMember{Role: tt.role}}
			got, err := newChecker(f, Options{}).IsSubscribed(context.Background(), -100123, 42)
			if err != nil || got != tt.want {
				t.Errorf("got (%v, %v), want (%v, nil)", got, err, tt.want)
			}
			if f.chat != "-100123" || f.user != "42" {
				t.Errorf("asked about chat %s user %s", f.chat, f.user)
			}
		})
	}
}

func TestIsSubscribedAPIError(t *testing.T) {
	apiErr := errors.New("chat not found")
	obs := &recObserver{}
	got, err := newChecker(&fakeLookup{errs: []error{apiErr}}, Options{Observer: obs}).IsSubscribed(context.Background(), 1, 2)
	if got || !errors.Is(err, apiErr) {
		t.Errorf("got (%v, %v), want (false, wrapped API error)", got, err)
	}
	if obs.count("error") != 1 {
		t.Errorf("observer saw %v", obs.results)
	}
}

func TestIsSubscribedCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}}
	if _, err := newChecker(f, Options{}).IsSubscribed(ctx, 1, 2); !errors.Is(err, context.Canceled) || f.callCount() != 0 {
		t.Errorf("err = %v, calls = %d", err, f.callCount())
	}
}

func TestObserverSeesEveryOutcome(t *testing.T) {
	obs := &recObserver{}
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}}
	c := newChecker(f, Options{Observer: obs})
	_, _ = c.IsSubscribed(context.Background(), 1, 2)
	f.member = &tele.ChatMember{Role: tele.Left}
	_, _ = c.IsSubscribed(context.Background(), 1, 2)
	if obs.count("subscribed") != 1 || obs.count("not_subscribed") != 1 {
		t.Errorf("results = %v", obs.results)
	}
}

func TestRateLimiterSpreadsCalls(t *testing.T) {
	cl := newClock()
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}}
	c := withClock(newChecker(f, Options{Rate: 10, Burst: 1}), cl)
	start := cl.now()
	for i := 0; i < 20; i++ {
		if _, err := c.IsSubscribed(context.Background(), 1, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := cl.now().Sub(start); elapsed < 1900*time.Millisecond {
		t.Errorf("20 calls at 10/s took %s of virtual time, want about 1.9s", elapsed)
	}
}

func TestRateLimiterAllowsABurstAfterAPause(t *testing.T) {
	cl := newClock()
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}}
	c := withClock(newChecker(f, Options{Rate: 10, Burst: 5}), cl)
	for i := 0; i < 5; i++ { // the burst goes out without waiting
		if _, err := c.IsSubscribed(context.Background(), 1, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(cl.sleeps) != 0 {
		t.Errorf("a burst of 5 slept %v", cl.sleeps)
	}
	if _, err := c.IsSubscribed(context.Background(), 1, 99); err != nil {
		t.Fatal(err)
	}
	if len(cl.sleeps) != 1 {
		t.Errorf("the 6th call must wait for a slot, sleeps = %v", cl.sleeps)
	}
}

func TestRateLimiterFailsFastWhenTheSlotIsBeyondTheDeadline(t *testing.T) {
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}}
	c := newChecker(f, Options{Rate: 1, Burst: 1}) // real clock: deadlines are real time
	if _, err := c.IsSubscribed(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	// the next slot is 1s away, but this caller can only wait 100ms
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.IsSubscribed(ctx, 1, 2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if f.callCount() != 1 {
		t.Errorf("calls = %d; Telegram must not be called when the answer cannot be waited for", f.callCount())
	}
}

func TestTelegram429IsWaitedOutAndRetried(t *testing.T) {
	cl := newClock()
	obs := &recObserver{}
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}, errs: []error{tele.FloodError{RetryAfter: 3}}}
	c := withClock(newChecker(f, Options{Observer: obs}), cl)
	ok, err := c.IsSubscribed(context.Background(), 1, 2)
	if err != nil || !ok {
		t.Fatalf("got (%v, %v)", ok, err)
	}
	if len(cl.sleeps) != 1 || cl.sleeps[0] != 3*time.Second || f.callCount() != 2 || obs.limited != 1 {
		t.Errorf("sleeps = %v, calls = %d, limited = %d", cl.sleeps, f.callCount(), obs.limited)
	}
}

func TestTelegram429GivesUpAfterTheRetries(t *testing.T) {
	cl := newClock()
	flood := tele.FloodError{RetryAfter: 1}
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}, errs: []error{flood, flood, flood, flood}}
	c := withClock(newChecker(f, Options{MaxRetries: 2}), cl)
	_, err := c.IsSubscribed(context.Background(), 1, 2)
	var got tele.FloodError
	if !errors.As(err, &got) {
		t.Fatalf("err = %T, want the flood error to be reported", err)
	}
	if f.callCount() != 3 {
		t.Errorf("calls = %d, want the first try and 2 retries", f.callCount())
	}
}

func TestTelegram429WithALongWaitFailsInsteadOfBlocking(t *testing.T) {
	cl := newClock()
	f := &fakeLookup{member: &tele.ChatMember{Role: tele.Member}, errs: []error{tele.FloodError{RetryAfter: 30}}}
	c := newChecker(f, Options{})
	c.sleep = cl.sleep // real now, so the real deadline below is meaningful
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.IsSubscribed(ctx, 1, 2)
	var got tele.FloodError
	if !errors.As(err, &got) || len(cl.sleeps) != 0 {
		t.Errorf("err = %T, sleeps = %v; waiting 30s inside a 10s budget is pointless", err, cl.sleeps)
	}
}

// --- cache ---

type countingInner struct {
	calls atomic.Int32
	ok    bool
	err   error
	gate  chan struct{}
}

func (c *countingInner) IsSubscribed(ctx context.Context, _, _ int64) (bool, error) {
	c.calls.Add(1)
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return c.ok, c.err
}

func TestCachePositiveAnswerIsReusedUntilTheTTLEnds(t *testing.T) {
	cl := newClock()
	inner := &countingInner{ok: true}
	obs := &recObserver{}
	c := NewCached(inner, 30*time.Second, obs)
	c.now = cl.now

	for i := 0; i < 3; i++ {
		if ok, err := c.IsSubscribed(context.Background(), 1, 2); err != nil || !ok {
			t.Fatalf("got (%v, %v)", ok, err)
		}
	}
	if inner.calls.Load() != 1 || obs.count("cache_hit") != 2 {
		t.Errorf("inner calls = %d, cache hits = %d; want 1 and 2", inner.calls.Load(), obs.count("cache_hit"))
	}
	if _, _ = c.IsSubscribed(context.Background(), 1, 3); inner.calls.Load() != 2 {
		t.Error("another user must not use this user's entry")
	}
	if _, _ = c.IsSubscribed(context.Background(), 9, 2); inner.calls.Load() != 3 {
		t.Error("another chat must not use this entry")
	}

	cl.advance(31 * time.Second)
	_, _ = c.IsSubscribed(context.Background(), 1, 2)
	if inner.calls.Load() != 4 {
		t.Errorf("after the TTL the answer must be checked again, calls = %d", inner.calls.Load())
	}
}

func TestCacheDoesNotRememberNegativeAnswersOrErrors(t *testing.T) {
	inner := &countingInner{ok: false}
	c := NewCached(inner, time.Minute, nil)
	for i := 0; i < 3; i++ {
		if ok, _ := c.IsSubscribed(context.Background(), 1, 2); ok {
			t.Fatal("not subscribed")
		}
	}
	if inner.calls.Load() != 3 {
		t.Errorf("calls = %d; a user who has just subscribed must be recognized at once", inner.calls.Load())
	}

	inner = &countingInner{err: errors.New("telegram is down")}
	c = NewCached(inner, time.Minute, nil)
	for i := 0; i < 2; i++ {
		if _, err := c.IsSubscribed(context.Background(), 1, 2); err == nil {
			t.Fatal("expected an error")
		}
	}
	if inner.calls.Load() != 2 {
		t.Errorf("errors were cached: calls = %d", inner.calls.Load())
	}
}

func TestCacheMergesConcurrentChecksIntoOneCall(t *testing.T) {
	inner := &countingInner{ok: true, gate: make(chan struct{})}
	c := NewCached(inner, 0, nil) // merging works even without the cache
	const n = 200
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := c.IsSubscribed(context.Background(), 1, 2)
			results[i] = ok && err == nil
		}()
	}
	// wait until the single call is in flight, give the others time to join it
	for inner.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(inner.gate)
	wg.Wait()

	if inner.calls.Load() != 1 {
		t.Errorf("Telegram was called %d times for %d identical checks", inner.calls.Load(), n)
	}
	for i, ok := range results {
		if !ok {
			t.Fatalf("caller %d did not get the shared answer", i)
		}
	}
}

func TestCacheSharedCallSurvivesAnImpatientCaller(t *testing.T) {
	inner := &countingInner{ok: true, gate: make(chan struct{})}
	c := NewCached(inner, time.Minute, nil)

	impatient, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := c.IsSubscribed(impatient, 1, 2)
		first <- err
	}()
	for inner.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	patient := make(chan bool, 1)
	go func() {
		ok, err := c.IsSubscribed(context.Background(), 1, 2)
		patient <- ok && err == nil
	}()
	time.Sleep(30 * time.Millisecond)

	cancel() // the first caller gives up
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("impatient caller: %v", err)
	}
	close(inner.gate) // Telegram answers
	if !<-patient {
		t.Error("the other caller lost the answer because the first one left")
	}
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d", inner.calls.Load())
	}
}

func TestCachePrunesExpiredEntriesWhenFull(t *testing.T) {
	cl := newClock()
	c := NewCached(&countingInner{ok: true}, time.Minute, nil)
	c.now = cl.now
	for i := 0; i < maxCacheEntries; i++ {
		c.entries[key{1, int64(i)}] = cl.now().Add(-time.Second) // all expired
	}
	if ok, err := c.IsSubscribed(context.Background(), 5, 5); err != nil || !ok {
		t.Fatal(ok, err)
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > 2 {
		t.Errorf("%d entries left after pruning, want the expired ones gone", n)
	}
}
