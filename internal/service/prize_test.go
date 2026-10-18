package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

// plainOutbox adds plain Send to the button outbox so one fake serves both interfaces.
type duplexOutbox struct {
	*buttonOutbox
	mu    sync.Mutex
	plain map[int64][]string
}

func (d *duplexOutbox) Send(_ context.Context, id int64, html string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.plain[id] = append(d.plain[id], html)
	return nil
}

type prizeEnv struct {
	*finalEnv
	duplex *duplexOutbox
	prize  *service.FinalPrize
	admin  []string
	mu     sync.Mutex
}

// newPrizeEnv creates holders with telegram ids 101.., holds and confirms the
// final draw. With weights all equal, who wins is random, so tests look the
// winners up by status rather than by id.
func newPrizeEnv(t *testing.T, winners, reserves, holders int) *prizeEnv {
	t.Helper()
	f := newFinalEnv(t, winners, reserves)
	for i := 0; i < holders; i++ {
		f.holder(t, int64(101+i), 3)
	}
	p := &prizeEnv{finalEnv: f}
	p.duplex = &duplexOutbox{buttonOutbox: f.out, plain: map[int64][]string{}}
	p.prize = service.NewFinalPrize(f.store, p.duplex, f.camp,
		service.PrizeConfig{Prize: "главный приз", ResponseTimeout: 48 * time.Hour}, discardLog)
	p.prize.NotifyAdmins = func(_ context.Context, html string) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.admin = append(p.admin, html)
	}
	if _, err := f.final.Run(f.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.final.Confirm(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	return p
}

// winnerIDs returns the user rows of the (non-reserve) winners and reserves in queue order.
func (p *prizeEnv) people(t *testing.T) (winners, reserves []db.User) {
	t.Helper()
	res, err := p.final.Result(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Winners {
		u, err := p.store.GetUserByTelegramID(p.ctx, w.TelegramID)
		if err != nil {
			t.Fatal(err)
		}
		if w.Reserve {
			reserves = append(reserves, u)
		} else {
			winners = append(winners, u)
		}
	}
	return winners, reserves
}

func (p *prizeEnv) row(t *testing.T, u db.User) db.FinalWinner {
	t.Helper()
	w, err := p.store.GetFinalWinnerOfUser(p.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (p *prizeEnv) expire(t *testing.T, u db.User) {
	t.Helper()
	if _, err := p.pool.Exec(p.ctx, `UPDATE final_winners SET deadline = now() - interval '1 minute' WHERE user_id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
}

func (p *prizeEnv) adminTexts() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.admin, "\n---\n")
}

// claim runs the whole dialog for a user up to and including the consent answer.
func (p *prizeEnv) fillIn(t *testing.T, u db.User, name string) {
	t.Helper()
	if c, err := p.prize.Start(p.ctx, u.ID); err != nil || c.Kind != service.ClaimAskName {
		t.Fatalf("Start = %+v, %v", c, err)
	}
	if c, err := p.prize.SubmitName(p.ctx, u.ID, name); err != nil || c.Kind != service.ClaimAskConsent {
		t.Fatalf("SubmitName = %+v, %v", c, err)
	}
}

func TestPrizeWinnerRespondsInTime(t *testing.T) {
	p := newPrizeEnv(t, 1, 1, 3)
	winners, reserves := p.people(t)
	w := winners[0]

	if c, err := p.prize.State(p.ctx, w.ID); err != nil || c.Kind != service.ClaimNone {
		t.Fatalf("before the button: %+v, %v (no dialog yet)", c, err)
	}
	if c, err := p.prize.Start(p.ctx, w.ID); err != nil || c.Kind != service.ClaimAskName {
		t.Fatalf("Start = %+v, %v", c, err)
	}
	if _, err := p.prize.SubmitName(p.ctx, w.ID, "Иван"); !errors.Is(err, service.ErrBadContactName) {
		t.Errorf("one word accepted: %v", err)
	}
	if c, _ := p.prize.State(p.ctx, w.ID); c.Kind != service.ClaimAskName {
		t.Errorf("a bad name must not advance the dialog, state = %+v", c)
	}
	if c, err := p.prize.SubmitName(p.ctx, w.ID, "  Иван   Петров-Водкин "); err != nil || c.Kind != service.ClaimAskConsent || c.Name != "Иван Петров-Водкин" {
		t.Fatalf("SubmitName = %+v, %v", c, err)
	}

	// the dialog lives in the database: a brand new service instance (a restart) continues it
	restarted := service.NewFinalPrize(p.store, p.duplex, p.camp, service.PrizeConfig{Prize: "x", ResponseTimeout: 48 * time.Hour}, discardLog)
	if c, err := restarted.State(p.ctx, w.ID); err != nil || c.Kind != service.ClaimAskConsent || c.Name != "Иван Петров-Водкин" {
		t.Fatalf("after restart: %+v, %v", c, err)
	}
	// a name without a consent is not exposed to the admins
	if res, _ := p.final.Result(p.ctx); res.Winners[0].Contact != "" {
		t.Errorf("name exposed before the consent: %q", res.Winners[0].Contact)
	}

	before := time.Now()
	if c, err := restarted.Consent(p.ctx, w.ID, true); err != nil || c.Kind != service.ClaimDone {
		t.Fatalf("Consent = %+v, %v", c, err)
	}
	row := p.row(t, w)
	if row.Status != "contact_received" || row.ContactName.String != "Иван Петров-Водкин" || row.DialogStep.Valid ||
		!row.ConsentGivenAt.Valid || row.ConsentGivenAt.Time.Before(before.Add(-time.Minute)) || row.ConsentGivenAt.Time.After(time.Now().Add(time.Minute)) {
		t.Errorf("stored = %+v", row)
	}
	res, _ := p.final.Result(p.ctx)
	if res.Winners[0].Contact != "Иван Петров-Водкин" || res.Winners[0].ConsentAt.IsZero() {
		t.Errorf("admin view = %+v", res.Winners[0])
	}

	// done is final: repeated presses, and even a passed deadline, change nothing
	if c, _ := p.prize.Start(p.ctx, w.ID); c.Kind != service.ClaimDone {
		t.Errorf("Start after done = %+v", c)
	}
	p.expire(t, w)
	if n, err := p.prize.SweepExpired(p.ctx); err != nil || n != 0 {
		t.Errorf("sweep expired %d (err %v) a completed claim", n, err)
	}
	if got := p.row(t, w); got.Status != "contact_received" {
		t.Errorf("status = %s", got.Status)
	}
	// the reserve was never bothered
	if len(p.out.sent[reserves[0].TelegramID]) != 0 || p.adminTexts() != "" {
		t.Errorf("reserve contacted: %v, admin messages: %q", p.out.sent[reserves[0].TelegramID], p.adminTexts())
	}
}

func TestPrizeNoResponsePassesToReserve(t *testing.T) {
	p := newPrizeEnv(t, 1, 2, 4)
	winners, reserves := p.people(t)
	w, r1, r2 := winners[0], reserves[0], reserves[1]

	p.fillIn(t, w, "Иван Петров") // the winner started but never agreed
	p.expire(t, w)

	n, err := p.prize.SweepExpired(p.ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	old := p.row(t, w)
	if old.Status != "forfeited" || old.ForfeitReason.String != "expired" || old.ContactName.Valid || old.ConsentGivenAt.Valid || old.DialogStep.Valid {
		t.Errorf("old winner = %+v; the prize must be taken away together with the personal data", old)
	}
	if msgs := p.duplex.plain[w.TelegramID]; len(msgs) != 1 || !strings.Contains(msgs[0], "Срок") {
		t.Errorf("old winner was told %q", msgs)
	}

	// the first reserve got the prize with a fresh deadline and the button
	next := p.row(t, r1)
	if next.Status != "notified" || !next.PromotedAt.Valid || next.Deadline.Time.Before(time.Now().Add(47*time.Hour)) {
		t.Errorf("reserve 1 = %+v", next)
	}
	msgs := p.out.sent[r1.TelegramID]
	if len(msgs) != 1 || msgs[0].unique != service.UniqueGetPrize || !strings.Contains(msgs[0].html, "главный приз") {
		t.Errorf("reserve 1 was sent %+v", msgs)
	}
	if got := p.row(t, r2); got.Status != "selected" || got.PromotedAt.Valid {
		t.Errorf("reserve 2 must wait: %+v", got)
	}
	if !strings.Contains(p.adminTexts(), "не ответил в срок") || !strings.Contains(p.adminTexts(), "резервисту №1") {
		t.Errorf("admin messages: %q", p.adminTexts())
	}

	// the old winner can no longer claim
	if c, _ := p.prize.Start(p.ctx, w.ID); c.Kind != service.ClaimForfeited || c.Reason != "expired" {
		t.Errorf("Start by the old winner = %+v", c)
	}
	if c, _ := p.prize.Consent(p.ctx, w.ID, true); c.Kind != service.ClaimForfeited {
		t.Errorf("Consent by the old winner = %+v", c)
	}

	// the promoted reserve can claim it
	p.fillIn(t, r1, "Мария Сидорова")
	if c, err := p.prize.Consent(p.ctx, r1.ID, true); err != nil || c.Kind != service.ClaimDone {
		t.Fatalf("reserve consent = %+v, %v", c, err)
	}

	// the sweeper is idempotent
	if n, _ := p.prize.SweepExpired(p.ctx); n != 0 {
		t.Errorf("second sweep expired %d", n)
	}
}

func TestPrizeQueueRunsOutOfReserves(t *testing.T) {
	p := newPrizeEnv(t, 1, 1, 3)
	winners, reserves := p.people(t)

	p.expire(t, winners[0])
	if n, _ := p.prize.SweepExpired(p.ctx); n != 1 {
		t.Fatalf("first sweep = %d", n)
	}
	p.expire(t, reserves[0])
	if n, _ := p.prize.SweepExpired(p.ctx); n != 1 {
		t.Fatalf("second sweep = %d", n)
	}
	if !strings.Contains(p.adminTexts(), "Резерв закончился") {
		t.Errorf("admins were not warned that the queue is empty: %q", p.adminTexts())
	}
	if got := p.row(t, reserves[0]); got.Status != "forfeited" {
		t.Errorf("last reserve = %+v", got)
	}
}

func TestPrizeUndeliverableReserveIsSkipped(t *testing.T) {
	p := newPrizeEnv(t, 1, 2, 4)
	winners, reserves := p.people(t)
	p.out.fail[reserves[0].TelegramID] = true // blocked the bot

	p.expire(t, winners[0])
	if n, err := p.prize.SweepExpired(p.ctx); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if got := p.row(t, reserves[0]); got.Status != "forfeited" || got.ForfeitReason.String != "undeliverable" {
		t.Errorf("reserve 1 = %+v", got)
	}
	if got := p.row(t, reserves[1]); got.Status != "notified" || !got.PromotedAt.Valid {
		t.Errorf("reserve 2 = %+v, must get the prize", got)
	}
	if !strings.Contains(p.adminTexts(), "не удалось доставить") || !strings.Contains(p.adminTexts(), "резервисту №2") {
		t.Errorf("admin messages: %q", p.adminTexts())
	}
}

func TestPrizeDeclinedConsent(t *testing.T) {
	p := newPrizeEnv(t, 1, 1, 3)
	winners, reserves := p.people(t)
	w := winners[0]

	p.fillIn(t, w, "Иван Петров")
	c, err := p.prize.Consent(p.ctx, w.ID, false)
	if err != nil || c.Kind != service.ClaimForfeited || c.Reason != "declined" {
		t.Fatalf("Consent(false) = %+v, %v", c, err)
	}
	row := p.row(t, w)
	if row.Status != "forfeited" || row.ForfeitReason.String != "declined" || row.ContactName.Valid || row.ConsentGivenAt.Valid || row.DialogStep.Valid {
		t.Errorf("after refusal: %+v; no data may remain and no prize is issued", row)
	}
	// the prize moves on immediately, without waiting for the sweeper
	if got := p.row(t, reserves[0]); got.Status != "notified" || !got.PromotedAt.Valid {
		t.Errorf("reserve = %+v", got)
	}
	if msgs := p.out.sent[reserves[0].TelegramID]; len(msgs) != 1 {
		t.Errorf("reserve was sent %d messages", len(msgs))
	}
	if !strings.Contains(p.adminTexts(), "отказался") {
		t.Errorf("admin messages: %q", p.adminTexts())
	}
	if n := p.count(t, `SELECT count(*) FROM final_winners WHERE status = 'contact_received'`); n != 0 {
		t.Errorf("a prize was registered without consent")
	}
	// refusing again is a no-op
	if c, _ := p.prize.Consent(p.ctx, w.ID, false); c.Kind != service.ClaimForfeited {
		t.Errorf("repeat refusal = %+v", c)
	}
	if len(p.out.sent[reserves[0].TelegramID]) != 1 {
		t.Error("a repeated refusal must not move the prize again")
	}
}

func TestPrizeAnswersOutOfOrderAreIgnored(t *testing.T) {
	p := newPrizeEnv(t, 1, 1, 3)
	winners, reserves := p.people(t)
	w := winners[0]

	// consent before the dialog started
	if c, _ := p.prize.Consent(p.ctx, w.ID, true); c.Kind != service.ClaimReady {
		t.Errorf("consent without a dialog = %+v", c)
	}
	// consent while the name is still expected
	if c, err := p.prize.Start(p.ctx, w.ID); err != nil || c.Kind != service.ClaimAskName {
		t.Fatal(c, err)
	}
	if c, _ := p.prize.Consent(p.ctx, w.ID, true); c.Kind != service.ClaimAskName {
		t.Errorf("consent at the name step = %+v", c)
	}
	if row := p.row(t, w); row.Status != "notified" || row.ConsentGivenAt.Valid {
		t.Errorf("row = %+v", row)
	}
	// pressing the button again repeats the question
	if c, _ := p.prize.Start(p.ctx, w.ID); c.Kind != service.ClaimAskName {
		t.Errorf("second Start = %+v", c)
	}
	// a reserve who has not been promoted has nothing to claim
	if c, _ := p.prize.Start(p.ctx, reserves[0].ID); c.Kind != service.ClaimNone {
		t.Errorf("reserve Start = %+v", c)
	}
	// somebody who never won
	stranger := p.userRow(t, 555)
	if c, err := p.prize.Start(p.ctx, stranger.ID); err != nil || c.Kind != service.ClaimNone {
		t.Errorf("stranger Start = %+v, %v", c, err)
	}
	if c, err := p.prize.SubmitName(p.ctx, stranger.ID, "Анна Иванова"); err != nil || c.Kind != service.ClaimNone {
		t.Errorf("stranger SubmitName = %+v, %v", c, err)
	}
}

func TestPrizeLateAnswerBeforeSweepIsRejected(t *testing.T) {
	p := newPrizeEnv(t, 1, 1, 3)
	winners, _ := p.people(t)
	w := winners[0]

	p.fillIn(t, w, "Иван Петров")
	p.expire(t, w) // the deadline passed, the sweeper has not run yet
	if c, err := p.prize.Consent(p.ctx, w.ID, true); err != nil || c.Kind != service.ClaimExpired {
		t.Errorf("late consent = %+v, %v", c, err)
	}
	if row := p.row(t, w); row.Status != "notified" || row.ConsentGivenAt.Valid {
		t.Errorf("a late consent was recorded: %+v", row)
	}
	if c, _ := p.prize.State(p.ctx, w.ID); c.Kind != service.ClaimExpired {
		t.Errorf("State = %+v", c)
	}
}

func TestPrizeConcurrentSweepsPassTheBatonOnce(t *testing.T) {
	p := newPrizeEnv(t, 1, 3, 5)
	winners, reserves := p.people(t)
	p.expire(t, winners[0])

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.prize.SweepExpired(p.ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	promoted := 0
	for _, r := range reserves {
		if p.row(t, r).PromotedAt.Valid {
			promoted++
		}
	}
	if promoted != 1 {
		t.Errorf("%d reserves got the prize, want exactly 1", promoted)
	}
	if n := len(p.duplex.plain[winners[0].TelegramID]); n != 1 {
		t.Errorf("the old winner was told %d times", n)
	}
}

func TestPurgeContacts(t *testing.T) {
	p := newPrizeEnv(t, 1, 1, 3)
	winners, _ := p.people(t)
	w := winners[0]
	p.fillIn(t, w, "Иван Петров")
	if _, err := p.prize.Consent(p.ctx, w.ID, true); err != nil {
		t.Fatal(err)
	}

	p.camp.SetNow(func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }) // last campaign day
	if _, err := p.prize.PurgeContacts(p.ctx, 9, true); !errors.Is(err, service.ErrPurgeTooEarly) {
		t.Fatalf("err = %v, want ErrPurgeTooEarly", err)
	}
	if !p.row(t, w).ContactName.Valid {
		t.Fatal("purged during the campaign")
	}

	p.camp.SetNow(func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) })
	dry, err := p.prize.PurgeContacts(p.ctx, 9, false)
	if err != nil || dry.Stored != 1 || dry.Purged != 0 || !p.row(t, w).ContactName.Valid {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	res, err := p.prize.PurgeContacts(p.ctx, 9, true)
	if err != nil || res.Stored != 1 || res.Purged != 1 {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	row := p.row(t, w)
	if row.ContactName.Valid || !row.ContactPurgedAt.Valid || row.Status != "contact_received" || !row.ConsentGivenAt.Valid {
		t.Errorf("after purge: %+v (name gone; consent record and status stay)", row)
	}
	if again, _ := p.prize.PurgeContacts(p.ctx, 9, true); again.Stored != 0 || again.Purged != 0 {
		t.Errorf("second purge = %+v", again)
	}
	if n := p.count(t, `SELECT count(*) FROM audit_log WHERE action = 'contacts_purge' AND admin_telegram_id = 9`); n != 2 {
		t.Errorf("audit rows = %d, want 2", n)
	}
}

func TestPurgeResetsADialogWaitingForConsent(t *testing.T) {
	p := newPrizeEnv(t, 1, 0, 2)
	winners, _ := p.people(t)
	p.fillIn(t, winners[0], "Иван Петров") // consent still pending
	p.camp.SetNow(func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) })
	if res, err := p.prize.PurgeContacts(p.ctx, 9, true); err != nil || res.Purged != 1 {
		t.Fatalf("purge = %+v, %v", res, err)
	}
	row := p.row(t, winners[0])
	if row.ContactName.Valid || row.DialogStep.Valid {
		t.Errorf("row = %+v; an orphaned consent step must not remain", row)
	}
}

func TestParseContactName(t *testing.T) {
	good := map[string]string{
		"Иван Петров":          "Иван Петров",
		"  анна   иванова  ":   "анна иванова",
		"Анна-Мария Ли":        "Анна-Мария Ли",
		"John O'Brien":         "John O'Brien",
		"Петров И. И.":         "Петров И. И.",
		"Иван Иванович Петров": "Иван Иванович Петров",
	}
	for in, want := range good {
		if got, ok := service.ParseContactName(in); !ok || got != want {
			t.Errorf("%q: got (%q, %v), want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "Иван", "   ", "Иван 123", "Иван @ivan", "http://x.y z", "- -", "/start", "a b c d e f g", strings.Repeat("я", 60) + " " + strings.Repeat("я", 60)} {
		if got, ok := service.ParseContactName(in); ok {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}
