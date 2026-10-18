package admin

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	tele "gopkg.in/telebot.v4"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// sent is one c.Send call.
type sent struct {
	what any
	opts []any
}

func (s sent) text() string { return fmt.Sprint(s.what) }

// fakeCtx implements only the tele.Context methods the admin layer touches.
type fakeCtx struct {
	tele.Context
	sender *tele.User
	chat   *tele.Chat
	msg    *tele.Message
	data   string
	sent   []sent
}

func adminCtx(senderID int64, payload string) *fakeCtx {
	return &fakeCtx{
		sender: &tele.User{ID: senderID},
		chat:   &tele.Chat{ID: senderID, Type: tele.ChatPrivate},
		msg:    &tele.Message{Payload: payload},
	}
}

func (f *fakeCtx) Sender() *tele.User     { return f.sender }
func (f *fakeCtx) Chat() *tele.Chat       { return f.chat }
func (f *fakeCtx) Message() *tele.Message { return f.msg }
func (f *fakeCtx) Text() string           { return f.msg.Text }
func (f *fakeCtx) Get(string) any         { return nil }
func (f *fakeCtx) Send(what any, opts ...any) error {
	f.sent = append(f.sent, sent{what: what, opts: opts})
	return nil
}

func TestIsAdmin(t *testing.T) {
	admins := []int64{10, 20}
	for id, want := range map[int64]bool{10: true, 20: true, 30: false, 0: false, -10: false} {
		if got := IsAdmin(admins, id); got != want {
			t.Errorf("IsAdmin(%d) = %v, want %v", id, got, want)
		}
	}
	if IsAdmin(nil, 10) {
		t.Error("nobody is an admin when ADMIN_IDS is empty")
	}
}

func TestGuardAllowsAdmin(t *testing.T) {
	called := false
	h := Guard([]int64{10}, discard)(func(tele.Context) error { called = true; return nil })
	c := adminCtx(10, "")
	if err := h(c); err != nil || !called {
		t.Errorf("admin was not let through (called=%v, err=%v)", called, err)
	}
	if len(c.sent) != 0 {
		t.Errorf("guard sent %d messages to an admin", len(c.sent))
	}
}

func TestGuardBlocksEveryoneElseSilently(t *testing.T) {
	var logs strings.Builder
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return logs.Write(p)
	}), nil))

	called := false
	h := Guard([]int64{10}, log)(func(tele.Context) error { called = true; return nil })
	c := adminCtx(99, "")
	c.msg.Text = "/admin"
	if err := h(c); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("handler ran for a non-admin")
	}
	if len(c.sent) != 0 {
		t.Errorf("non-admin got %d replies; the commands must not reveal themselves", len(c.sent))
	}
	if !strings.Contains(logs.String(), "admin command denied") || !strings.Contains(logs.String(), "user_id=99") {
		t.Errorf("denial not logged: %s", logs.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestGuardBlocksMissingSender(t *testing.T) {
	called := false
	h := Guard([]int64{10}, discard)(func(tele.Context) error { called = true; return nil })
	c := adminCtx(10, "")
	c.sender = nil
	c.msg.Text = "/admin"
	if err := h(c); err != nil || called {
		t.Errorf("update without a sender must be ignored (called=%v, err=%v)", called, err)
	}
}

func TestGuardAdminOnlyInPrivateChat(t *testing.T) {
	called := false
	h := Guard([]int64{10}, discard)(func(tele.Context) error { called = true; return nil })
	c := adminCtx(10, "")
	c.chat = &tele.Chat{ID: -500, Type: tele.ChatSuperGroup}
	if err := h(c); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("admin command ran in a group chat")
	}
	if len(c.sent) != 1 || !strings.Contains(c.sent[0].text(), "личной переписке") {
		t.Errorf("sent = %+v, want an explanation", c.sent)
	}
}

func (f *fakeCtx) Data() string                            { return f.data }
func (f *fakeCtx) Respond(...*tele.CallbackResponse) error { return nil }
