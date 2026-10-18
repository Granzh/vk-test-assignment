package service_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/domain"
	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// subs is a SubscriptionChecker keyed by telegram id: everyone is subscribed
// unless listed in unsub; ids in fail make the check error out.
type subs struct {
	mu    sync.Mutex
	unsub map[int64]bool
	fail  map[int64]error
	calls atomic.Int32
}

func (s *subs) IsSubscribed(_ context.Context, _, telegramID int64) (bool, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[telegramID]; err != nil {
		return false, err
	}
	return !s.unsub[telegramID], nil
}

// outbox is a Sender that records messages; ids in fail are undeliverable.
type outbox struct {
	mu   sync.Mutex
	sent map[int64][]string
	fail map[int64]bool
}

func newOutbox() *outbox { return &outbox{sent: map[int64][]string{}, fail: map[int64]bool{}} }

func (o *outbox) Send(_ context.Context, telegramID int64, html string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail[telegramID] {
		return errors.New("Forbidden: bot was blocked by the user")
	}
	o.sent[telegramID] = append(o.sent[telegramID], html)
	return nil
}

func (o *outbox) total() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, m := range o.sent {
		n += len(m)
	}
	return n
}

type drawEnv struct {
	*env
	subs   *subs
	out    *outbox
	draw   *service.DailyDraw
	ad     *service.ActiveDay
	camp   *service.Campaign
	nowDay int // day of September the clock shows
}

// newDrawEnv creates a day (Sep 5) with winners winners and a prize text; the clock shows Sep 6.
func newDrawEnv(t *testing.T, winners int32) *drawEnv {
	t.Helper()
	e := newEnv(t, 0)
	d := pgtype.Date{Time: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Valid: true}
	e.partner.ChatID = pgtype.Int8{Int64: -100, Valid: true}
	if _, err := e.pool.Exec(e.ctx, `UPDATE partners SET chat_id = -100 WHERE id = $1`, e.partner.ID); err != nil {
		t.Fatal(err)
	}
	cd, err := e.store.CreateCampaignDay(e.ctx, db.CreateCampaignDayParams{
		Day: d, PartnerID: e.partner.ID, WinnersCount: winners, PrizesDescription: "Сертификат",
	})
	if err != nil {
		t.Fatal(err)
	}
	de := &drawEnv{env: e, subs: &subs{unsub: map[int64]bool{}, fail: map[int64]error{}}, out: newOutbox()}
	de.ad = &service.ActiveDay{Day: cd, Partner: e.partner}
	de.camp = service.NewCampaign(e.store, domain.Calendar{Location: time.UTC},
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	de.setToday(6)
	de.draw = service.NewDailyDraw(e.store, de.subs, de.out, de.camp, discardLog)
	return de
}

func (d *drawEnv) setToday(day int) {
	d.nowDay = day
	d.camp.SetNow(func() time.Time { return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC) })
}

func (d *drawEnv) date() time.Time { return time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) }

