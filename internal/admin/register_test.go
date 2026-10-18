package admin

import (
	"bytes"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"
)

type boundRoute struct {
	name string
	h    tele.HandlerFunc
	mws  []tele.MiddlewareFunc
}

// recBinder records every route Register installs.
type recBinder struct{ routes []boundRoute }

func (r *recBinder) Handle(endpoint interface{}, h tele.HandlerFunc, m ...tele.MiddlewareFunc) {
	name := ""
	switch e := endpoint.(type) {
	case string:
		name = strings.TrimPrefix(e, "\a")
	case *tele.Btn:
		name = "btn:" + e.Unique
	}
	r.routes = append(r.routes, boundRoute{name: name, h: h, mws: m})
}

// chain applies the middleware the way telebot does: the first one is outermost.
func (r boundRoute) chain() tele.HandlerFunc {
	h := r.h
	for i := len(r.mws) - 1; i >= 0; i-- {
		h = r.mws[i](h)
	}
	return h
}

func registered(t *testing.T) (*recBinder, *fakeBackend) {
	t.Helper()
	be := &fakeBackend{}
	rb := &recBinder{}
	Register(rb, Deps{Log: discard, Admins: []int64{10}, Backend: be, Location: time.UTC})
	return rb, be
}

// TestEveryAdminRouteIsGuarded is the structural guarantee behind "admin rights
// are checked on every command": whatever route Register installs, a stranger
// gets no reply and cannot reach the backend, and nothing works in a group chat.
// A new command added without the guard fails here.
func TestEveryAdminRouteIsGuarded(t *testing.T) {
	rb, be := registered(t)

	var names []string
	for _, r := range rb.routes {
		names = append(names, r.name)
	}
	sort.Strings(names)
	for _, must := range []string{
		"/admin", "/partner_add", "/partner_edit", "/partner_list", "/day_set", "/promo_stats", "/export",
		"/draw_run", "/draw_confirm", "/final_run", "/final_confirm", "/winners", "/purge_contacts",
		"/broadcast", "/broadcast_status", "/broadcast_cancel", "/stats", "/promo_upload",
		"document", "btn:bc_send", "btn:bc_cancel",
	} {
		i := sort.SearchStrings(names, must)
		if i == len(names) || names[i] != must {
			t.Errorf("route %q is not registered; have %v", must, names)
		}
	}

	for _, r := range rb.routes {
		if len(r.mws) != 1 {
			t.Errorf("%s: %d middleware, want exactly the admin guard", r.name, len(r.mws))
			continue
		}
		h := r.chain()

		stranger := adminCtx(99, "x | https://t.me/x | -1 | t")
		stranger.msg.Text = "/broadcast all | spam"
		stranger.msg.Caption = "/promo_upload 1"
		stranger.msg.Document = &tele.Document{}
		stranger.data = "1"
		if err := h(stranger); err != nil {
			t.Errorf("%s: stranger got error %v", r.name, err)
		}
		if len(stranger.sent) != 0 {
			t.Errorf("%s: a stranger got %d replies", r.name, len(stranger.sent))
		}

		group := adminCtx(10, "1")
		group.chat = &tele.Chat{ID: -5, Type: tele.ChatGroup}
		group.msg.Text = "/broadcast all | spam"
		group.data = "1"
		_ = h(group)
	}
	if len(be.calls) != 0 {
		t.Errorf("the backend was reached by a stranger or from a group: %v", be.calls)
	}
}

func TestAdminRoutesWorkForAnAdminInPrivate(t *testing.T) {
	rb, be := registered(t)
	for _, r := range rb.routes {
		if r.name != "/admin" {
			continue
		}
		c := adminCtx(10, "")
		if err := r.chain()(c); err != nil || len(c.sent) != 1 {
			t.Errorf("/admin as an admin: err %v, sent %v", err, c.sent)
		}
	}
	if len(be.calls) != 1 || be.calls[0] != "Audit" {
		t.Errorf("calls = %v", be.calls)
	}
}

func TestDeniedAttemptIsLoggedWithoutTheMessageText(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := Guard([]int64{10}, log)(func(tele.Context) error { return nil })

	c := adminCtx(99, "")
	c.msg.Text = "/broadcast all | my passport number is 4510 123456"
	if err := h(c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "admin command denied") || !strings.Contains(out, "command=/broadcast") || !strings.Contains(out, "user_id=99") {
		t.Errorf("log: %s", out)
	}
	if strings.Contains(out, "passport") || strings.Contains(out, "123456") {
		t.Errorf("the stranger's message leaked into the log: %s", out)
	}
}

func TestCommandWord(t *testing.T) {
	for in, want := range map[string]string{
		"/admin":                      "/admin",
		"  /export participants 5":    "/export participants 5"[:7],
		"/broadcast all | x\nmore":    "/broadcast",
		"just text":                   "",
		"":                            "",
		"/" + strings.Repeat("a", 50): "/" + strings.Repeat("a", 31),
	} {
		if got := commandWord(in); got != want {
			t.Errorf("commandWord(%q) = %q, want %q", in, got, want)
		}
	}
}
