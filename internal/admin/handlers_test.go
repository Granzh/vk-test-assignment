package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

// fakeBackend records calls and returns canned results.
type fakeBackend struct {
	calls    []string
	err      error
	partner  db.Partner
	uploaded *PromoParse
	uploadTo int64
	file     *ExportFile
	edited   bool
}

func (f *fakeBackend) Audit(context.Context, int64, string, map[string]any) error {
	f.calls = append(f.calls, "Audit")
	return f.err
}

func (f *fakeBackend) AddPartner(_ context.Context, _ int64, in PartnerInput) (db.Partner, error) {
	f.calls = append(f.calls, "AddPartner")
	f.partner = db.Partner{ID: 7, Name: in.Name}
	return f.partner, f.err
}

func (f *fakeBackend) ListPartners(context.Context, int64) ([]db.Partner, error) {
	f.calls = append(f.calls, "ListPartners")
	return []db.Partner{{ID: 1, Name: "No chat", Url: "https://t.me/x"}, {ID: 2, Name: "With chat", ChatID: pgtype.Int8{Int64: -100, Valid: true}}}, f.err
}

func (f *fakeBackend) SetDay(_ context.Context, _ int64, in DayInput) (DaySetResult, error) {
	f.calls = append(f.calls, "SetDay")
	return DaySetResult{Day: db.CampaignDay{ID: 9}, Partner: db.Partner{Name: "P"}, Created: true}, f.err
}

func (f *fakeBackend) UploadPromo(_ context.Context, _ int64, partnerID int64, parsed *PromoParse) (PromoReport, error) {
	f.calls = append(f.calls, "UploadPromo")
	f.uploadTo, f.uploaded = partnerID, parsed
	return PromoReport{Partner: db.Partner{ID: partnerID, Name: "P"}, Rows: parsed.Rows, Loaded: len(parsed.Codes)}, f.err
}

func (f *fakeBackend) PromoStats(context.Context, int64) ([]db.ListPromoStatsRow, error) {
	f.calls = append(f.calls, "PromoStats")
	return []db.ListPromoStatsRow{{ID: 1, Name: "A", Total: 10, Free: 4}, {ID: 2, Name: "B"}}, f.err
}

func (f *fakeBackend) Export(context.Context, int64, ExportRequest) (*ExportFile, error) {
	f.calls = append(f.calls, "Export")
	return f.file, f.err
}

// route wires a handler the way Register does: behind the guard.
func route(admins []int64, h tele.HandlerFunc) tele.HandlerFunc {
	return Guard(admins, discard)(h)
}

func newHandlers(b Backend) *handlers {
	return &handlers{Deps: Deps{Log: discard, Backend: b, Location: time.UTC}}
}

func TestNonAdminReachesNoCommand(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	commands := map[string]tele.HandlerFunc{
		"/admin": h.menu, "/partner_add": h.partnerAdd, "/partner_list": h.partnerList, "/day_set": h.daySet,
		"/promo_upload": h.promoUploadHint, "document": h.promoUpload, "/promo_stats": h.promoStats, "/export": h.export,
	}
	for name, handler := range commands {
		c := adminCtx(99, "x | https://t.me/x | -1 | t")
		c.msg.Caption = "/promo_upload 1"
		c.msg.Document = &tele.Document{}
		if err := route([]int64{10}, handler)(c); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(c.sent) != 0 {
			t.Errorf("%s: non-admin received %d replies", name, len(c.sent))
		}
	}
	if len(be.calls) != 0 {
		t.Errorf("backend was called by a non-admin: %v", be.calls)
	}
}