// enter registers n new users (telegram ids 1000+) for the day.
func (d *drawEnv) enter(t *testing.T, n int) []db.User {
	t.Helper()
	var users []db.User
	base := d.count(t, `SELECT count(*) FROM users`)
	for i := 0; i < n; i++ {
		u := d.userRow(t, int64(1000+base+i))
		if _, err := d.store.CreateDailyEntry(d.ctx, db.CreateDailyEntryParams{
			UserID: u.ID, CampaignDayID: d.ad.Day.ID, SubscribedCheckedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	return users
}

func tgIDs(ws []service.DrawWinner) []int64 {
	var ids []int64
	for _, w := range ws {
		ids = append(ids, w.TelegramID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (d *drawEnv) dayStatus(t *testing.T) string {
	t.Helper()
	cd, err := d.store.GetCampaignDay(d.ctx, d.ad.Day.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cd.Status
}

func TestDrawNoDuplicatesAndNotMoreThanParticipants(t *testing.T) {
	d := newDrawEnv(t, 5)
	d.enter(t, 20)

	res, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Already || len(res.Winners) != 5 || res.Entries != 20 || res.Excluded != 0 {
		t.Fatalf("res = %+v", res)
	}
	seen := map[int64]bool{}
	for _, w := range res.Winners {
		if seen[w.TelegramID] {
			t.Errorf("winner %d selected twice", w.TelegramID)
		}
		seen[w.TelegramID] = true
		if w.Prize != "Сертификат" || w.Status != "selected" {
			t.Errorf("winner = %+v", w)
		}
	}
	if n := d.count(t, `SELECT count(*) FROM daily_winners`); n != 5 {
		t.Errorf("daily_winners = %d, want 5", n)
	}
	if st := d.dayStatus(t); st != "drawn" || !res.DrawnAt.After(time.Now().Add(-time.Minute)) {
		t.Errorf("status = %s, drawn_at = %v", st, res.DrawnAt)
	}
	if n := d.count(t, `SELECT count(*) FROM daily_entries`); n != 20 {
		t.Errorf("the participant list must stay intact, entries = %d", n)
	}
}

func TestDrawFewerParticipantsThanWinners(t *testing.T) {
	d := newDrawEnv(t, 10)
	d.enter(t, 3)
	res, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Winners) != 3 || res.WinnersCount != 10 {
		t.Errorf("winners = %d of %d, want all 3 participants to win", len(res.Winners), res.WinnersCount)
	}
}

func TestDrawNoParticipants(t *testing.T) {
	d := newDrawEnv(t, 3)
	res, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Winners) != 0 || res.Entries != 0 || d.dayStatus(t) != "drawn" {
		t.Errorf("res = %+v, status %s", res, d.dayStatus(t))
	}
}

func TestDrawSingleParticipantAndExactCount(t *testing.T) {
	d := newDrawEnv(t, 1)
	us := d.enter(t, 1)
	res, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil || len(res.Winners) != 1 || res.Winners[0].TelegramID != us[0].TelegramID {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestDrawRerunKeepsResult(t *testing.T) {
	d := newDrawEnv(t, 3)
	d.enter(t, 12)
	first, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := d.subs.calls.Load()

	// even entries added behind its back (or a different clock) cannot change it
	d.enter(t, 5)
	d.setToday(20)
	second, err := d.draw.Run(d.ctx, 10, d.date(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already || first.Already {
		t.Errorf("Already: first = %v, second = %v", first.Already, second.Already)
	}
	if fmt.Sprint(tgIDs(first.Winners)) != fmt.Sprint(tgIDs(second.Winners)) || !first.DrawnAt.Equal(second.DrawnAt) {
		t.Errorf("result changed: %v -> %v", tgIDs(first.Winners), tgIDs(second.Winners))
	}
	if d.subs.calls.Load() != callsAfterFirst {
		t.Error("a rerun must not touch Telegram")
	}
	if n := d.count(t, `SELECT count(*) FROM daily_winners`); n != 3 {
		t.Errorf("daily_winners = %d, want 3", n)
	}
}

func TestDrawConcurrentRunsProduceOneResult(t *testing.T) {
	d := newDrawEnv(t, 4)
	d.enter(t, 30)

	const n = 8
	results := make([]service.DrawResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = d.draw.Run(d.ctx, 10, d.date(), false)
		}()
	}
	wg.Wait()

	fresh := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if !r.Already {
			fresh++
		}
		if fmt.Sprint(tgIDs(r.Winners)) != fmt.Sprint(tgIDs(results[0].Winners)) {
			t.Errorf("run %d saw different winners", i)
		}
	}
	if fresh != 1 {
		t.Errorf("%d runs held the draw, want exactly 1", fresh)
	}
	if c := d.count(t, `SELECT count(*) FROM daily_winners`); c != 4 {
		t.Errorf("daily_winners = %d, want 4", c)
	}
	if c := d.count(t, `SELECT count(*) FROM audit_log WHERE action = 'draw_run'`); c != 1 {
		t.Errorf("draw_run audit rows = %d, want 1", c)
	}
}

func TestDrawExcludesUnsubscribedAndTakesNext(t *testing.T) {
	d := newDrawEnv(t, 6)
	users := d.enter(t, 10)
	gone := map[int64]bool{}
	for _, u := range users[:4] {
		d.subs.unsub[u.TelegramID] = true
		gone[u.TelegramID] = true
	}

	res, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil {
		t.Fatal(err)
	}
	// only 6 subscribed people exist and 6 winners are wanted: all of them win.
	// Unsubscribed ones are excluded only if the walk reached them before the
	// sixth winner was found, so the count varies between 0 and 4.
	if len(res.Winners) != 6 || res.Excluded > 4 {
		t.Fatalf("winners = %d, excluded = %d; want 6 and at most 4", len(res.Winners), res.Excluded)
	}
	for _, w := range res.Winners {
		if gone[w.TelegramID] {
			t.Errorf("unsubscribed user %d won", w.TelegramID)
		}
	}
	if n := d.count(t, `SELECT count(*) FROM daily_entries WHERE excluded_reason = 'unsubscribed'`); n != res.Excluded {
		t.Errorf("excluded rows = %d, want %d", n, res.Excluded)
	}
	if n := d.count(t, `SELECT count(*) FROM daily_entries`); n != 10 {
		t.Errorf("entries = %d, want all 10 kept", n)
	}
}

func TestDrawNeverPicksUnsubscribedWhenReplacementsExist(t *testing.T) {
	for i := 0; i < 5; i++ { // random order: repeat
		d := newDrawEnv(t, 3)
		users := d.enter(t, 10)
		for _, u := range users[:5] {
			d.subs.unsub[u.TelegramID] = true
		}
		res, err := d.draw.Run(d.ctx, 10, d.date(), false)
		if err != nil || len(res.Winners) != 3 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		for _, w := range res.Winners {
			if d.subs.unsub[w.TelegramID] {
				t.Fatalf("unsubscribed user %d won", w.TelegramID)
			}
		}
	}
}

func TestDrawTelegramFailureRollsBackAndCanBeRetried(t *testing.T) {
	d := newDrawEnv(t, 5)
	users := d.enter(t, 5) // everyone is needed, so the failing one is always reached
	d.subs.unsub[users[0].TelegramID] = true
	d.subs.fail[users[1].TelegramID] = errors.New("telegram: too many requests")

	_, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if !errors.Is(err, service.ErrSubscriptionCheck) {
		t.Fatalf("err = %v, want ErrSubscriptionCheck", err)
	}
	if st := d.dayStatus(t); st != "pending" {
		t.Errorf("status = %s, want pending", st)
	}
	if n := d.count(t, `SELECT count(*) FROM daily_winners`) + d.count(t, `SELECT count(*) FROM daily_entries WHERE excluded_at IS NOT NULL`); n != 0 {
		t.Errorf("a failed draw left %d rows behind", n)
	}

	delete(d.subs.fail, users[1].TelegramID)
	res, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil || len(res.Winners) != 4 || res.Excluded != 1 {
		t.Errorf("retry: res = %+v, err = %v", res, err)
	}
}

func TestDrawPartnerWithoutChatID(t *testing.T) {
	d := newDrawEnv(t, 1)
	d.enter(t, 2)
	if _, err := d.pool.Exec(d.ctx, `UPDATE partners SET chat_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.draw.Run(d.ctx, 10, d.date(), false); !errors.Is(err, service.ErrSubscriptionCheck) {
		t.Errorf("err = %v, want ErrSubscriptionCheck", err)
	}
}

func TestDrawTiming(t *testing.T) {
	d := newDrawEnv(t, 1)
	d.enter(t, 2)

	d.setToday(5) // the day itself
	if _, err := d.draw.Run(d.ctx, 10, d.date(), false); !errors.Is(err, service.ErrDrawDayNotOver) {
		t.Errorf("same day: err = %v, want ErrDrawDayNotOver", err)
	}
	d.setToday(4)
	if _, err := d.draw.Run(d.ctx, 10, d.date(), true); !errors.Is(err, service.ErrDrawDayInFuture) {
		t.Errorf("future day: err = %v, want ErrDrawDayInFuture (even with force)", err)
	}
	if _, err := d.draw.Run(d.ctx, 10, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), false); !errors.Is(err, service.ErrDrawDayNotFound) {
		t.Errorf("unknown day: err = %v, want ErrDrawDayNotFound", err)
	}
	if st := d.dayStatus(t); st != "pending" {
		t.Errorf("status = %s", st)
	}

	d.setToday(5)
	if _, err := d.draw.Run(d.ctx, 10, d.date(), true); err != nil {
		t.Errorf("force on the day itself: %v", err)
	}
}

func TestEntriesClosedAfterDraw(t *testing.T) {
	d := newDrawEnv(t, 1)
	d.enter(t, 1)
	if _, err := d.draw.Run(d.ctx, 10, d.date(), false); err != nil {
		t.Fatal(err)
	}
	late := d.userRow(t, 9999)
	_, err := service.NewEntry(d.store, &fakeChecker{subscribed: true}, 10).Join(d.ctx, late, d.ad)
	if !errors.Is(err, service.ErrDayClosed) {
		t.Errorf("err = %v, want ErrDayClosed", err)
	}
	if n := d.count(t, `SELECT count(*) FROM daily_entries WHERE user_id = $1`, late.ID); n != 0 {
		t.Error("late entry was stored")
	}
}

func TestConfirmBeforeRunAndUnknownDay(t *testing.T) {
	d := newDrawEnv(t, 1)
	if _, err := d.draw.Confirm(d.ctx, 10, d.date()); !errors.Is(err, service.ErrDrawNotRun) {
		t.Errorf("err = %v, want ErrDrawNotRun", err)
	}
	if _, err := d.draw.Confirm(d.ctx, 10, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)); !errors.Is(err, service.ErrDrawDayNotFound) {
		t.Errorf("err = %v, want ErrDrawDayNotFound", err)
	}
	if d.out.total() != 0 {
		t.Error("something was sent")
	}
}

func TestNothingIsSentBeforeConfirm(t *testing.T) {
	d := newDrawEnv(t, 3)
	d.enter(t, 5)
	if _, err := d.draw.Run(d.ctx, 10, d.date(), false); err != nil {
		t.Fatal(err)
	}
	if d.out.total() != 0 {
		t.Errorf("%d messages sent by the draw itself", d.out.total())
	}
}

func TestConfirmNotifiesWinnersOnce(t *testing.T) {
	d := newDrawEnv(t, 3)
	d.enter(t, 8)
	run, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := d.draw.Confirm(d.ctx, 10, d.date())
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 3 || !res.Done || len(res.Failed) != 0 || res.Day.Status != "notified" {
		t.Fatalf("res = %+v", res)
	}
	for _, w := range run.Winners {
		msgs := d.out.sent[w.TelegramID]
		if len(msgs) != 1 || !strings.Contains(msgs[0], "Ты в числе хедлайнеров дня") || !strings.Contains(msgs[0], "Сертификат") {
			t.Errorf("winner %d got %q", w.TelegramID, msgs)
		}
	}
	if d.out.total() != 3 {
		t.Errorf("sent %d messages, want 3", d.out.total())
	}
	if n := d.count(t, `SELECT count(*) FROM daily_winners WHERE status = 'notified'`); n != 3 {
		t.Errorf("notified winners = %d", n)
	}

	again, err := d.draw.Confirm(d.ctx, 10, d.date())
	if err != nil || again.Sent != 0 || again.Already != 3 || !again.Done {
		t.Errorf("repeat: res = %+v, err = %v", again, err)
	}
	if d.out.total() != 3 {
		t.Errorf("repeat confirm sent more messages: %d", d.out.total())
	}
	if c := d.count(t, `SELECT count(*) FROM audit_log WHERE action = 'draw_confirm'`); c != 1 {
		t.Errorf("draw_confirm audit rows = %d, want 1", c)
	}
}

func TestConfirmRetriesOnlyFailedDeliveries(t *testing.T) {
	d := newDrawEnv(t, 3)
	d.enter(t, 3)
	run, err := d.draw.Run(d.ctx, 10, d.date(), false)
	if err != nil {
		t.Fatal(err)
	}
	blocked := run.Winners[0].TelegramID
	d.out.fail[blocked] = true

	res, err := d.draw.Confirm(d.ctx, 10, d.date())
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || len(res.Failed) != 1 || res.Failed[0].TelegramID != blocked || res.Done || res.Day.Status != "confirmed" {
		t.Fatalf("res = %+v", res)
	}

	delete(d.out.fail, blocked)
	res, err = d.draw.Confirm(d.ctx, 10, d.date())
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || res.Already != 2 || !res.Done || res.Day.Status != "notified" {
		t.Errorf("retry: res = %+v", res)
	}
	for _, w := range run.Winners {
		if n := len(d.out.sent[w.TelegramID]); n != 1 {
			t.Errorf("winner %d received %d messages, want 1", w.TelegramID, n)
		}
	}
}

func TestConfirmConcurrentCallsNotifyEachWinnerOnce(t *testing.T) {
	d := newDrawEnv(t, 5)
	d.enter(t, 9)
	if _, err := d.draw.Run(d.ctx, 10, d.date(), false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.draw.Confirm(d.ctx, 10, d.date()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for id, msgs := range d.out.sent {
		if len(msgs) != 1 {
			t.Errorf("user %d notified %d times", id, len(msgs))
		}
	}
	if d.out.total() != 5 {
		t.Errorf("sent %d messages, want 5", d.out.total())
	}
}

func TestDrawResultOfUndrawnDay(t *testing.T) {
	d := newDrawEnv(t, 1)
	if _, err := d.draw.Result(d.ctx, d.date()); !errors.Is(err, service.ErrDrawNotRun) {
		t.Errorf("err = %v, want ErrDrawNotRun", err)
	}
}
