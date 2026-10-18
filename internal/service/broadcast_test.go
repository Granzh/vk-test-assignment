package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/domain"
	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

// virtualClock lets tests run the worker without waiting: sleeping only moves time.
type virtualClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func (c *virtualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *virtualClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return ctx.Err()
}

type sendRecord struct {
	to   int64
	text string
	at   time.Time
}

// scriptedSender is a BroadcastSender whose behaviour is set per telegram id.
type scriptedSender struct {
	clock   *virtualClock
	mu      sync.Mutex
	log     []sendRecord
	blocked map[int64]bool
	flood   map[int64]time.Duration // answers 429 once
	flaky   map[int64]int           // fails this many times, then works
	broken  map[int64]bool          // always fails with an ordinary error
}

func newScriptedSender(c *virtualClock) *scriptedSender {
	return &scriptedSender{clock: c, blocked: map[int64]bool{}, flood: map[int64]time.Duration{}, flaky: map[int64]int{}, broken: map[int64]bool{}}
}

func (s *scriptedSender) SendText(_ context.Context, id int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked[id] {
		return fmt.Errorf("send to %d: %w", id, service.ErrRecipientBlocked)
	}
	if d, ok := s.flood[id]; ok {
		delete(s.flood, id)
		return &service.RateLimitError{RetryAfter: d}
	}
	if s.flaky[id] > 0 {
		s.flaky[id]--
		return errors.New("telegram: internal server error (500)")
	}
	if s.broken[id] {
		return errors.New("telegram: bad gateway (502)")
	}
	s.log = append(s.log, sendRecord{id, text, s.clock.now()})
	return nil
}

func (s *scriptedSender) sentTo() map[int64]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[int64]int{}
	for _, r := range s.log {
		m[r.to]++
	}
	return m
}

type reportBox struct {
	mu   sync.Mutex
	msgs map[int64][]string
}

func (r *reportBox) Send(_ context.Context, id int64, html string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.msgs == nil {
		r.msgs = map[int64][]string{}
	}
	r.msgs[id] = append(r.msgs[id], html)
	return nil
}

type bcEnv struct {
	*env
	clock  *virtualClock
	sender *scriptedSender
	report *reportBox
	bc     *service.Broadcast
	camp   *service.Campaign
	today  *service.ActiveDay
}

const bcAdmin = int64(9001)

// newBcEnv has an active campaign day (Sep 5) with a partner.
func newBcEnv(t *testing.T) *bcEnv {
	t.Helper()
	e := newEnv(t, 0)
	b := &bcEnv{env: e, clock: &virtualClock{t: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)}, report: &reportBox{}}
	b.today = e.day(t, 5)
	b.camp = service.NewCampaign(e.store, domain.Calendar{Location: time.UTC},
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	b.camp.SetNow(b.clock.now)
	b.sender = newScriptedSender(b.clock)
	b.bc = service.NewBroadcast(e.store, b.sender, b.report, b.camp, service.BroadcastConfig{RatePerSecond: 25, MaxAttempts: 3}, discardLog)
	b.bc.SetClock(b.clock.now, b.clock.sleep)
	return b
}

func (b *bcEnv) users(t *testing.T, n int) []db.User {
	t.Helper()
	var us []db.User
	base := b.count(t, `SELECT count(*) FROM users`)
	for i := 0; i < n; i++ {
		us = append(us, b.userRow(t, int64(1000+base+i)))
	}
	return us
}