func TestAdminCommandsReachBackend(t *testing.T) {
	be := &fakeBackend{file: &ExportFile{Name: "x.csv", Data: []byte("a"), Rows: 0}}
	h := newHandlers(be)
	run := func(handler tele.HandlerFunc, payload string) *fakeCtx {
		c := adminCtx(10, payload)
		if err := route([]int64{10}, handler)(c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	run(h.menu, "")
	run(h.partnerAdd, "P | https://t.me/p | -100 | t")
	run(h.partnerList, "")
	run(h.daySet, "2026-09-05 | 1 | 1 | prize")
	run(h.promoStats, "")
	run(h.export, "participants 1")
	want := "Audit AddPartner ListPartners SetDay PromoStats Export"
	if got := strings.Join(be.calls, " "); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestCommandsWithoutArgumentsShowUsage(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	for name, handler := range map[string]tele.HandlerFunc{"partner_add": h.partnerAdd, "day_set": h.daySet} {
		c := adminCtx(10, "  ")
		if err := handler(c); err != nil {
			t.Fatal(err)
		}
		if len(c.sent) != 1 || !strings.Contains(c.sent[0].text(), "Пример") {
			t.Errorf("%s: sent = %+v, want usage with an example", name, c.sent)
		}
	}
	if len(be.calls) != 0 {
		t.Errorf("backend called: %v", be.calls)
	}
}

func TestInvalidInputIsExplainedAndNotExecuted(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	c := adminCtx(10, "P | https://t.me/p | @channel | t")
	if err := h.partnerAdd(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 0 {
		t.Errorf("backend called with invalid input: %v", be.calls)
	}
	if len(c.sent) != 1 || !strings.Contains(c.sent[0].text(), "chat_id «@channel» — не число") {
		t.Errorf("sent = %+v, want a detailed chat_id error", c.sent)
	}
}

func TestInputErrorFromBackendIsShownAsIs(t *testing.T) {
	be := &fakeBackend{err: inputErr("День 2026-09-05 уже разыгран")}
	c := adminCtx(10, "2026-09-05 | 1 | 1 | p")
	if err := newHandlers(be).daySet(c); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 1 || !strings.Contains(c.sent[0].text(), "День 2026-09-05 уже разыгран") {
		t.Errorf("sent = %+v", c.sent)
	}
}

func TestInternalErrorShowsCauseToAdmin(t *testing.T) {
	be := &fakeBackend{err: errors.New("connection refused")}
	c := adminCtx(10, "")
	if err := newHandlers(be).promoStats(c); err != nil {
		t.Fatalf("err = %v; the admin is told in chat instead", err)
	}
	if len(c.sent) != 1 || !strings.Contains(c.sent[0].text(), "connection refused") ||
		!strings.Contains(c.sent[0].text(), "не выполнено") {
		t.Errorf("sent = %+v, want the cause and 'not performed'", c.sent)
	}
}

func uploadCtx(caption string, size int64) *fakeCtx {
	c := adminCtx(10, "")
	c.msg.Caption = caption
	c.msg.Document = &tele.Document{File: tele.File{FileSize: size}}
	return c
}

func TestPromoUploadParsesAndReports(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	h.open = func(tele.Context, *tele.Document) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("code\nA1\nB2\nA1\nBAD CODE\n")), nil
	}
	c := uploadCtx("/promo_upload 5", 30)
	if err := h.promoUpload(c); err != nil {
		t.Fatal(err)
	}
	if be.uploadTo != 5 || strings.Join(be.uploaded.Codes, ",") != "A1,B2" ||
		len(be.uploaded.Duplicates) != 1 || len(be.uploaded.Invalid) != 1 {
		t.Errorf("uploaded to %d: %+v", be.uploadTo, be.uploaded)
	}
	if len(c.sent) != 1 || !strings.Contains(c.sent[0].text(), "Загружено: 2") {
		t.Errorf("sent = %+v", c.sent)
	}
}

func TestPromoUploadRejections(t *testing.T) {
	tests := []struct {
		name    string
		c       *fakeCtx
		open    func(tele.Context, *tele.Document) (io.ReadCloser, error)
		wantSub string
	}{
		{"no caption", uploadCtx("", 10), nil, "без подписи"},
		{"wrong caption", uploadCtx("/promo_upload abc", 10), nil, "не распознана"},
		{"too big", uploadCtx("/promo_upload 1", maxUploadBytes+1), nil, "слишком большой"},
		{"download fails", uploadCtx("/promo_upload 1", 10), func(tele.Context, *tele.Document) (io.ReadCloser, error) {
			return nil, errors.New("telegram down")
		}, "telegram down"},
		{"bad csv", uploadCtx("/promo_upload 1", 10), func(tele.Context, *tele.Document) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("1,A\n2,B\n")), nil
		}, "несколько столбцов"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			be := &fakeBackend{}
			h := newHandlers(be)
			h.open = tt.open
			if err := h.promoUpload(tt.c); err != nil {
				t.Fatal(err)
			}
			if len(be.calls) != 0 {
				t.Errorf("backend called: %v", be.calls)
			}
			if len(tt.c.sent) != 1 || !strings.Contains(tt.c.sent[0].text(), tt.wantSub) {
				t.Errorf("sent = %+v, want text containing %q", tt.c.sent, tt.wantSub)
			}
		})
	}
}

