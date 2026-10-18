package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
)

func (f *fakeBackend) DraftBroadcast(_ context.Context, _ int64, segment, text string) (service.BroadcastInfo, error) {
	f.calls = append(f.calls, fmt.Sprintf("Draft %s|%s", segment, text))
	return service.BroadcastInfo{ID: 12, Status: "draft", Segment: segment, Text: text, Audience: 340}, f.err
}

func (f *fakeBackend) ConfirmBroadcast(_ context.Context, _ int64, id int64) (service.BroadcastInfo, error) {
	f.calls = append(f.calls, fmt.Sprintf("Confirm %d", id))
	return service.BroadcastInfo{ID: id, Status: "sending", Audience: 340}, f.err
}

func (f *fakeBackend) CancelBroadcast(_ context.Context, _ int64, id int64) (service.BroadcastReport, error) {
	f.calls = append(f.calls, fmt.Sprintf("Cancel %d", id))
	return service.BroadcastReport{ID: id, Status: "cancelled", Sent: 100, Pending: 240}, f.err
}

func (f *fakeBackend) BroadcastReport(_ context.Context, _ int64, id int64) (service.BroadcastReport, error) {
	f.calls = append(f.calls, fmt.Sprintf("Report %d", id))
	return service.BroadcastReport{
		ID: 12, Status: "done", Segment: "no_promo", Total: 340, Sent: 320, Failed: 5, Blocked: 15,
		StartedAt: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), Finished: time.Date(2026, 9, 5, 12, 0, 14, 0, time.UTC),
	}, f.err
}

func TestParseBroadcastArgs(t *testing.T) {
	seg, text, err := parseBroadcastArgs("NO_PROMO | Привет!\nВторая строка | с чертой")
	if err != nil || seg != "no_promo" || text != "Привет!\nВторая строка | с чертой" {
		t.Errorf("got (%q, %q, %v); line breaks and later | must stay in the text", seg, text, err)
	}
	for in, want := range map[string]string{
		"всем привет":   "получено полей: 1",
		"everyone | hi": "Неизвестный сегмент «everyone»",
		"all |   ":      "пустой или длиннее",
		" | hi":         "Неизвестный сегмент",
	} {
		_, _, err := parseBroadcastArgs(in)
		wantInputErr(t, err, want)
	}
}

func TestCommandTextKeepsLineBreaks(t *testing.T) {
	c := adminCtx(10, "")
	c.msg.Text = "/broadcast all | строка 1\nстрока 2"
	if got := commandText(c); got != "all | строка 1\nстрока 2" {
		t.Errorf("got %q", got)
	}
	c.msg.Text = "/broadcast@my_bot all | hi"
	if got := commandText(c); got != "all | hi" {
		t.Errorf("got %q", got)
	}
	c.msg.Text = "/broadcast"
	if got := commandText(c); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestBroadcastShowsPreviewThenAsksForConfirmation(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "")
	c.msg.Text = "/broadcast no_promo | Забери <промокод>!\nДо вечера."
	if err := newHandlers(be).broadcast(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 1 || be.calls[0] != "Draft no_promo|Забери <промокод>!\nДо вечера." {
		t.Errorf("calls = %v; nothing may be sent before the confirmation", be.calls)
	}
	if len(c.sent) != 2 {
		t.Fatalf("sent %d messages, want preview + question", len(c.sent))
	}
	// 1: the exact text recipients will see, sent to the admin only, without markup parsing
	if c.sent[0].text() != "Забери <промокод>!\nДо вечера." || len(c.sent[0].opts) != 0 {
		t.Errorf("preview = %+v", c.sent[0])
	}
	// 2: the summary with confirm / cancel buttons carrying the broadcast id
	summary := c.sent[1].text()
	for _, want := range []string{"Рассылка #12", "не получили промокод сегодня", "Получателей сейчас: 340", "Отправить?"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	var markup *tele.ReplyMarkup
	for _, o := range c.sent[1].opts {
		if m, ok := o.(*tele.ReplyMarkup); ok {
			markup = m
		}
	}
	if markup == nil || len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("markup = %+v", markup)
	}
	send, cancel := markup.InlineKeyboard[0][0], markup.InlineKeyboard[0][1]
	if send.Unique != uniqueBroadcastSend || send.Data != "12" || cancel.Unique != uniqueBroadcastCancel || cancel.Data != "12" {
		t.Errorf("buttons = %+v %+v", send, cancel)
	}
}

func TestBroadcastWithoutArgumentsShowsUsage(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "")
	c.msg.Text = "/broadcast"
	if err := newHandlers(be).broadcast(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 0 || !strings.Contains(c.sent[0].text(), "Пример") {
		t.Errorf("calls = %v, sent = %v", be.calls, c.sent)
	}
}

func TestBroadcastInvalidInputNeverReachesTheQueue(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "")
	c.msg.Text = "/broadcast everyone | hi"
	if err := newHandlers(be).broadcast(c); err != nil {
		t.Fatal(err)
	}
	if len(be.calls) != 0 || !strings.Contains(c.sent[0].text(), "no_promo") {
		t.Errorf("calls = %v, sent = %v", be.calls, c.sent)
	}
}

