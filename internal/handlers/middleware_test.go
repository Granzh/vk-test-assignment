package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// stubCtx implements only the tele.Context methods the middleware touches;
// calling anything else panics on the nil embedded interface.
type stubCtx struct {
	tele.Context
	sender *tele.User
	vals   map[string]any
	sent   []any
}

func newStubCtx(sender *tele.User) *stubCtx {
	return &stubCtx{sender: sender, vals: map[string]any{}}
}

func (s *stubCtx) Sender() *tele.User       { return s.sender }
func (s *stubCtx) Set(k string, v any)      { s.vals[k] = v }
func (s *stubCtx) Get(k string) any         { return s.vals[k] }
func (s *stubCtx) Update() tele.Update      { return tele.Update{ID: 1} }
func (s *stubCtx) Callback() *tele.Callback { return nil }
func (s *stubCtx) Send(what any, _ ...any) error {
	s.sent = append(s.sent, what)
	return nil
}

type fakeUsers struct {
	upserted []db.UpsertUserParams
	blocked  []db.SetUserBlockedParams
	err      error
}

func (f *fakeUsers) UpsertUser(_ context.Context, a db.UpsertUserParams) (db.User, error) {
	f.upserted = append(f.upserted, a)
	return db.User{}, f.err
}

func (f *fakeUsers) SetUserBlocked(_ context.Context, a db.SetUserBlockedParams) error {
	f.blocked = append(f.blocked, a)
	return nil
}

func TestRecoverMW(t *testing.T) {
	c := newStubCtx(nil)
	h := recoverMW(discard, nil)(func(tele.Context) error { panic("boom") })

	err := h(c)
	if err == nil {
		t.Fatal("expected error from recovered panic")
	}
	if len(c.sent) != 1 || c.sent[0] != texts.InternalError {
		t.Errorf("sent = %v, want one apology", c.sent)
	}
}

func TestUserMWUpserts(t *testing.T) {
	users := &fakeUsers{}
	c := newStubCtx(&tele.User{ID: 42, Username: "neo", FirstName: "Thomas"})
	called := false
	h := userMW(users)(func(tele.Context) error { called = true; return nil })

	if err := h(c); err != nil || !called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
	if len(users.upserted) != 1 {
		t.Fatalf("upserts = %d", len(users.upserted))
	}
	if _, ok := userOf(c); !ok {
		t.Error("user not stored in context")
	}
	got := users.upserted[0]
	if got.TelegramID != 42 || got.Username.String != "neo" || !got.FirstName.Valid {
		t.Errorf("upsert params = %+v", got)
	}
}

func TestUserMWEmptyUsername(t *testing.T) {
	users := &fakeUsers{}
	c := newStubCtx(&tele.User{ID: 1})
	_ = userMW(users)(func(tele.Context) error { return nil })(c)
	if users.upserted[0].Username.Valid {
		t.Error("empty username must be stored as NULL")
	}
}

func TestUserMWFailureStopsChain(t *testing.T) {
	boom := errors.New("db down")
	users := &fakeUsers{err: boom}
	called := false
	h := userMW(users)(func(tele.Context) error { called = true; return nil })

	err := h(newStubCtx(&tele.User{ID: 1}))
	if !errors.Is(err, boom) || called {
		t.Errorf("err = %v, called = %v", err, called)
	}
}

func TestBlockedMW(t *testing.T) {
	t.Run("marks blocked user and swallows error", func(t *testing.T) {
		users := &fakeUsers{}
		c := newStubCtx(&tele.User{ID: 7})
		h := blockedMW(users, discard)(func(tele.Context) error {
			return fmt.Errorf("send: %w", tele.ErrBlockedByUser)
		})
		if err := h(c); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(users.blocked) != 1 || users.blocked[0].TelegramID != 7 || !users.blocked[0].IsBlocked {
			t.Errorf("blocked = %+v", users.blocked)
		}
	})

	t.Run("passes other errors through", func(t *testing.T) {
		users := &fakeUsers{}
		other := errors.New("other")
		h := blockedMW(users, discard)(func(tele.Context) error { return other })
		if err := h(newStubCtx(&tele.User{ID: 7})); !errors.Is(err, other) {
			t.Errorf("err = %v", err)
		}
		if len(users.blocked) != 0 {
			t.Error("must not mark user blocked")
		}
	})
}
