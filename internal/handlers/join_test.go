package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/domain"
	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

// sentMsg is one c.Send call.
type sentMsg struct {
	text string
	opts []any
}

// markup returns the reply markup passed to Send, if any.
func (m sentMsg) markup() *tele.ReplyMarkup {
	for _, o := range m.opts {
		if rm, ok := o.(*tele.ReplyMarkup); ok {
			return rm
		}
	}
	return nil
}

// recCtx records Send calls including their options.
type recCtx struct {
	*stubCtx
	msgs []sentMsg
}

func (r *recCtx) Send(what any, opts ...any) error {
	r.msgs = append(r.msgs, sentMsg{text: fmt.Sprint(what), opts: opts})
	return nil
}

type fakeJoiner struct {
	mu    sync.Mutex
	res   service.JoinResult
	err   error
	calls int
}

func (f *fakeJoiner) Join(context.Context, db.User, *service.ActiveDay) (service.JoinResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.res, f.err
}

type fakeCampaignStore struct{ partner db.Partner }

func (f fakeCampaignStore) GetCampaignDayByDate(_ context.Context, day pgtype.Date) (db.CampaignDay, error) {
	return db.CampaignDay{ID: 1, Day: day, PartnerID: f.partner.ID}, nil
}

func (f fakeCampaignStore) GetPartner(context.Context, int64) (db.Partner, error) {
	return f.partner, nil
}

func newJoinHandlers(t *testing.T, partner db.Partner, j Joiner, start time.Time) (*Handlers, *recordingHandler) {
	t.Helper()
	cal := domain.Calendar{Location: time.UTC}
	today := cal.DayOf(time.Now())
	if start.IsZero() {
		start = today.AddDate(0, 0, -1)
	}
	rec := &recordingHandler{}
	log := slog.New(rec)
	h := &Handlers{
		Deps: Deps{
			Log:      log,
			Entry:    j,
			Campaign: service.NewCampaign(fakeCampaignStore{partner}, cal, start, today.AddDate(0, 0, 1)),
		},
		checks: newCooldown(checkInterval),
	}
	return h, rec
}

// recordingHandler is a slog.Handler that keeps the records.
type recordingHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (r *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (r *recordingHandler) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *recordingHandler) WithGroup(string) slog.Handler            { return r }
func (r *recordingHandler) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
	return nil
}

func (r *recordingHandler) count(level slog.Level) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.recs {
		if rec.Level == level {
			n++
		}
	}
	return n
}

var testPartner = db.Partner{ID: 1, Name: "Cafe <One>", Url: "https://t.me/cafe_one"}

func joinCtx() *recCtx {
	c := newStubCtx(&tele.User{ID: 100})
	c.Set(userKey, db.User{ID: 5, TelegramID: 100})
	return &recCtx{stubCtx: c}
}

// buttons flattens the markup into label -> (url, unique).
func buttons(t *testing.T, m *tele.ReplyMarkup) map[string]tele.InlineButton {
	t.Helper()
	if m == nil {
		t.Fatal("message has no markup")
	}
	out := map[string]tele.InlineButton{}
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			out[b.Text] = b
		}
	}
	return out
}

func TestJoinDrawShowsSubscribeAndCheckButtons(t *testing.T) {
	h, _ := newJoinHandlers(t, testPartner, &fakeJoiner{}, time.Time{})
	c := joinCtx()

	if err := h.joinDraw(c); err != nil {
		t.Fatal(err)
	}
	if len(c.msgs) != 1 {
		t.Fatalf("sent %d messages", len(c.msgs))
	}
	if want := texts.JoinPrompt(testPartner.Name); c.msgs[0].text != want {
		t.Errorf("text = %q, want %q", c.msgs[0].text, want)
	}
	btns := buttons(t, c.msgs[0].markup())
	sub, ok := btns["Подписаться на Cafe <One>"]
	if !ok || sub.URL != testPartner.Url {
		t.Errorf("subscribe button = %+v (ok=%v), want URL %s", sub, ok, testPartner.Url)
	}
	if chk, ok := btns[texts.BtnCheckSub]; !ok || chk.Unique != uniqueCheckSub {
		t.Errorf("check button = %+v (ok=%v)", chk, ok)
	}
}