func TestExportSendsDocument(t *testing.T) {
	be := &fakeBackend{file: &ExportFile{Name: "participants_2026-09-05.csv", Data: []byte("a,b\n"), Rows: 3}}
	c := adminCtx(10, "participants 1")
	if err := newHandlers(be).export(c); err != nil {
		t.Fatal(err)
	}
	doc, ok := c.sent[0].what.(*tele.Document)
	if !ok || doc.FileName != "participants_2026-09-05.csv" || !strings.Contains(doc.Caption, "строк 3") {
		t.Errorf("sent = %+v", c.sent)
	}
}

func (f *fakeBackend) EditPartner(_ context.Context, _ int64, e PartnerEdit) (PartnerEditResult, error) {
	f.calls = append(f.calls, "EditPartner")
	return PartnerEditResult{Partner: db.Partner{ID: e.ID, Name: "P"}, Old: "old", New: "new", Changed: f.edited}, f.err
}

func TestPartnerEditHandler(t *testing.T) {
	be := &fakeBackend{edited: true}
	c := adminCtx(10, "3 | chat_id | -100500")
	if err := newHandlers(be).partnerEdit(c); err != nil {
		t.Fatal(err)
	}
	if got := c.sent[0].text(); !strings.Contains(got, "chat_id: old → new") {
		t.Errorf("reply = %q", got)
	}

	be = &fakeBackend{}
	c = adminCtx(10, "3 | chat_id | -100500")
	if err := newHandlers(be).partnerEdit(c); err != nil {
		t.Fatal(err)
	}
	if got := c.sent[0].text(); !strings.Contains(got, "менять нечего") {
		t.Errorf("unchanged reply = %q", got)
	}

	c = adminCtx(10, "3 | color | red")
	if err := newHandlers(be).partnerEdit(c); err != nil {
		t.Fatal(err)
	}
	if got := c.sent[0].text(); !strings.Contains(got, "Неизвестное поле «color»") || len(be.calls) != 1 {
		t.Errorf("reply = %q, calls = %v", got, be.calls)
	}

	c = adminCtx(10, "")
	_ = newHandlers(be).partnerEdit(c)
	if !strings.Contains(c.sent[0].text(), "Пример") {
		t.Errorf("no usage: %q", c.sent[0].text())
	}
}

