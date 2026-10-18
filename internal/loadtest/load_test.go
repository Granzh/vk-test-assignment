// Package loadtest holds the load test of the bot: it imitates a crowd pressing
// "Get promo code" and "Check subscription" at the same moment and reports
// latencies, errors, pool usage and the rate of calls to the (fake) Telegram.
//
// It runs against a real PostgreSQL and is opt-in:
//
//	LOAD_TEST=1 TEST_DATABASE_URL=postgres://bot:bot@localhost:5432/postgres?sslmode=disable \
//	    go test -count=1 -v -run TestLoad ./internal/loadtest
//
// Knobs (environment): LOAD_USERS (1000), LOAD_POOL (20), LOAD_CHECK_RATE (100,
// getChatMember per second), LOAD_LATENCY_MS (40, latency of one Telegram call),
// LOAD_CONCURRENCY (200, updates handled at once, like MAX_CONCURRENT_UPDATES).
package loadtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/domain"
	"chat_bot/internal/service"
	"chat_bot/internal/storage"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/storage/storagetest"
	"chat_bot/internal/subscription"
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// fakeTelegram answers getChatMember after a fixed latency and records when
// each call started and how many ran at once.
type fakeTelegram struct {
	latency time.Duration

	mu       sync.Mutex
	starts   []time.Time
	inflight int
	peak     int
}

func (f *fakeTelegram) ChatMemberOf(tele.Recipient, tele.Recipient) (*tele.ChatMember, error) {
	f.mu.Lock()
	f.starts = append(f.starts, time.Now())
	f.inflight++
	f.peak = max(f.peak, f.inflight)
	f.mu.Unlock()

	time.Sleep(f.latency)

	f.mu.Lock()
	f.inflight--
	f.mu.Unlock()
	return &tele.ChatMember{Role: tele.Member}, nil
}

func (f *fakeTelegram) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

// maxPerSecond is the most calls that started within any window of one second.
func (f *fakeTelegram) maxPerSecond() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := append([]time.Time(nil), f.starts...)
	sort.Slice(s, func(i, j int) bool { return s[i].Before(s[j]) })
	best, lo := 0, 0
	for hi := range s {
		for s[hi].Sub(s[lo]) >= time.Second {
			lo++
		}
		best = max(best, hi-lo+1)
	}
	return best
}

func (f *fakeTelegram) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts, f.peak = nil, 0
}

// result of one simulated press.
type result struct {
	d   time.Duration
	err error
}

// phase runs fn for every id at the same moment and collects the outcomes.
type phaseReport struct {
	name      string
	n         int
	wall      time.Duration
	lat       []time.Duration
	ok        int
	errs      map[string]int
	peakConns int32
}

func (p phaseReport) pct(q float64) time.Duration {
	if len(p.lat) == 0 {
		return 0
	}
	return p.lat[min(int(float64(len(p.lat))*q), len(p.lat)-1)]
}

func (p phaseReport) String() string {
	return fmt.Sprintf("%-34s n=%-5d ok=%-5d wall=%-8s p50=%-8s p95=%-8s p99=%-8s max=%-8s peak_conns=%d errors=%v",
		p.name, p.n, p.ok, p.wall.Round(time.Millisecond), p.pct(.50).Round(time.Millisecond), p.pct(.95).Round(time.Millisecond),
		p.pct(.99).Round(time.Millisecond), p.pct(1).Round(time.Millisecond), p.peakConns, p.errs)
}