func TestJoinDrawWithoutPartnerURLOmitsLink(t *testing.T) {
	p := testPartner
	p.Url = ""
	h, _ := newJoinHandlers(t, p, &fakeJoiner{}, time.Time{})
	c := joinCtx()

	if err := h.joinDraw(c); err != nil {
		t.Fatal(err)
	}
	btns := buttons(t, c.msgs[0].markup())
	if len(btns) != 1 || btns[texts.BtnCheckSub].Unique != uniqueCheckSub {
		t.Errorf("buttons = %+v, want only the check button", btns)
	}
}

func TestJoinDrawBeforeCampaignStart(t *testing.T) {
	future := domain.Calendar{Location: time.UTC}.DayOf(time.Now()).AddDate(0, 0, 3)
	h, _ := newJoinHandlers(t, testPartner, &fakeJoiner{}, future)
	c := joinCtx()

	if err := h.joinDraw(c); err != nil {
		t.Fatal(err)
	}
	if want := texts.CampaignNotStarted(future); len(c.msgs) != 1 || c.msgs[0].text != want {
		t.Errorf("msgs = %+v, want %q", c.msgs, want)
	}
}

func TestCheckSubJoined(t *testing.T) {
	j := &fakeJoiner{res: service.JoinResult{Chances: 3}}
	h, _ := newJoinHandlers(t, testPartner, j, time.Time{})
	c := joinCtx()

	if err := h.checkSub(c); err != nil {
		t.Fatal(err)
	}
	got := c.msgs[0].text
	if want := texts.JoinSuccess(testPartner.Name, 3); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	for _, s := range []string{"Ты в лайнапе ✅", "Cafe &lt;One&gt;", "Шансов на главный приз: 3"} {
		if !strings.Contains(got, s) {
			t.Errorf("text %q lacks %q", got, s)
		}
	}
}

func TestCheckSubAlreadyJoined(t *testing.T) {
	h, _ := newJoinHandlers(t, testPartner, &fakeJoiner{res: service.JoinResult{Already: true, Chances: 2}}, time.Time{})
	c := joinCtx()

	if err := h.checkSub(c); err != nil {
		t.Fatal(err)
	}
	if len(c.msgs) != 1 || c.msgs[0].text != texts.AlreadyJoined {
		t.Errorf("msgs = %+v, want AlreadyJoined", c.msgs)
	}
	if !strings.HasPrefix(texts.AlreadyJoined, "Ты уже участвуешь") {
		t.Errorf("AlreadyJoined = %q", texts.AlreadyJoined)
	}
}

func TestCheckSubNotSubscribed(t *testing.T) {
	h, rec := newJoinHandlers(t, testPartner, &fakeJoiner{err: service.ErrNotSubscribed}, time.Time{})
	c := joinCtx()

	if err := h.checkSub(c); err != nil {
		t.Fatal(err)
	}
	if want := texts.NotSubscribed(testPartner.Name); c.msgs[0].text != want {
		t.Errorf("text = %q, want %q", c.msgs[0].text, want)
	}
	btns := buttons(t, c.msgs[0].markup())
	if b := btns[texts.BtnSubscribe]; b.URL != testPartner.Url {
		t.Errorf("subscribe button = %+v", b)
	}
	if b := btns[texts.BtnCheckSubAgain]; b.Unique != uniqueCheckSub {
		t.Errorf("recheck button = %+v", b)
	}
	if n := rec.count(slog.LevelError); n != 0 {
		t.Errorf("logged %d errors for an ordinary missing subscription", n)
	}
}

