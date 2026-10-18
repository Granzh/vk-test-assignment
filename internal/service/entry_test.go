package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

// fakeChecker is a SubscriptionChecker mock. When barrier is set, every call
// waits until that many calls are in flight, so concurrent requests all pass
// the Telegram check before any of them writes.
type fakeChecker struct {
	subscribed bool
	err        error
	calls      atomic.Int32

	barrier int
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func (f *fakeChecker) IsSubscribed(_ context.Context, _, _ int64) (bool, error) {
	f.calls.Add(1)
	if f.barrier > 0 {
		f.mu.Lock()
		f.arrived++
		if f.arrived == f.barrier {
			close(f.release)
		}
		f.mu.Unlock()
		<-f.release
	}
	return f.subscribed, f.err
}

type checkerFunc func(chatID, userID int64) (bool, error)

func (f checkerFunc) IsSubscribed(_ context.Context, chatID, userID int64) (bool, error) {
	return f(chatID, userID)
}

// withChat gives the active day's partner a channel id, as in production.
func withChat(ad *service.ActiveDay) *service.ActiveDay {
	ad.Partner.ChatID = pgtype.Int8{Int64: -1001234567890, Valid: true}
	return ad
}

func (e *env) userRow(t *testing.T, telegramID int64) db.User {
	t.Helper()
	u, err := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: telegramID})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func assertNothingWritten(t *testing.T, e *env) {
	t.Helper()
	if n := e.count(t, `SELECT count(*) FROM daily_entries`); n != 0 {
		t.Errorf("daily_entries = %d, want 0", n)
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days`); n != 0 {
		t.Errorf("activity_days = %d, want 0", n)
	}
}

func TestEntryJoinSubscribed(t *testing.T) {
	e := newEnv(t, 0)
	ad := withChat(e.day(t, 1))
	svc := service.NewEntry(e.store, &fakeChecker{subscribed: true}, 10)
	u := e.userRow(t, 1)

	res, err := svc.Join(e.ctx, u, ad)
	if err != nil {
		t.Fatal(err)
	}
	if res.Already || res.Chances != 1 {
		t.Errorf("res = %+v, want new entry with 1 chance", res)
	}
	entry, err := e.store.GetDailyEntry(e.ctx, db.GetDailyEntryParams{UserID: u.ID, CampaignDayID: ad.Day.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.SubscribedCheckedAt.Valid || entry.SubscribedCheckedAt.Time.IsZero() {
		t.Error("registration time is not recorded")
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days WHERE user_id = $1`, u.ID); n != 1 {
		t.Errorf("activity_days = %d, want 1", n)
	}
}

func TestEntryJoinAsksTelegramAboutPartnerChannel(t *testing.T) {
	e := newEnv(t, 0)
	ad := withChat(e.day(t, 1))
	var gotChat, gotUser int64
	chk := checkerFunc(func(chatID, userID int64) (bool, error) {
		gotChat, gotUser = chatID, userID
		return true, nil
	})
	if _, err := service.NewEntry(e.store, chk, 10).Join(e.ctx, e.userRow(t, 777), ad); err != nil {
		t.Fatal(err)
	}
	if gotChat != ad.Partner.ChatID.Int64 || gotUser != 777 {
		t.Errorf("checked chat %d user %d", gotChat, gotUser)
	}
}

func TestEntryJoinRepeatIsIdempotent(t *testing.T) {
	e := newEnv(t, 0)
	ad := withChat(e.day(t, 1))
	chk := &fakeChecker{subscribed: true}
	svc := service.NewEntry(e.store, chk, 10)
	u := e.userRow(t, 1)

	if _, err := svc.Join(e.ctx, u, ad); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Join(e.ctx, u, ad)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Already || res.Chances != 1 {
		t.Errorf("res = %+v, want Already with 1 chance", res)
	}
	if got := chk.calls.Load(); got != 1 {
		t.Errorf("Telegram checked %d times, want 1 (repeat needs no check)", got)
	}
	if n := e.count(t, `SELECT count(*) FROM daily_entries`); n != 1 {
		t.Errorf("daily_entries = %d, want 1", n)
	}
}

func TestEntryJoinNotSubscribed(t *testing.T) {
	e := newEnv(t, 0)
	ad := withChat(e.day(t, 1))
	svc := service.NewEntry(e.store, &fakeChecker{subscribed: false}, 10)

	_, err := svc.Join(e.ctx, e.userRow(t, 1), ad)
	if !errors.Is(err, service.ErrNotSubscribed) {
		t.Fatalf("err = %v, want ErrNotSubscribed", err)
	}
	assertNothingWritten(t, e)
}

