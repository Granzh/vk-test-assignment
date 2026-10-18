package handlers

import (
	"context"
	"errors"
	"fmt"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

// uniqueCheckSub is the callback id of the "check subscription" buttons.
const uniqueCheckSub = "check_sub"

// Joiner registers a user in the active day's draw (implemented by *service.Entry).
type Joiner interface {
	Join(ctx context.Context, user db.User, ad *service.ActiveDay) (service.JoinResult, error)
}

func (h *Handlers) joinDraw(c tele.Context) error {
	return h.withActiveDay(c, func(ad *service.ActiveDay) error {
		markup := subscribeMarkup(ad.Partner, texts.BtnSubscribeTo(ad.Partner.Name), texts.BtnCheckSub)
		return c.Send(texts.JoinPrompt(ad.Partner.Name), tele.ModeHTML, markup)
	})
}

func (h *Handlers) checkSub(c tele.Context) error {
	user, ok := userOf(c)
	if !ok {
		return errors.New("subscription check requested without a user in context")
	}
	if !h.checks.Allow(user.TelegramID) {
		return nil // too frequent; the earlier check is still running or just answered
	}
	return h.withActiveDay(c, func(ad *service.ActiveDay) error {
		res, err := h.Entry.Join(ctxOf(c), user, ad)
		switch {
		case errors.Is(err, service.ErrNotSubscribed):
			markup := subscribeMarkup(ad.Partner, texts.BtnSubscribe, texts.BtnCheckSubAgain)
			return c.Send(texts.NotSubscribed(ad.Partner.Name), tele.ModeHTML, markup)
		case errors.Is(err, service.ErrDayClosed):
			return c.Send(texts.DayClosed)
		case errors.Is(err, service.ErrSubscriptionCheck):
			h.Log.Error("subscription check failed", "err", err, "user_id", user.ID, "partner_id", ad.Partner.ID)
			return c.Send(texts.CheckFailed)
		case err != nil:
			return fmt.Errorf("join draw: %w", err)
		case res.Already:
			return c.Send(texts.AlreadyJoined)
		}
		return c.Send(texts.JoinSuccess(ad.Partner.Name, res.Chances), tele.ModeHTML)
	})
}

// subscribeMarkup builds the partner link (when the partner has one) and the
// check button.
func subscribeMarkup(p db.Partner, subscribeLabel, checkLabel string) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	if p.Url != "" {
		rows = append(rows, m.Row(m.URL(subscribeLabel, p.Url)))
	}
	rows = append(rows, m.Row(m.Data(checkLabel, uniqueCheckSub)))
	m.Inline(rows...)
	return m
}