func TestPartnerEditNeedsAdmin(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(99, "3 | chat_id | -100500")
	if err := route([]int64{10}, newHandlers(be).partnerEdit)(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 0 || len(c.sent) != 0 {
		t.Errorf("non-admin edit: calls = %v, sent = %v", be.calls, c.sent)
	}
}

func (f *fakeBackend) RunDraw(_ context.Context, _ int64, date time.Time, force bool) (service.DrawResult, error) {
	f.calls = append(f.calls, fmt.Sprintf("RunDraw %s force=%v", date.Format(time.DateOnly), force))
	return service.DrawResult{
		Day:     db.CampaignDay{Day: pgtype.Date{Time: date, Valid: true}, Status: "drawn"},
		Partner: db.Partner{Name: "Кофейня"}, DrawnAt: time.Date(2026, 9, 6, 0, 1, 0, 0, time.UTC),
		Entries: 10, Excluded: 1, WinnersCount: 3,
		Winners: []service.DrawWinner{{TelegramID: 5, Username: "ann", FirstName: "Анна", Prize: "Приз"}},
	}, f.err
}

func (f *fakeBackend) ConfirmDraw(_ context.Context, _ int64, date time.Time) (service.ConfirmResult, error) {
	f.calls = append(f.calls, "ConfirmDraw "+date.Format(time.DateOnly))
	return service.ConfirmResult{
		Partner: db.Partner{Name: "Кофейня"}, Winners: 3, Sent: 2,
		Failed: []service.NotifyFailure{{TelegramID: 7, Err: errors.New("user 7 blocked the bot")}},
	}, f.err
}

func TestDrawRunHandler(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "2026-09-05 force")
	if err := newHandlers(be).drawRun(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 1 || be.calls[0] != "RunDraw 2026-09-05 force=true" {
		t.Errorf("calls = %v", be.calls)
	}
	got := c.sent[0].text()
	for _, want := range []string{"Розыгрыш дня 2026-09-05 — Кофейня", "Заявок: 10, выбыло из-за отписки: 1", "Победителей: 1 из 3",
		"меньше заявленного", "1. Анна (@ann, id 5) — Приз", "НЕ отправлены", "/draw_confirm 2026-09-05"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview lacks %q:\n%s", want, got)
		}
	}
}

func TestDrawConfirmHandler(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "2026-09-05")
	if err := newHandlers(be).drawConfirm(c); err != nil {
		t.Fatal(err)
	}
	got := c.sent[0].text()
	for _, want := range []string{"подтверждены, но не все уведомления", "Победителей: 3. Уведомлено сейчас: 2", "id 7: user 7 blocked the bot", "/draw_confirm 2026-09-05"} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
}

func TestDrawCommandsValidateInput(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	for payload, want := range map[string]string{
		"05.09.2026":             "ГГГГ-ММ-ДД",
		"2026-09-05 now":         "Допустим только force",
		"2026-09-05 force extra": "получено полей: 3",
	} {
		c := adminCtx(10, payload)
		if err := h.drawRun(c); err != nil {
			t.Fatal(err)
		}
		if len(c.sent) != 1 || !strings.Contains(c.sent[0].text(), want) {
			t.Errorf("%q: sent = %v, want %q", payload, c.sent, want)
		}
	}
	c := adminCtx(10, "2026-09-05 force") // confirm has no force
	_ = h.drawConfirm(c)
	if !strings.Contains(c.sent[0].text(), "получено полей: 2") || len(be.calls) != 0 {
		t.Errorf("confirm accepted force: %v %v", c.sent, be.calls)
	}
	for _, handler := range []tele.HandlerFunc{h.drawRun, h.drawConfirm} {
		c := adminCtx(10, "")
		_ = handler(c)
		if !strings.Contains(c.sent[0].text(), "Формат команды") {
			t.Errorf("no usage: %q", c.sent[0].text())
		}
	}
}

func TestDrawCommandsNeedAdmin(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	for _, handler := range []tele.HandlerFunc{h.drawRun, h.drawConfirm} {
		c := adminCtx(99, "2026-09-05")
		if err := route([]int64{10}, handler)(c); err != nil {
			t.Fatal(err)
		}
		if len(c.sent) != 0 {
			t.Error("non-admin got a reply")
		}
	}
	if len(be.calls) != 0 {
		t.Errorf("non-admin triggered %v", be.calls)
	}
}

func TestDrawErrorMapping(t *testing.T) {
	date := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		err  error
		want string
	}{
		{service.ErrDrawDayNotFound, "не назначен партнёр"},
		{service.ErrDrawDayNotOver, "/draw_run 2026-09-05 force"},
		{service.ErrDrawDayInFuture, "ещё не наступил"},
		{service.ErrDrawNotRun, "ещё не проводился"},
		{fmt.Errorf("%w: chat not found", service.ErrSubscriptionCheck), "НЕ проведён"},
	}
	for _, tt := range tests {
		wantInputErr(t, drawError(tt.err, date), tt.want)
	}
	boom := errors.New("db down")
	if got := drawError(boom, date); !errors.Is(got, boom) {
		t.Errorf("unexpected errors must pass through, got %v", got)
	}
	if drawError(nil, date) != nil {
		t.Error("nil must stay nil")
	}
}

