package handlers

import (
	"context"
	"errors"
	"fmt"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

// Callback ids of the consent buttons.
const (
	uniqueConsentYes = "consent_yes"
	uniqueConsentNo  = "consent_no"
)

// PrizeClaims is the prize claim dialog (implemented by *service.FinalPrize).
// Its state lives in the database, so these handlers keep none.
type PrizeClaims interface {
	Start(ctx context.Context, userID int64) (service.Claim, error)
	State(ctx context.Context, userID int64) (service.Claim, error)
	SubmitName(ctx context.Context, userID int64, text string) (service.Claim, error)
	Consent(ctx context.Context, userID int64, agree bool) (service.Claim, error)
}

func consentMarkup() *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data(texts.BtnConsentYes, uniqueConsentYes), m.Data(texts.BtnConsentNo, uniqueConsentNo)))
	return m
}

// getPrize handles the "Get prize" button of the winner notification.
func (h *Handlers) getPrize(c tele.Context) error {
	user, ok := userOf(c)
	if !ok {
		return errors.New("prize requested without a user in context")
	}
	claim, err := h.Prize.Start(ctxOf(c), user.ID)
	if err != nil {
		return fmt.Errorf("start prize claim: %w", err)
	}
	switch claim.Kind {
	case service.ClaimAskName, service.ClaimReady:
		return c.Send(texts.AskContactName)
	case service.ClaimAskConsent:
		return h.askConsent(c, claim.Name)
	case service.ClaimDone:
		return c.Send(texts.PrizeAlreadyReceived)
	}
	return c.Send(closedClaimText(claim))
}

// onText routes free text into the dialog when the user is in the middle of one.
func (h *Handlers) onText(c tele.Context) error {
	user, ok := userOf(c)
	if !ok {
		return nil
	}
	state, err := h.Prize.State(ctxOf(c), user.ID)
	if err != nil {
		return fmt.Errorf("prize dialog state: %w", err)
	}
	switch state.Kind {
	case service.ClaimAskName:
		claim, err := h.Prize.SubmitName(ctxOf(c), user.ID, c.Text())
		if errors.Is(err, service.ErrBadContactName) {
			return c.Send(texts.BadContactName)
		}
		if err != nil {
			return fmt.Errorf("submit contact name: %w", err)
		}
		if claim.Kind == service.ClaimAskConsent {
			return h.askConsent(c, claim.Name)
		}
		return c.Send(closedClaimText(claim))
	case service.ClaimAskConsent:
		return c.Send(texts.ConsentNudge)
	case service.ClaimExpired:
		return c.Send(texts.PrizeDeadlinePassed)
	}
	return nil // not in a dialog: ordinary text is ignored
}

func (h *Handlers) askConsent(c tele.Context, name string) error {
	return c.Send(texts.ConsentPrompt(name), tele.ModeHTML, consentMarkup())
}

func (h *Handlers) consentYes(c tele.Context) error { return h.consent(c, true) }
func (h *Handlers) consentNo(c tele.Context) error  { return h.consent(c, false) }

func (h *Handlers) consent(c tele.Context, agree bool) error {
	user, ok := userOf(c)
	if !ok {
		return errors.New("consent answered without a user in context")
	}
	claim, err := h.Prize.Consent(ctxOf(c), user.ID, agree)
	if err != nil {
		return fmt.Errorf("record consent: %w", err)
	}
	switch claim.Kind {
	case service.ClaimDone:
		return c.Send(texts.ConsentThanks)
	case service.ClaimForfeited:
		if claim.Reason == "declined" {
			return c.Send(texts.ConsentDeclined)
		}
		return c.Send(texts.PrizeDeadlinePassed)
	case service.ClaimExpired:
		return c.Send(texts.PrizeDeadlinePassed)
	case service.ClaimAskName:
		return c.Send(texts.AskContactName)
	case service.ClaimAskConsent:
		return h.askConsent(c, claim.Name)
	}
	return c.Send(texts.PrizeDialogStale)
}

// closedClaimText explains why there is nothing (more) to claim.
func closedClaimText(c service.Claim) string {
	switch {
	case c.Kind == service.ClaimForfeited && c.Reason == "declined":
		return texts.PrizeDeclinedAlready
	case c.Kind == service.ClaimForfeited, c.Kind == service.ClaimExpired:
		return texts.PrizeDeadlinePassed
	}
	return texts.NoActivePrize
}
