// Package notify delivers messages to Telegram users on behalf of services.
package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/secret"
	"chat_bot/internal/service"
)

// sender is the part of *tele.Bot the notifier uses.
type sender interface {
	Send(to tele.Recipient, what interface{}, opts ...interface{}) (*tele.Message, error)
}

// Bot sends HTML messages through the bot; it implements service.Sender.
type Bot struct{ bot sender }

// NewBot wraps the bot.
func NewBot(b *tele.Bot) *Bot { return &Bot{bot: b} }

// Send delivers html to the user. A user who blocked the bot yields an error
// that says so.
func (b *Bot) Send(ctx context.Context, telegramID int64, html string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := b.bot.Send(&tele.User{ID: telegramID}, html, tele.ModeHTML); err != nil {
		if errors.Is(err, tele.ErrBlockedByUser) {
			return secret.Wrap(fmt.Errorf("user %d blocked the bot: %w", telegramID, err))
		}
		return secret.Wrap(fmt.Errorf("send to %d: %w", telegramID, err))
	}
	return nil
}

// SendWithButton delivers html with one inline callback button identified by
// unique; it implements service.ButtonSender.
func (b *Bot) SendWithButton(ctx context.Context, telegramID int64, html, label, unique string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data(label, unique)))
	if _, err := b.bot.Send(&tele.User{ID: telegramID}, html, tele.ModeHTML, m); err != nil {
		if errors.Is(err, tele.ErrBlockedByUser) {
			return secret.Wrap(fmt.Errorf("user %d blocked the bot: %w", telegramID, err))
		}
		return secret.Wrap(fmt.Errorf("send to %d: %w", telegramID, err))
	}
	return nil
}

// SendText delivers plain text (no formatting, so any characters are safe) and
// classifies Telegram's failures for the broadcast worker: an error wrapping
// service.ErrRecipientBlocked when the user cannot be reached and a
// *service.RateLimitError on 429. It implements service.BroadcastSender.
func (b *Bot) SendText(ctx context.Context, telegramID int64, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := b.bot.Send(&tele.User{ID: telegramID}, text)
	if err == nil {
		return nil
	}
	var flood tele.FloodError
	switch {
	case errors.As(err, &flood):
		return &service.RateLimitError{RetryAfter: time.Duration(flood.RetryAfter) * time.Second}
	case errors.Is(err, tele.ErrBlockedByUser), errors.Is(err, tele.ErrUserIsDeactivated),
		errors.Is(err, tele.ErrNotStartedByUser), errors.Is(err, tele.ErrChatNotFound):
		return secret.Wrap(fmt.Errorf("user %d: %w: %w", telegramID, service.ErrRecipientBlocked, err))
	}
	return secret.Wrap(fmt.Errorf("send to %d: %w", telegramID, err))
}
