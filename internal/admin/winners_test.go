package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

func (f *fakeBackend) Winners(_ context.Context, _ int64, req WinnersRequest) (texts.AdminWinnersView, error) {
	f.calls = append(f.calls, fmt.Sprintf("Winners final=%v csv=%v", req.Final, req.CSV))
	return texts.AdminWinnersView{
		Title: "🏆 Финальный розыгрыш (id 4)", Final: req.Final, ID: 4,
		Lines: []texts.AdminWinnerLine{{TelegramID: 1, Username: "ann", FirstName: "Анна", Role: "победитель", Status: "согласие и данные получены"}},
	}, f.err
}

func (f *fakeBackend) PurgeContacts(_ context.Context, _ int64, confirm bool) (service.PurgeResult, error) {
	f.calls = append(f.calls, fmt.Sprintf("PurgeContacts confirm=%v", confirm))
	if confirm {
		return service.PurgeResult{Stored: 3, Purged: 3}, f.err
	}
	return service.PurgeResult{Stored: 3}, f.err
}

func TestWinnersHandler(t *testing.T) {
	be := &fakeBackend{file: &ExportFile{Name: "finalists_4.csv", Data: []byte("a"), Rows: 1}}
	h := newHandlers(be)

	c := adminCtx(10, "final")
	if err := h.winners(c); err != nil {
		t.Fatal(err)
	}
	got := c.sent[0].text()
	for _, want := range []string{"Финальный розыгрыш (id 4)", "1. Анна (@ann, id 1) — победитель", "согласие и данные получены", "/winners final csv"} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}

	c = adminCtx(10, "final csv")
	if err := h.winners(c); err != nil {
		t.Fatal(err)
	}
	if doc, ok := c.sent[0].what.(*tele.Document); !ok || doc.FileName != "finalists_4.csv" {
		t.Errorf("csv reply = %+v", c.sent)
	}
	if got := strings.Join(be.calls, "|"); got != "Winners final=true csv=false|Winners final=true csv=true|Export" {
		t.Errorf("calls = %s", got)
	}
}

func TestWinnersArgs(t *testing.T) {
	r, err := parseWinnersArgs("2026-09-05")
	if err != nil || r.Final || r.CSV || r.Date.Day() != 5 {
		t.Errorf("%+v, %v", r, err)
	}
	r, err = parseWinnersArgs("FINAL CSV")
	if err != nil || !r.Final || !r.CSV {
		t.Errorf("%+v, %v", r, err)
	}
	for in, want := range map[string]string{
		"":                "получено полей: 0",
		"final xlsx":      "Допустим только csv",
		"05.09 csv":       "ГГГГ-ММ-ДД",
		"final csv extra": "получено полей: 3",
	} {
		_, err := parseWinnersArgs(in)
		wantInputErr(t, err, want)
	}
}

func TestPurgeContactsHandler(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	c := adminCtx(10, "")
	if err := h.purgeContacts(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.sent[0].text(), "Сохранено имён победителей: 3") || !strings.Contains(c.sent[0].text(), "/purge_contacts confirm") {
		t.Errorf("dry run reply: %q", c.sent[0].text())
	}
	c = adminCtx(10, "confirm")
	if err := h.purgeContacts(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.sent[0].text(), "Удалено имён: 3") {
		t.Errorf("reply: %q", c.sent[0].text())
	}
	if got := strings.Join(be.calls, "|"); got != "PurgeContacts confirm=false|PurgeContacts confirm=true" {
		t.Errorf("calls = %s", got)
	}
	c = adminCtx(10, "yes")
	_ = h.purgeContacts(c)
	if !strings.Contains(c.sent[0].text(), "Допустим только параметр confirm") || len(be.calls) != 2 {
		t.Errorf("reply %q, calls %v", c.sent[0].text(), be.calls)
	}
}

func TestWinnersAndPurgeNeedAdmin(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	for _, handler := range []tele.HandlerFunc{h.winners, h.purgeContacts} {
		c := adminCtx(99, "final confirm")
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

type failingPurger struct{ err error }

func (f failingPurger) PurgeContacts(context.Context, int64, bool) (service.PurgeResult, error) {
	return service.PurgeResult{}, f.err
}

func TestPurgeTooEarlyIsExplained(t *testing.T) {
	s := NewService(nil, time.Time{}, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), time.UTC).
		WithContactPurger(failingPurger{service.ErrPurgeTooEarly})
	_, err := s.PurgeContacts(context.Background(), 1, true)
	wantInputErr(t, err, "после окончания акции (последний день — 2026-09-30)")
}