func (f *fakeBackend) RunFinal(_ context.Context, adminID int64, force bool) (service.FinalResult, error) {
	f.calls = append(f.calls, fmt.Sprintf("RunFinal force=%v", force))
	return service.FinalResult{
		Draw: db.FinalDraw{
			ID: 1, Status: "drawn", HeldAt: pgtype.Timestamptz{Time: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), Valid: true},
			HeldByTelegramID: adminID, DrumTickets: 57, DrumUsers: 12, WinnersCount: 5, ReservesCount: 5, PrizeTickets: 2,
		},
		Winners: []service.FinalWinner{
			{TelegramID: 1, Username: "ann", FirstName: "Анна", Tickets: 7},
			{TelegramID: 2, FirstName: "Борис", Tickets: 3, Reserve: true, Position: 1},
		},
	}, f.err
}

func (f *fakeBackend) ConfirmFinal(context.Context, int64) (service.FinalConfirmResult, error) {
	f.calls = append(f.calls, "ConfirmFinal")
	return service.FinalConfirmResult{Winners: 5, Sent: 4, Failed: []service.NotifyFailure{{TelegramID: 9, Err: errors.New("blocked")}}}, f.err
}

func TestFinalRunHandler(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "force")
	if err := newHandlers(be).finalRun(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 1 || be.calls[0] != "RunFinal force=true" {
		t.Errorf("calls = %v", be.calls)
	}
	got := c.sent[0].text()
	for _, want := range []string{"Финальный розыгрыш", "админ 10", "В барабане: 57 билетов у 12 участников", "Победителей: 1 из 5",
		"призовых билетов на победителя: 2", "резерв: 1 из 5", "меньше заявленного", "1. Анна (@ann, id 1) — билетов в барабане: 7",
		"Резерв (очередь)", "1. Борис (id 2)", "НЕ отправлены", "/final_confirm"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview lacks %q:\n%s", want, got)
		}
	}
}

func TestFinalConfirmHandler(t *testing.T) {
	c := adminCtx(10, "")
	if err := newHandlers(&fakeBackend{}).finalConfirm(c); err != nil {
		t.Fatal(err)
	}
	got := c.sent[0].text()
	for _, want := range []string{"не все уведомления доставлены", "Победителей: 5. Уведомлено сейчас: 4", "Резервистам уведомления не отправляются", "id 9: blocked"} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
}

func TestFinalRunRejectsUnknownArguments(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "now")
	if err := newHandlers(be).finalRun(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 0 || !strings.Contains(c.sent[0].text(), "Допустим только параметр force") {
		t.Errorf("calls = %v, sent = %v", be.calls, c.sent)
	}
}

func TestFinalCommandsNeedAdmin(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	for _, handler := range []tele.HandlerFunc{h.finalRun, h.finalConfirm} {
		c := adminCtx(99, "force")
		if err := route([]int64{10}, handler)(c); err != nil {
			t.Fatal(err)
		}
		if len(c.sent) != 0 {
			t.Error("non-admin got a reply")
		}
	}
	if len(be.calls) != 0 {
		t.Errorf("non-admin triggered %v", be.calls)
	}
}

func TestFinalErrorMapping(t *testing.T) {
	s := NewService(nil, time.Time{}, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), time.UTC)
	wantInputErr(t, s.finalError(service.ErrFinalNotOver), "2026-09-30")
	wantInputErr(t, s.finalError(service.ErrFinalNotOver), "/final_run force")
	wantInputErr(t, s.finalError(service.ErrFinalNoTickets), "нет ни одного билета")
	wantInputErr(t, s.finalError(service.ErrFinalNotRun), "/final_run")
	boom := errors.New("db down")
	if got := s.finalError(boom); !errors.Is(got, boom) {
		t.Errorf("unexpected errors must pass through, got %v", got)
	}
}
