package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
)

type fakeSender struct {
	to   string
	what any
	opts []any
	err  error
}

func (f *fakeSender) Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error) {
	f.to, f.what, f.opts = to.Recipient(), what, opts
	return nil, f.err
}

func TestSend(t *testing.T) {
	f := &fakeSender{}
	if err := (&Bot{bot: f}).Send(context.Background(), 42, "<b>hi</b>"); err != nil {
		t.Fatal(err)
	}
	if f.to != "42" || f.what != "<b>hi</b>" || len(f.opts) != 1 || f.opts[0] != tele.ModeHTML {
		t.Errorf("sent to %s: %v %v", f.to, f.what, f.opts)
	}
}

func TestSendErrors(t *testing.T) {
	err := (&Bot{bot: &fakeSender{err: tele.ErrBlockedByUser}}).Send(context.Background(), 42, "x")
	if !errors.Is(err, tele.ErrBlockedByUser) {
		t.Errorf("err = %v", err)
	}
	boom := errors.New("boom")
	if err := (&Bot{bot: &fakeSender{err: boom}}).Send(context.Background(), 42, "x"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeSender{}
	if err := (&Bot{bot: f}).Send(ctx, 42, "x"); !errors.Is(err, context.Canceled) || f.to != "" {
		t.Errorf("canceled ctx: err = %v, sent = %q", err, f.to)
	}
}

func TestSendWithButton(t *testing.T) {
	f := &fakeSender{}
	if err := (&Bot{bot: f}).SendWithButton(context.Background(), 42, "hi", "Получить приз", "get_prize"); err != nil {
		t.Fatal(err)
	}
	var markup *tele.ReplyMarkup
	for _, o := range f.opts {
		if m, ok := o.(*tele.ReplyMarkup); ok {
			markup = m
		}
	}
	if f.to != "42" || markup == nil || len(markup.InlineKeyboard) != 1 ||
		markup.InlineKeyboard[0][0].Text != "Получить приз" || markup.InlineKeyboard[0][0].Unique != "get_prize" {
		t.Errorf("sent to %s with %+v", f.to, f.opts)
	}

	err := (&Bot{bot: &fakeSender{err: tele.ErrBlockedByUser}}).SendWithButton(context.Background(), 42, "x", "l", "u")
	if !errors.Is(err, tele.ErrBlockedByUser) {
		t.Errorf("err = %v", err)
	}
}

func TestSendTextClassifiesErrors(t *testing.T) {
	f := &fakeSender{}
	if err := (&Bot{bot: f}).SendText(context.Background(), 42, "<b>not html</b>"); err != nil {
		t.Fatal(err)
	}
	if f.to != "42" || f.what != "<b>not html</b>" || len(f.opts) != 0 {
		t.Errorf("plain text must go out without a parse mode: %v %v", f.what, f.opts)
	}

	for _, blocked := range []error{tele.ErrBlockedByUser, tele.ErrUserIsDeactivated, tele.ErrNotStartedByUser, tele.ErrChatNotFound} {
		err := (&Bot{bot: &fakeSender{err: blocked}}).SendText(context.Background(), 42, "x")
		if !errors.Is(err, service.ErrRecipientBlocked) {
			t.Errorf("%v: err = %v, want ErrRecipientBlocked", blocked, err)
		}
	}

	err := (&Bot{bot: &fakeSender{err: tele.FloodError{RetryAfter: 7}}}).SendText(context.Background(), 42, "x")
	var rl *service.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 7*time.Second {
		t.Errorf("flood: err = %v", err)
	}

	boom := errors.New("boom")
	err = (&Bot{bot: &fakeSender{err: boom}}).SendText(context.Background(), 42, "x")
	if !errors.Is(err, boom) || errors.Is(err, service.ErrRecipientBlocked) {
		t.Errorf("ordinary error: %v", err)
	}
}
