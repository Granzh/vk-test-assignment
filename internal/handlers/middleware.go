package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

const (
	ctxKey         = "ctx"
	userKey        = "user"
	requestTimeout = 15 * time.Second
)

// UserStore is the part of the storage layer the middleware needs.
type UserStore interface {
	UpsertUser(ctx context.Context, arg db.UpsertUserParams) (db.User, error)
	SetUserBlocked(ctx context.Context, arg db.SetUserBlockedParams) error
}

// ctxOf returns the per-update context created by contextMW.
func ctxOf(c tele.Context) context.Context {
	if ctx, ok := c.Get(ctxKey).(context.Context); ok {
		return ctx
	}
	return context.Background()
}

// userOf returns the sender's database row stored by userMW.
func userOf(c tele.Context) (db.User, bool) {
	u, ok := c.Get(userKey).(db.User)
	return u, ok
}

// recoverMW turns panics in handlers into logged errors and an apology to the user.
func recoverMW(log *slog.Logger, m Metrics) tele.MiddlewareFunc {
	if m == nil {
		m = noopMetrics{}
	}
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					m.Panic()
					log.Error("panic in handler", "panic", r, "stack", string(debug.Stack()))
					_ = c.Send(texts.InternalError)
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			return next(c)
		}
	}
}

// loggingMW logs every update with its duration and outcome.
func loggingMW(log *slog.Logger) tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			start := time.Now()
			err := next(c)

			attrs := []any{"update_id", c.Update().ID, "duration", time.Since(start)}
			if u := c.Sender(); u != nil {
				attrs = append(attrs, "user_id", u.ID)
			}
			if cb := c.Callback(); cb != nil {
				attrs = append(attrs, "callback", cb.Unique)
			}
			if err != nil {
				log.Error("update failed", append(attrs, "err", err)...)
			} else {
				log.Info("update handled", attrs...)
			}
			return err
		}
	}
}

// contextMW attaches a context with a timeout to every update.
func contextMW() tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
			defer cancel()
			c.Set(ctxKey, ctx)
			return next(c)
		}
	}
}

// userMW upserts the sender, which also refreshes last_seen_at and clears
// is_blocked (a user who writes to the bot has evidently unblocked it).
func userMW(users UserStore) tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			if u := c.Sender(); u != nil {
				user, err := users.UpsertUser(ctxOf(c), db.UpsertUserParams{
					TelegramID: u.ID,
					Username:   text(u.Username),
					FirstName:  text(u.FirstName),
				})
				if err != nil {
					return fmt.Errorf("upsert user %d: %w", u.ID, err)
				}
				c.Set(userKey, user)
			}
			return next(c)
		}
	}
}

// blockedMW marks users who blocked the bot. The error is swallowed: there is
// nothing to retry and nobody to tell.
func blockedMW(users UserStore, log *slog.Logger) tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			err := next(c)
			if err == nil || !errors.Is(err, tele.ErrBlockedByUser) {
				return err
			}
			if u := c.Sender(); u != nil {
				if mErr := users.SetUserBlocked(ctxOf(c), db.SetUserBlockedParams{TelegramID: u.ID, IsBlocked: true}); mErr != nil {
					return fmt.Errorf("mark user %d blocked: %w", u.ID, mErr)
				}
				log.Info("user blocked the bot", "user_id", u.ID)
			}
			return nil
		}
	}
}