func TestCheckSubTelegramFailure(t *testing.T) {
	apiErr := fmt.Errorf("%w: %w", service.ErrSubscriptionCheck, errors.New("Forbidden: bot is not a member of the channel chat"))
	h, rec := newJoinHandlers(t, testPartner, &fakeJoiner{err: apiErr}, time.Time{})
	c := joinCtx()

	if err := h.checkSub(c); err != nil {
		t.Fatalf("handler returned %v; the user gets a neutral message instead", err)
	}
	if len(c.msgs) != 1 || c.msgs[0].text != "Не удалось проверить, попробуй позже" {
		t.Errorf("msgs = %+v, want the neutral failure text", c.msgs)
	}
	if strings.Contains(c.msgs[0].text, "Forbidden") {
		t.Error("API error leaked to the user")
	}
	if n := rec.count(slog.LevelError); n != 1 {
		t.Errorf("error log records = %d, want 1", n)
	}
}

func TestCheckSubUnexpectedErrorIsReturned(t *testing.T) {
	boom := errors.New("db down")
	h, _ := newJoinHandlers(t, testPartner, &fakeJoiner{err: boom}, time.Time{})
	c := joinCtx()

	if err := h.checkSub(c); !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestCheckSubFloodProtection(t *testing.T) {
	j := &fakeJoiner{err: service.ErrNotSubscribed}
	h, _ := newJoinHandlers(t, testPartner, j, time.Time{})
	now := time.Now()
	h.checks.now = func() time.Time { return now }
	c := joinCtx()

	for i := 0; i < 3; i++ { // rapid presses within the interval
		if err := h.checkSub(c); err != nil {
			t.Fatal(err)
		}
	}
	if j.calls != 1 || len(c.msgs) != 1 {
		t.Fatalf("joins = %d, messages = %d; want 1 and 1", j.calls, len(c.msgs))
	}

	now = now.Add(checkInterval) // the user may retry again
	if err := h.checkSub(c); err != nil {
		t.Fatal(err)
	}
	if j.calls != 2 || len(c.msgs) != 2 {
		t.Errorf("after the interval: joins = %d, messages = %d; want 2 and 2", j.calls, len(c.msgs))
	}
}

func TestCooldownIsPerUser(t *testing.T) {
	now := time.Now()
	cd := newCooldown(2 * time.Second)
	cd.now = func() time.Time { return now }

	if !cd.Allow(1) || cd.Allow(1) {
		t.Fatal("second action of the same user must be blocked")
	}
	if !cd.Allow(2) {
		t.Error("another user must not be blocked")
	}
	now = now.Add(1999 * time.Millisecond)
	if cd.Allow(1) {
		t.Error("still inside the interval")
	}
	now = now.Add(time.Millisecond)
	if !cd.Allow(1) {
		t.Error("interval elapsed, action must be allowed")
	}
}

func TestCooldownPrunesExpired(t *testing.T) {
	now := time.Now()
	cd := newCooldown(time.Second)
	cd.now = func() time.Time { return now }
	for i := int64(0); i < 2000; i++ {
		cd.Allow(i)
	}
	now = now.Add(time.Minute)
	cd.Allow(-1)
	if len(cd.last) > 1 {
		t.Errorf("map holds %d entries, expired ones must be dropped", len(cd.last))
	}
}

type fakeChances struct {
	view service.ChancesView
	err  error
}

func (f fakeChances) View(context.Context, int64) (service.ChancesView, error) { return f.view, f.err }

func TestChancesHandler(t *testing.T) {
	h, _ := newJoinHandlers(t, testPartner, &fakeJoiner{}, time.Time{})
	h.Chances = fakeChances{view: service.ChancesView{
		Chances: 1, Max: 10,
		Days:     []service.ActiveDayView{{Day: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Partner: "Cafe"}},
		Partners: []service.PartnerInteraction{{Name: "Cafe", Promo: true}},
	}}
	c := joinCtx()

	if err := h.chances(c); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Накоплено шансов: 1 из 10", "• 05.09 — Cafe", "• Cafe — промокод", "Осталось дней акции: 2"} {
		if !strings.Contains(c.msgs[0].text, want) {
			t.Errorf("screen lacks %q:\n%s", want, c.msgs[0].text)
		}
	}
}

func TestChancesHandlerError(t *testing.T) {
	boom := errors.New("db down")
	h, _ := newJoinHandlers(t, testPartner, &fakeJoiner{}, time.Time{})
	h.Chances = fakeChances{err: boom}
	if err := h.chances(joinCtx()); !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}