// launch drafts and confirms a broadcast and returns its id.
func (b *bcEnv) launch(t *testing.T, segment, text string) int64 {
	t.Helper()
	info, err := b.bc.Draft(b.ctx, bcAdmin, segment, text)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.bc.Confirm(b.ctx, bcAdmin, info.ID); err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// drain runs the worker until nothing is left to do.
func (b *bcEnv) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		n, err := b.bc.Tick(b.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			// one more tick lets an empty batch finish the broadcast
			if _, err := b.bc.Tick(b.ctx); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("worker did not settle")
}

func (b *bcEnv) recipient(t *testing.T, bid int64, u db.User) db.BroadcastRecipient {
	t.Helper()
	var r db.BroadcastRecipient
	err := b.pool.QueryRow(b.ctx, `SELECT id, broadcast_id, user_id, telegram_id, status, attempts, last_error, claimed_at, sent_at FROM broadcast_recipients WHERE broadcast_id = $1 AND user_id = $2`,
		bid, u.ID).Scan(&r.ID, &r.BroadcastID, &r.UserID, &r.TelegramID, &r.Status, &r.Attempts, &r.LastError, &r.ClaimedAt, &r.SentAt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBroadcastSegments(t *testing.T) {
	b := newBcEnv(t)
	us := b.users(t, 6) // 0 blocked, 1 took promo, 2 joined the draw, 3 both, 4 and 5 nothing
	if err := b.store.SetUserBlocked(b.ctx, db.SetUserBlockedParams{TelegramID: us[0].TelegramID, IsBlocked: true}); err != nil {
		t.Fatal(err)
	}
	b.partner.ChatID = pgtype.Int8{}
	if _, err := b.pool.Exec(b.ctx, `INSERT INTO promo_codes (partner_id, code) VALUES ($1, 'A'), ($1, 'B')`, b.partner.ID); err != nil {
		t.Fatal(err)
	}
	promo := service.NewPromo(b.store, 10)
	for _, i := range []int{1, 3} {
		if _, err := promo.Issue(b.ctx, us[i].ID, b.today); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []int{2, 3} {
		if _, err := b.store.CreateDailyEntry(b.ctx, db.CreateDailyEntryParams{
			UserID: us[i].ID, CampaignDayID: b.today.Day.ID, SubscribedCheckedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}); err != nil {
			t.Fatal(err)
		}
	}

	want := map[string]int{service.SegmentAll: 5, service.SegmentNoPromo: 3, service.SegmentNoEntry: 3}
	for seg, n := range want {
		info, err := b.bc.Draft(b.ctx, bcAdmin, seg, "hello")
		if err != nil || info.Audience != n || info.Status != "draft" {
			t.Errorf("%s: info = %+v, err = %v; want audience %d", seg, info, err, n)
		}
	}
	if b.sender.sentTo() == nil || len(b.sender.log) != 0 {
		t.Error("a draft must not send anything")
	}

	// the recipients are fixed on confirmation and match the counted audience
	id := b.launch(t, service.SegmentNoPromo, "hello")
	if n := b.count(t, `SELECT count(*) FROM broadcast_recipients WHERE broadcast_id = $1`, id); n != 3 {
		t.Errorf("recipients = %d, want 3", n)
	}
	for _, i := range []int{2, 4, 5} {
		b.recipient(t, id, us[i]) // fails the test if missing
	}
}

func TestBroadcastDailySegmentNeedsAnActiveDay(t *testing.T) {
	b := newBcEnv(t)
	b.camp.SetNow(func() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) }) // no partner on Sep 9
	_, err := b.bc.Draft(b.ctx, bcAdmin, service.SegmentNoPromo, "hello")
	if !errors.Is(err, service.ErrSegmentUnavailable) {
		t.Errorf("err = %v, want ErrSegmentUnavailable", err)
	}
	if _, err := b.bc.Draft(b.ctx, bcAdmin, service.SegmentAll, "hello"); err != nil {
		t.Errorf("the all segment needs no day: %v", err)
	}
}

func TestBroadcastDraftValidation(t *testing.T) {
	b := newBcEnv(t)
	if _, err := b.bc.Draft(b.ctx, bcAdmin, "everyone", "hello"); !errors.Is(err, service.ErrBroadcastBadSegment) {
		t.Errorf("segment: %v", err)
	}
	for _, text := range []string{"", "   \n", strings.Repeat("я", service.MaxBroadcastText+1)} {
		if _, err := b.bc.Draft(b.ctx, bcAdmin, service.SegmentAll, text); !errors.Is(err, service.ErrBroadcastBadText) {
			t.Errorf("text of %d runes: %v", len([]rune(text)), err)
		}
	}
	if _, err := b.bc.Draft(b.ctx, bcAdmin, service.SegmentAll, strings.Repeat("я", service.MaxBroadcastText)); err != nil {
		t.Errorf("max length: %v", err)
	}
}

func TestBroadcastSendsToEveryoneAndReports(t *testing.T) {
	b := newBcEnv(t)
	us := b.users(t, 7)
	id := b.launch(t, service.SegmentAll, "  Привет!  ")
	b.drain(t)

	sent := b.sender.sentTo()
	for _, u := range us {
		if sent[u.TelegramID] != 1 {
			t.Errorf("user %d got %d messages", u.TelegramID, sent[u.TelegramID])
		}
	}
	if b.sender.log[0].text != "Привет!" {
		t.Errorf("text = %q", b.sender.log[0].text)
	}
	rep, err := b.bc.Report(b.ctx, id)
	if err != nil || rep.Status != "done" || rep.Total != 7 || rep.Sent != 7 || rep.Failed != 0 || rep.Blocked != 0 || rep.Pending != 0 {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	msgs := b.report.msgs[bcAdmin]
	if len(msgs) != 1 || !strings.Contains(msgs[0], "завершена") || !strings.Contains(msgs[0], "Отправлено: 7") {
		t.Errorf("report messages = %q", msgs)
	}
	// finished broadcasts are not touched again
	b.drain(t)
	if len(b.report.msgs[bcAdmin]) != 1 || len(b.sender.log) != 7 {
		t.Error("a finished broadcast was processed again")
	}
}

func TestBroadcastDoubleConfirmSendsOnce(t *testing.T) {
	b := newBcEnv(t)
	b.users(t, 3)
	info, err := b.bc.Draft(b.ctx, bcAdmin, service.SegmentAll, "hi")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = b.bc.Confirm(b.ctx, bcAdmin, info.ID)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, service.ErrBroadcastState) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d confirmations succeeded, want 1", ok)
	}
	b.drain(t)
	if len(b.sender.log) != 3 {
		t.Errorf("sent %d messages, want 3", len(b.sender.log))
	}
}

func TestBroadcastRateNeverExceedsLimit(t *testing.T) {
	b := newBcEnv(t)
	b.users(t, 80)
	b.launch(t, service.SegmentAll, "hi")
	b.drain(t)

	log := b.sender.log
	if len(log) != 80 {
		t.Fatalf("sent %d, want 80", len(log))
	}
	// at most 25 messages in any window of one second
	for i := 0; i+25 < len(log); i++ {
		if gap := log[i+25].at.Sub(log[i].at); gap < time.Second {
			t.Fatalf("messages %d..%d went out within %s, more than 25 per second", i, i+25, gap)
		}
	}
	if span := log[79].at.Sub(log[0].at); span < 79*40*time.Millisecond {
		t.Errorf("80 messages took only %s", span)
	}
}

func TestBroadcastWaitsOutTelegram429(t *testing.T) {
	b := newBcEnv(t)
	us := b.users(t, 4)
	b.sender.flood[us[1].TelegramID] = 7 * time.Second
	id := b.launch(t, service.SegmentAll, "hi")
	b.drain(t)

	slept := false
	for _, d := range b.clock.sleeps {
		if d >= 7*time.Second {
			slept = true
		}
	}
	if !slept {
		t.Errorf("the worker did not wait retry_after: sleeps = %v", b.clock.sleeps)
	}
	rep, _ := b.bc.Report(b.ctx, id)
	if rep.Sent != 4 || rep.Failed != 0 {
		t.Errorf("report = %+v; a 429 must delay, not lose, a message", rep)
	}
	// nobody was skipped or doubled, and the delayed user got their message after the pause
	for _, u := range us {
		if b.sender.sentTo()[u.TelegramID] != 1 {
			t.Errorf("user %d got %d messages", u.TelegramID, b.sender.sentTo()[u.TelegramID])
		}
	}
	if r := b.recipient(t, id, us[1]); r.Attempts != 1 {
		t.Errorf("attempts = %d; a 429 does not use up an attempt", r.Attempts)
	}
}

func TestBroadcastBlockedUsersAreMarkedAndExcludedLater(t *testing.T) {
	b := newBcEnv(t)
	us := b.users(t, 5)
	b.sender.blocked[us[2].TelegramID] = true
	id := b.launch(t, service.SegmentAll, "first")
	b.drain(t)

	rep, _ := b.bc.Report(b.ctx, id)
	if rep.Sent != 4 || rep.Blocked != 1 || rep.Failed != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if r := b.recipient(t, id, us[2]); r.Status != "blocked" || r.Attempts != 1 {
		t.Errorf("recipient = %+v", r)
	}
	u, _ := b.store.GetUserByTelegramID(b.ctx, us[2].TelegramID)
	if !u.IsBlocked {
		t.Error("the user was not marked as blocked")
	}
	if msgs := b.report.msgs[bcAdmin]; len(msgs) != 1 || !strings.Contains(msgs[0], "Заблокировали бота: 1") {
		t.Errorf("report = %q", msgs)
	}

	// the next broadcast skips them
	info, err := b.bc.Draft(b.ctx, bcAdmin, service.SegmentAll, "second")
	if err != nil || info.Audience != 4 {
		t.Errorf("audience = %d, want 4 (err %v)", info.Audience, err)
	}
	b.sender.blocked[us[2].TelegramID] = false
	id2 := b.launch(t, service.SegmentAll, "second")
	b.drain(t)
	if n := b.count(t, `SELECT count(*) FROM broadcast_recipients WHERE broadcast_id = $1 AND user_id = $2`, id2, us[2].ID); n != 0 {
		t.Error("a blocked user was queued again")
	}

	// a user who writes to the bot again is reachable again
	if _, err := b.store.UpsertUser(b.ctx, db.UpsertUserParams{TelegramID: us[2].TelegramID}); err != nil {
		t.Fatal(err)
	}
	if info, _ := b.bc.Draft(b.ctx, bcAdmin, service.SegmentAll, "third"); info.Audience != 5 {
		t.Errorf("audience after the user came back = %d, want 5", info.Audience)
	}
}

func TestBroadcastOrdinaryErrorsAreRetriedThenFail(t *testing.T) {
	b := newBcEnv(t)
	us := b.users(t, 3)
	b.sender.flaky[us[0].TelegramID] = 2 // recovers on the third try
	b.sender.broken[us[1].TelegramID] = true
	id := b.launch(t, service.SegmentAll, "hi")
	b.drain(t)

	if r := b.recipient(t, id, us[0]); r.Status != "sent" || r.Attempts != 3 {
		t.Errorf("flaky recipient = %+v", r)
	}
	r := b.recipient(t, id, us[1])
	if r.Status != "failed" || r.Attempts != 3 || !r.LastError.Valid || !strings.Contains(r.LastError.String, "502") {
		t.Errorf("broken recipient = %+v", r)
	}
	rep, _ := b.bc.Report(b.ctx, id)
	if rep.Sent != 2 || rep.Failed != 1 || rep.Blocked != 0 || rep.Status != "done" {
		t.Errorf("report = %+v", rep)
	}
	u, _ := b.store.GetUserByTelegramID(b.ctx, us[1].TelegramID)
	if u.IsBlocked {
		t.Error("a technical failure must not mark the user as blocked")
	}
}

func TestBroadcastCancel(t *testing.T) {
	b := newBcEnv(t)
	b.users(t, 60)
	id := b.launch(t, service.SegmentAll, "hi")
	if n, err := b.bc.Tick(b.ctx); err != nil || n != 25 { // one batch goes out
		t.Fatalf("tick = %d, %v", n, err)
	}
	rep, err := b.bc.Cancel(b.ctx, bcAdmin, id)
	if err != nil || rep.Status != "cancelled" || rep.Sent != 25 || rep.Pending != 35 {
		t.Fatalf("cancel = %+v, %v", rep, err)
	}
	b.drain(t)
	if len(b.sender.log) != 25 {
		t.Errorf("sent %d messages after the cancellation, want it to stay at 25", len(b.sender.log))
	}
	if _, err := b.bc.Cancel(b.ctx, bcAdmin, id); !errors.Is(err, service.ErrBroadcastState) {
		t.Errorf("second cancel: %v", err)
	}
	if _, err := b.bc.Confirm(b.ctx, bcAdmin, id); !errors.Is(err, service.ErrBroadcastState) {
		t.Errorf("confirm after cancel: %v", err)
	}
	if len(b.report.msgs[bcAdmin]) != 0 {
		t.Error("a cancelled broadcast must not send the completion report")
	}
}

func TestBroadcastCancelDraftAndUnknown(t *testing.T) {
	b := newBcEnv(t)
	info, _ := b.bc.Draft(b.ctx, bcAdmin, service.SegmentAll, "hi")
	if rep, err := b.bc.Cancel(b.ctx, bcAdmin, info.ID); err != nil || rep.Status != "cancelled" {
		t.Errorf("cancel draft = %+v, %v", rep, err)
	}
	if _, err := b.bc.Cancel(b.ctx, bcAdmin, 999); !errors.Is(err, service.ErrBroadcastNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := b.bc.Confirm(b.ctx, bcAdmin, 999); !errors.Is(err, service.ErrBroadcastNotFound) {
		t.Errorf("unknown confirm: %v", err)
	}
	if _, err := b.bc.Report(b.ctx, 999); !errors.Is(err, service.ErrBroadcastNotFound) {
		t.Errorf("unknown report: %v", err)
	}
}

func TestBroadcastRecoversRecipientsOfADeadWorker(t *testing.T) {
	b := newBcEnv(t)
	us := b.users(t, 3)
	id := b.launch(t, service.SegmentAll, "hi")
	// a worker claimed a recipient and died before recording the outcome
	if _, err := b.pool.Exec(b.ctx, `UPDATE broadcast_recipients SET status = 'sending', attempts = 1, claimed_at = now() - interval '10 minutes' WHERE user_id = $1`, us[0].ID); err != nil {
		t.Fatal(err)
	}
	b.drain(t)
	rep, _ := b.bc.Report(b.ctx, id)
	if rep.Sent != 3 || rep.Status != "done" {
		t.Errorf("report = %+v", rep)
	}
}

func TestBroadcastRestartContinues(t *testing.T) {
	b := newBcEnv(t)
	b.users(t, 40)
	id := b.launch(t, service.SegmentAll, "hi")
	if _, err := b.bc.Tick(b.ctx); err != nil { // 25 sent, then the "process" stops
		t.Fatal(err)
	}
	restarted := service.NewBroadcast(b.store, b.sender, b.report, b.camp, service.BroadcastConfig{RatePerSecond: 25}, discardLog)
	restarted.SetClock(b.clock.now, b.clock.sleep)
	for i := 0; i < 5; i++ {
		if _, err := restarted.Tick(b.ctx); err != nil {
			t.Fatal(err)
		}
	}
	rep, _ := restarted.Report(b.ctx, id)
	if rep.Sent != 40 || rep.Status != "done" || len(b.sender.log) != 40 {
		t.Errorf("report = %+v, sent %d", rep, len(b.sender.log))
	}
}

func TestBroadcastNoRecipients(t *testing.T) {
	b := newBcEnv(t)
	id := b.launch(t, service.SegmentAll, "hi")
	b.drain(t)
	rep, _ := b.bc.Report(b.ctx, id)
	if rep.Status != "done" || rep.Total != 0 {
		t.Errorf("report = %+v", rep)
	}
}

func TestBroadcastReportOfLatestAndAudit(t *testing.T) {
	b := newBcEnv(t)
	b.users(t, 2)
	b.launch(t, service.SegmentAll, "one")
	id2 := b.launch(t, service.SegmentAll, "two")
	rep, err := b.bc.Report(b.ctx, 0)
	if err != nil || rep.ID != id2 {
		t.Errorf("latest = %+v, %v", rep, err)
	}
	b.drain(t)
	for _, action := range []string{"broadcast_draft", "broadcast_confirm", "broadcast_done"} {
		if n := b.count(t, `SELECT count(*) FROM audit_log WHERE action = $1`, action); n != 2 {
			t.Errorf("%s audit rows = %d, want 2", action, n)
		}
	}
}
