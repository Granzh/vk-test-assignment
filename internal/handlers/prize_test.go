package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

// textCtx adds a message text to the recording context.
type textCtx struct {
	*recCtx
	text string
}

func (t *textCtx) Text() string { return t.text }

func newTextCtx(text string) *textCtx { return &textCtx{recCtx: joinCtx(), text: text} }

// fakePrize replays canned claims and records what it was asked.
type fakePrize struct {
	start, state, submit, consent service.Claim
	submitErr, err                error
	calls                         []string
	users                         []int64
}

func (f *fakePrize) Start(_ context.Context, userID int64) (service.Claim, error) {
	f.users = append(f.users, userID)
	f.calls = append(f.calls, "Start")
	return f.start, f.err
}

func (f *fakePrize) State(_ context.Context, userID int64) (service.Claim, error) {
	f.users = append(f.users, userID)
	f.calls = append(f.calls, "State")
	return f.state, f.err
}

func (f *fakePrize) SubmitName(_ context.Context, userID int64, text string) (service.Claim, error) {
	f.users = append(f.users, userID)
	f.calls = append(f.calls, "SubmitName:"+text)
	return f.submit, f.submitErr
}

func (f *fakePrize) Consent(_ context.Context, userID int64, agree bool) (service.Claim, error) {
	f.users = append(f.users, userID)
	if agree {
		f.calls = append(f.calls, "Consent:yes")
	} else {
		f.calls = append(f.calls, "Consent:no")
	}
	return f.consent, f.err
}

func prizeHandlers(p PrizeClaims) *Handlers { return &Handlers{Deps: Deps{Log: discard, Prize: p}} }

func TestGetPrizeAsksForTheName(t *testing.T) {
	c := newTextCtx("")
	if err := prizeHandlers(&fakePrize{start: service.Claim{Kind: service.ClaimAskName}}).getPrize(c); err != nil {
		t.Fatal(err)
	}
	if len(c.msgs) != 1 || c.msgs[0].text != texts.AskContactName {
		t.Errorf("msgs = %+v", c.msgs)
	}
}

func TestGetPrizeOtherStates(t *testing.T) {
	tests := []struct {
		name  string
		claim service.Claim
		want  string
	}{
		{"nothing to claim", service.Claim{Kind: service.ClaimNone}, texts.NoActivePrize},
		{"already done", service.Claim{Kind: service.ClaimDone}, texts.PrizeAlreadyReceived},
		{"expired prize", service.Claim{Kind: service.ClaimForfeited, Reason: "expired"}, texts.PrizeDeadlinePassed},
		{"deadline passed", service.Claim{Kind: service.ClaimExpired}, texts.PrizeDeadlinePassed},
		{"declined earlier", service.Claim{Kind: service.ClaimForfeited, Reason: "declined"}, texts.PrizeDeclinedAlready},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTextCtx("")
			if err := prizeHandlers(&fakePrize{start: tt.claim}).getPrize(c); err != nil {
				t.Fatal(err)
			}
			if len(c.msgs) != 1 || c.msgs[0].text != tt.want {
				t.Errorf("msgs = %+v, want %q", c.msgs, tt.want)
			}
		})
	}
}

func TestGetPrizeResumesAtConsent(t *testing.T) {
	c := newTextCtx("")
	if err := prizeHandlers(&fakePrize{start: service.Claim{Kind: service.ClaimAskConsent, Name: "Иван <b>"}}).getPrize(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.msgs[0].text, "Иван &lt;b&gt;") {
		t.Errorf("name must be escaped: %q", c.msgs[0].text)
	}
	btns := buttons(t, c.msgs[0].markup())
	if btns[texts.BtnConsentYes].Unique != uniqueConsentYes || btns[texts.BtnConsentNo].Unique != uniqueConsentNo {
		t.Errorf("buttons = %+v", btns)
	}
}

func TestOnTextOutsideADialogIsIgnored(t *testing.T) {
	f := &fakePrize{state: service.Claim{Kind: service.ClaimNone}}
	c := newTextCtx("привет")
	if err := prizeHandlers(f).onText(c); err != nil {
		t.Fatal(err)
	}
	if len(c.msgs) != 0 || len(f.calls) != 1 {
		t.Errorf("msgs = %+v, calls = %v", c.msgs, f.calls)
	}
}