func TestBroadcastConfirmAndCancelButtons(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)

	c := adminCtx(10, "")
	c.data = "12"
	if err := h.broadcastSend(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.sent[0].text(), "Рассылка #12 запущена: получателей 340") {
		t.Errorf("reply = %q", c.sent[0].text())
	}

	c = adminCtx(10, "")
	c.data = "12"
	if err := h.broadcastCancelButton(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.sent[0].text(), "Уже отправлено: 100, не будет отправлено: 240") {
		t.Errorf("reply = %q", c.sent[0].text())
	}

	c = adminCtx(10, "")
	c.data = "junk"
	_ = h.broadcastSend(c)
	if !strings.Contains(c.sent[0].text(), "Кнопка устарела") || len(be.calls) != 2 {
		t.Errorf("reply = %q, calls = %v", c.sent[0].text(), be.calls)
	}
}

func TestBroadcastStatusAndCancelCommands(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)

	c := adminCtx(10, "")
	if err := h.broadcastStatus(c); err != nil {
		t.Fatal(err)
	}
	got := c.sent[0].text()
	for _, want := range []string{"Рассылка #12 завершена", "Получателей: 340", "Отправлено: 320", "Ошибок: 5", "Заблокировали бота: 15", "Заняло: 14s", "в следующие рассылки не попадут"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}

	c = adminCtx(10, "7")
	_ = h.broadcastStatus(c)
	c = adminCtx(10, "abc")
	_ = h.broadcastStatus(c)
	if !strings.Contains(c.sent[0].text(), "id рассылки") {
		t.Errorf("reply = %q", c.sent[0].text())
	}

	c = adminCtx(10, "")
	_ = h.broadcastCancelCommand(c)
	if !strings.Contains(c.sent[0].text(), "Формат команды") {
		t.Errorf("reply = %q", c.sent[0].text())
	}
	c = adminCtx(10, "5")
	_ = h.broadcastCancelCommand(c)
	if got := strings.Join(be.calls, "|"); got != "Report 0|Report 7|Cancel 5" {
		t.Errorf("calls = %s", got)
	}
}

func TestBroadcastCommandsNeedAdmin(t *testing.T) {
	be := &fakeBackend{}
	h := newHandlers(be)
	for _, handler := range []tele.HandlerFunc{h.broadcast, h.broadcastStatus, h.broadcastCancelCommand, h.broadcastSend, h.broadcastCancelButton} {
		c := adminCtx(99, "5")
		c.msg.Text = "/broadcast all | spam"
		c.data = "12"
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

type stubBroadcaster struct{ err error }

func (s stubBroadcaster) Draft(context.Context, int64, string, string) (service.BroadcastInfo, error) {
	return service.BroadcastInfo{}, s.err
}
func (s stubBroadcaster) Confirm(context.Context, int64, int64) (service.BroadcastInfo, error) {
	return service.BroadcastInfo{}, s.err
}
func (s stubBroadcaster) Cancel(context.Context, int64, int64) (service.BroadcastReport, error) {
	return service.BroadcastReport{}, s.err
}
func (s stubBroadcaster) Report(context.Context, int64) (service.BroadcastReport, error) {
	return service.BroadcastReport{}, s.err
}

func TestBroadcastErrorsAreExplained(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{service.ErrBroadcastBadText, "пустой или длиннее 4096"},
		{fmt.Errorf("%w: %w", service.ErrSegmentUnavailable, service.ErrNoCampaignDay), "привязан к сегодняшнему дню"},
		{fmt.Errorf("%w: %w", service.ErrSegmentUnavailable, service.ErrCampaignFinished), "акция уже завершилась"},
		{service.ErrBroadcastNotFound, "Рассылка #5 не найдена"},
		{fmt.Errorf("%w: sending", service.ErrBroadcastState), "Рассылка #5: действие не подходит к её состоянию (sending)"},
	}
	for _, tt := range tests {
		s := NewService(nil, time.Time{}, time.Time{}, time.UTC).WithBroadcaster(stubBroadcaster{tt.err})
		_, err := s.ConfirmBroadcast(context.Background(), 1, 5)
		wantInputErr(t, err, tt.want)
	}
	boom := errors.New("db down")
	s := NewService(nil, time.Time{}, time.Time{}, time.UTC).WithBroadcaster(stubBroadcaster{boom})
	if _, err := s.ConfirmBroadcast(context.Background(), 1, 5); !errors.Is(err, boom) {
		t.Errorf("unexpected errors must pass through, got %v", err)
	}
}