func TestEntryJoinNotSubscribedThenSubscribed(t *testing.T) {
	e := newEnv(t, 0)
	ad := withChat(e.day(t, 1))
	chk := &fakeChecker{}
	svc := service.NewEntry(e.store, chk, 10)
	u := e.userRow(t, 1)

	for i := 0; i < 3; i++ { // retries are unlimited
		if _, err := svc.Join(e.ctx, u, ad); !errors.Is(err, service.ErrNotSubscribed) {
			t.Fatalf("attempt %d: err = %v, want ErrNotSubscribed", i, err)
		}
	}
	chk.subscribed = true
	res, err := svc.Join(e.ctx, u, ad)
	if err != nil || res.Already || res.Chances != 1 {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestEntryJoinTelegramError(t *testing.T) {
	e := newEnv(t, 0)
	ad := withChat(e.day(t, 1))
	apiErr := errors.New("telegram: bot is not a member of the channel chat")
	svc := service.NewEntry(e.store, &fakeChecker{err: apiErr}, 10)

	_, err := svc.Join(e.ctx, e.userRow(t, 1), ad)
	if !errors.Is(err, service.ErrSubscriptionCheck) || !errors.Is(err, apiErr) {
		t.Fatalf("err = %v, want ErrSubscriptionCheck wrapping the API error", err)
	}
	if errors.Is(err, service.ErrNotSubscribed) {
		t.Error("an API error must not look like a missing subscription")
	}
	assertNothingWritten(t, e)
}

func TestEntryJoinPartnerWithoutChat(t *testing.T) {
	e := newEnv(t, 0)
	ad := e.day(t, 1) // partner has no chat_id
	chk := &fakeChecker{subscribed: true}
	svc := service.NewEntry(e.store, chk, 10)

	_, err := svc.Join(e.ctx, e.userRow(t, 1), ad)
	if !errors.Is(err, service.ErrSubscriptionCheck) {
		t.Fatalf("err = %v, want ErrSubscriptionCheck", err)
	}
	if chk.calls.Load() != 0 {
		t.Error("checker must not be called without a chat id")
	}
	assertNothingWritten(t, e)
}

func TestEntryJoinDayAlreadyActiveViaPromo(t *testing.T) {
	e := newEnv(t, 1)
	ad := withChat(e.day(t, 1))
	u := e.userRow(t, 1)
	if _, err := service.NewPromo(e.store, 10).Issue(e.ctx, u.ID, ad); err != nil {
		t.Fatal(err)
	}

	res, err := service.NewEntry(e.store, &fakeChecker{subscribed: true}, 10).Join(e.ctx, u, ad)
	if err != nil {
		t.Fatal(err)
	}
	if res.Already || res.Chances != 1 {
		t.Errorf("res = %+v, want new entry, still 1 chance", res)
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days`); n != 1 {
		t.Errorf("activity_days = %d, want 1 (the day was counted once)", n)
	}
}

func TestEntryJoinChancesAccumulateAndRespectCap(t *testing.T) {
	e := newEnv(t, 0)
	days := []*service.ActiveDay{withChat(e.day(t, 1)), withChat(e.day(t, 2)), withChat(e.day(t, 3))}
	svc := service.NewEntry(e.store, &fakeChecker{subscribed: true}, 2)
	u := e.userRow(t, 1)

	want := []int{1, 2, 2}
	for i, ad := range days {
		res, err := svc.Join(e.ctx, u, ad)
		if err != nil {
			t.Fatal(err)
		}
		if res.Chances != want[i] {
			t.Errorf("day %d: chances = %d, want %d", i+1, res.Chances, want[i])
		}
	}
	if n := e.count(t, `SELECT count(*) FROM daily_entries WHERE user_id = $1`, u.ID); n != 3 {
		t.Errorf("daily_entries = %d, want 3 (the cap limits chances, not entries)", n)
	}
}

func TestEntryJoinConcurrentPresses(t *testing.T) {
	const n = 8
	e := newEnv(t, 0)
	ad := withChat(e.day(t, 1))
	chk := &fakeChecker{subscribed: true, barrier: n, release: make(chan struct{})}
	svc := service.NewEntry(e.store, chk, 10)
	u := e.userRow(t, 1)

	results := make([]service.JoinResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.Join(e.ctx, u, ad)
		}()
	}
	wg.Wait()

	fresh := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("press %d: %v", i, errs[i])
		}
		if !results[i].Already {
			fresh++
		}
		if results[i].Chances != 1 {
			t.Errorf("press %d: chances = %d, want 1", i, results[i].Chances)
		}
	}
	if fresh != 1 {
		t.Errorf("%d presses created an entry, want exactly 1", fresh)
	}
	if got := chk.calls.Load(); got != n {
		t.Errorf("checker calls = %d, want %d (all presses must have raced past the check)", got, n)
	}
	if c := e.count(t, `SELECT count(*) FROM daily_entries`); c != 1 {
		t.Errorf("daily_entries = %d, want 1", c)
	}
	if c := e.count(t, `SELECT count(*) FROM activity_days`); c != 1 {
		t.Errorf("activity_days = %d, want 1", c)
	}
}