func TestLoad(t *testing.T) {
	if os.Getenv("LOAD_TEST") == "" {
		t.Skip("load test; run with LOAD_TEST=1 (make load-test)")
	}
	users := envInt("LOAD_USERS", 1000)
	poolSize := envInt("LOAD_POOL", 20)
	rate := envInt("LOAD_CHECK_RATE", 100)
	latency := time.Duration(envInt("LOAD_LATENCY_MS", 40)) * time.Millisecond
	concurrency := envInt("LOAD_CONCURRENCY", 200)
	const updateTimeout = 15 * time.Second // handlers.requestTimeout

	ctx := context.Background()
	admin := storagetest.New(t) // an isolated database with all migrations
	pool, err := storage.Connect(ctx, admin.Config().ConnString(), storage.Options{MaxConns: int32(poolSize)})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := storage.NewStore(pool)

	// A campaign running today with one partner, a code for every user and a spare.
	cal := domain.Calendar{Location: time.UTC}
	today := cal.DayOf(time.Now())
	camp := service.NewCampaign(store, cal, today.AddDate(0, 0, -1), today.AddDate(0, 0, 1)).WithCache(5 * time.Second) // as in main
	partner, err := store.CreatePartner(ctx, db.CreatePartnerParams{Name: "Load partner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE partners SET chat_id = -100123 WHERE id = $1`, partner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO campaign_days (day, partner_id, winners_count) VALUES ($1, $2, 3)`, today, partner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO promo_codes (partner_id, code) SELECT $1, 'L' || g FROM generate_series(1, $2) g`, partner.ID, users+10); err != nil {
		t.Fatal(err)
	}

	tg := &fakeTelegram{latency: latency}
	checker := subscription.NewCheckerWith(tg, subscription.Options{Rate: rate})
	cached := subscription.NewCached(checker, 30*time.Second, nil)
	promo := service.NewPromo(store, 10)
	entry := service.NewEntry(store, cached, 10)

	sem := make(chan struct{}, concurrency) // limitMW
	var peakConns atomic.Int32
	stop := make(chan struct{})
	go func() { // sample the pool while the phases run
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				c := pool.Stat().AcquiredConns()
				for p := peakConns.Load(); c > p && !peakConns.CompareAndSwap(p, c); p = peakConns.Load() {
				}
			}
		}
	}()
	defer close(stop)

	// handle imitates one update: concurrency limit, per-update timeout, the
	// user upsert of userMW, the active-day lookup and the action itself.
	handle := func(tgID int64, action func(ctx context.Context, u db.User, ad *service.ActiveDay) error) result {
		start := time.Now()
		select { // limitMW: wait for a slot, but not longer than the update timeout
		case sem <- struct{}{}:
		case <-time.After(updateTimeout):
			return result{d: time.Since(start), err: errBusy}
		}
		defer func() { <-sem }()
		ctx, cancel := context.WithTimeout(ctx, updateTimeout)
		defer cancel()
		u, err := store.UpsertUser(ctx, db.UpsertUserParams{TelegramID: tgID})
		if err == nil {
			var ad *service.ActiveDay
			if ad, err = camp.Active(ctx); err == nil {
				err = action(ctx, u, ad)
			}
		}
		return result{d: time.Since(start), err: err}
	}

	storm := func(name string, ids []int64, action func(ctx context.Context, u db.User, ad *service.ActiveDay) error) phaseReport {
		peakConns.Store(0)
		tg.reset()
		results := make([]result, len(ids))
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for i, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				results[i] = handle(id, action)
			}()
		}
		start := time.Now()
		close(gate)
		wg.Wait()
		rep := phaseReport{name: name, n: len(ids), wall: time.Since(start), errs: map[string]int{}, peakConns: peakConns.Load()}
		for _, r := range results {
			rep.lat = append(rep.lat, r.d)
			if r.err == nil {
				rep.ok++
				continue
			}
			rep.errs[classify(r.err)]++
		}
		sort.Slice(rep.lat, func(i, j int) bool { return rep.lat[i] < rep.lat[j] })
		t.Log(rep)
		return rep
	}

	ids := make([]int64, users)
	for i := range ids {
		ids[i] = int64(5_000_000 + i)
	}

	count := func(q string, args ...any) int {
		var n int
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. everybody presses "Get promo code" at once.
	p1 := storm("1. promo button, all at once", ids, func(ctx context.Context, u db.User, ad *service.ActiveDay) error {
		_, err := promo.Issue(ctx, u.ID, ad)
		return err
	})
	if p1.ok != users {
		t.Errorf("promo: %d of %d succeeded, errors %v", p1.ok, users, p1.errs)
	}
	if n := count(`SELECT count(DISTINCT promo_code_id) FROM promo_issues`); n != users {
		t.Errorf("distinct issued codes = %d, want %d (a code must never go to two people)", n, users)
	}
	if n := count(`SELECT count(*) FROM promo_codes WHERE assigned_to_user_id IS NOT NULL`); n != users {
		t.Errorf("assigned codes = %d, want %d", n, users)
	}
	if n := count(`SELECT count(*) FROM promo_requests WHERE issued`); n != users {
		t.Errorf("promo_requests = %d, want %d", n, users)
	}

	// 2. everybody presses "Check subscription" at once. Telegram, not the
	// database, is the bottleneck here: the checks drain at the configured rate.
	p2 := storm("2. check subscription, all at once", ids, func(ctx context.Context, u db.User, ad *service.ActiveDay) error {
		_, err := entry.Join(ctx, u, ad)
		return err
	})
	t.Logf("   Telegram: %d getChatMember calls, at most %d per second (limit %d), %d at once", tg.calls(), tg.maxPerSecond(), rate, tg.peak)
	if p2.ok+p2.errs["check_failed"]+p2.errs["busy"] != users {
		t.Errorf("unexpected failures: %v", p2.errs)
	}
	if allowed := rate + max(rate/5, 1) + 2; tg.maxPerSecond() > allowed {
		t.Errorf("Telegram was called %d times in one second, limit %d (+burst)", tg.maxPerSecond(), rate)
	}
	if n := count(`SELECT count(*) FROM daily_entries`); n != p2.ok {
		t.Errorf("daily_entries = %d, successful checks %d", n, p2.ok)
	}
	if n := count(`SELECT count(*) FROM activity_days`); n != users {
		t.Errorf("activity_days = %d, want %d (promo and entry on one day = one chance)", n, users)
	}
	// In updateTimeout the limiter can serve about rate*timeout checks plus the
	// updates already admitted; the rest must be told "busy", not lost silently.
	if want := int(float64(rate) * updateTimeout.Seconds() * 0.8); p2.ok < min(users, want) {
		t.Errorf("only %d checks succeeded; at %d/s about %d fit into the %s update timeout", p2.ok, rate, want, updateTimeout)
	}

	// 3. a few people hammer the button: 10 presses each, all at once.
	const hammer = 100
	extra := make([]int64, 0, hammer*10)
	for i := 0; i < hammer; i++ {
		for k := 0; k < 10; k++ {
			extra = append(extra, int64(9_000_000+i))
		}
	}
	p3 := storm("3. check, 100 users x 10 presses", extra, func(ctx context.Context, u db.User, ad *service.ActiveDay) error {
		_, err := entry.Join(ctx, u, ad)
		return err
	})
	t.Logf("   Telegram: %d getChatMember calls for %d presses by %d users", tg.calls(), len(extra), hammer)
	if tg.calls() > hammer+hammer/10 {
		t.Errorf("%d Telegram calls for %d users: repeated presses must be merged or cached", tg.calls(), hammer)
	}
	if p3.errs["other"] > 0 {
		t.Errorf("unexpected errors: %v", p3.errs)
	}
	if n := count(`SELECT count(*) FROM daily_entries WHERE user_id IN (SELECT id FROM users WHERE telegram_id >= 9000000)`); n > hammer {
		t.Errorf("%d entries for %d users: duplicates were created", n, hammer)
	}
	t.Logf("pool: size %d, peak in use %d/%d/%d; waits for a free connection: %d",
		poolSize, p1.peakConns, p2.peakConns, p3.peakConns, pool.Stat().EmptyAcquireCount())
}

// errBusy is what limitMW answers when no slot became free in time (the user
// sees "too many requests, try again").
var errBusy = errors.New("busy")

// classify groups errors the way the user sees them.
func classify(err error) string {
	switch {
	case errors.Is(err, service.ErrSubscriptionCheck):
		return "check_failed" // shown as "could not check, try later"
	case errors.Is(err, errBusy):
		return "busy"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "other"
}