func TestOnTextNameStep(t *testing.T) {
	// a good name moves on to the consent question
	f := &fakePrize{state: service.Claim{Kind: service.ClaimAskName}, submit: service.Claim{Kind: service.ClaimAskConsent, Name: "Иван Петров"}}
	c := newTextCtx("Иван Петров")
	if err := prizeHandlers(f).onText(c); err != nil {
		t.Fatal(err)
	}
	if f.calls[1] != "SubmitName:Иван Петров" || !strings.Contains(c.msgs[0].text, "Иван Петров") || c.msgs[0].markup() == nil {
		t.Errorf("calls = %v, msgs = %+v", f.calls, c.msgs)
	}

	// a bad one is asked again
	f = &fakePrize{state: service.Claim{Kind: service.ClaimAskName}, submitErr: service.ErrBadContactName}
	c = newTextCtx("12345")
	if err := prizeHandlers(f).onText(c); err != nil {
		t.Fatal(err)
	}
	if len(c.msgs) != 1 || c.msgs[0].text != texts.BadContactName {
		t.Errorf("msgs = %+v", c.msgs)
	}

	// the deadline ran out while the user was typing
	f = &fakePrize{state: service.Claim{Kind: service.ClaimAskName}, submit: service.Claim{Kind: service.ClaimExpired}}
	c = newTextCtx("Иван Петров")
	_ = prizeHandlers(f).onText(c)
	if c.msgs[0].text != texts.PrizeDeadlinePassed {
		t.Errorf("msgs = %+v", c.msgs)
	}
}

func TestOnTextAtConsentStepPointsToTheButtons(t *testing.T) {
	f := &fakePrize{state: service.Claim{Kind: service.ClaimAskConsent}}
	c := newTextCtx("да")
	if err := prizeHandlers(f).onText(c); err != nil {
		t.Fatal(err)
	}
	if len(c.msgs) != 1 || c.msgs[0].text != texts.ConsentNudge || len(f.calls) != 1 {
		t.Errorf("msgs = %+v, calls = %v (typed text must not count as consent)", c.msgs, f.calls)
	}
}

func TestConsentButtons(t *testing.T) {
	tests := []struct {
		name  string
		agree bool
		claim service.Claim
		want  string
		call  string
	}{
		{"agreed", true, service.Claim{Kind: service.ClaimDone}, texts.ConsentThanks, "Consent:yes"},
		{"refused", false, service.Claim{Kind: service.ClaimForfeited, Reason: "declined"}, texts.ConsentDeclined, "Consent:no"},
		{"too late", true, service.Claim{Kind: service.ClaimExpired}, texts.PrizeDeadlinePassed, "Consent:yes"},
		{"stale button", true, service.Claim{Kind: service.ClaimNone}, texts.PrizeDialogStale, "Consent:yes"},
		{"name still expected", true, service.Claim{Kind: service.ClaimAskName}, texts.AskContactName, "Consent:yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakePrize{consent: tt.claim}
			c := newTextCtx("")
			h := prizeHandlers(f)
			handler := h.consentYes
			if !tt.agree {
				handler = h.consentNo
			}
			if err := handler(c); err != nil {
				t.Fatal(err)
			}
			if len(f.calls) != 1 || f.calls[0] != tt.call || len(c.msgs) != 1 || c.msgs[0].text != tt.want {
				t.Errorf("calls = %v, msgs = %+v, want %q", f.calls, c.msgs, tt.want)
			}
		})
	}
}

func TestPrizeHandlersReturnServiceErrors(t *testing.T) {
	boom := errors.New("db down")
	h := prizeHandlers(&fakePrize{err: boom})
	for name, handler := range map[string]func(*textCtx) error{
		"getPrize": func(c *textCtx) error { return h.getPrize(c) },
		"onText":   func(c *textCtx) error { return h.onText(c) },
		"yes":      func(c *textCtx) error { return h.consentYes(c) },
	} {
		if err := handler(newTextCtx("x")); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// forgedCtx carries callback data an attacker could have crafted. User-facing
// handlers must never read it: they act on the sender's own record only.
type forgedCtx struct{ *textCtx }

func (f *forgedCtx) Data() string { return "999" } // somebody else's id, say

func TestUserCallbacksActOnTheSenderOnlyWhateverTheCallbackData(t *testing.T) {
	f := &fakePrize{
		start:   service.Claim{Kind: service.ClaimAskName},
		state:   service.Claim{Kind: service.ClaimAskName},
		submit:  service.Claim{Kind: service.ClaimAskConsent, Name: "Иван Петров"},
		consent: service.Claim{Kind: service.ClaimDone},
	}
	h := prizeHandlers(f)
	c := func(text string) *forgedCtx { return &forgedCtx{newTextCtx(text)} } // the sender's database id is 5

	for name, run := range map[string]func() error{
		"get prize":   func() error { return h.getPrize(c("")) },
		"name":        func() error { return h.onText(c("Иван Петров")) },
		"consent yes": func() error { return h.consentYes(c("")) },
		"consent no":  func() error { return h.consentNo(c("")) },
	} {
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(f.users) == 0 {
		t.Fatal("nothing was called")
	}
	for _, id := range f.users {
		if id != 5 {
			t.Errorf("the service was asked about user %d; only the sender (5) is allowed", id)
		}
	}
}
