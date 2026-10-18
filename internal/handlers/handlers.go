// Package handlers wires Telegram updates to bot behaviour.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

// Deps are the collaborators of the handlers.
type Deps struct {
	Log      *slog.Logger
	Users    UserStore
	Campaign *service.Campaign
	Promo    *service.Promo
	Entry    Joiner
	Chances  ChancesViewer
	Prize    PrizeClaims
	Metrics  Metrics // optional

	// MaxConcurrentUpdates bounds the updates handled at once (0: unlimited).
	MaxConcurrentUpdates int
	Location             *time.Location // for dates shown to users
}

// Handlers holds the bot's update handlers.
type Handlers struct {
	Deps
	menu   *tele.ReplyMarkup
	join   tele.Btn  // standalone copy of the "join" button for messages outside the menu
	checks *cooldown // rate limit of subscription checks per user
}

// checkInterval is the minimum gap between two subscription checks of one user.
const checkInterval = 2 * time.Second

// route binds a main-menu button to its callback handler.
type route struct {
	label  string
	unique string
	handle tele.HandlerFunc
}

// Register installs middleware and all handlers on the bot.
func Register(b Router, deps Deps) {
	h := &Handlers{Deps: deps, checks: newCooldown(checkInterval)}
	if deps.Metrics == nil {
		deps.Metrics = noopMetrics{}
	}

	// Order matters: panics are caught outermost; duplicates are dropped before
	// any work; the concurrency limit sits before the per-update timeout starts,
	// so waiting for a slot does not eat into the handler's time budget.
	b.Use(
		recoverMW(deps.Log, deps.Metrics),
		dedupMW(newDedup(), deps.Log, deps.Metrics),
		loggingMW(deps.Log),
		limitMW(deps.MaxConcurrentUpdates, requestTimeout),
		contextMW(),
		userMW(deps.Users),
		blockedMW(deps.Users, deps.Log),
	)

	// The single table of callback routes; its order is the menu order.
	routes := []route{
		{texts.BtnGetPromo, "get_promo", h.getPromo},
		{texts.BtnJoin, "join", h.joinDraw},
		{texts.BtnHowTo, "how_to", h.howTo},
		{texts.BtnChances, "chances", h.chances},
		{texts.BtnRules, "rules", h.rules},
	}

	h.menu = &tele.ReplyMarkup{}
	rows := make([]tele.Row, 0, len(routes))
	for _, r := range routes {
		btn := h.menu.Data(r.label, r.unique)
		if r.unique == "join" {
			h.join = btn
		}
		rows = append(rows, h.menu.Row(btn))
		b.Handle(&btn, answering(r.handle))
	}
	h.menu.Inline(rows...)

	check := h.menu.Data(texts.BtnCheckSub, uniqueCheckSub)
	b.Handle(&check, answering(h.checkSub))

	prize := h.menu.Data(texts.BtnGetPrize, service.UniqueGetPrize)
	b.Handle(&prize, answering(h.getPrize))
	yes := h.menu.Data(texts.BtnConsentYes, uniqueConsentYes)
	no := h.menu.Data(texts.BtnConsentNo, uniqueConsentNo)
	b.Handle(&yes, answering(h.consentYes))
	b.Handle(&no, answering(h.consentNo))
	b.Handle(tele.OnText, h.onText)

	b.Handle("/start", h.start)
	b.Handle(tele.OnCallback, answering(func(tele.Context) error { return nil })) // stale buttons
}

// answering acknowledges the callback query first so the client stops showing a spinner.
func answering(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if err := c.Respond(); err != nil {
			slog.Debug("answer callback", "err", err)
		}
		return next(c)
	}
}

func (h *Handlers) start(c tele.Context) error {
	return c.Send(texts.Welcome, h.menu)
}

func (h *Handlers) howTo(c tele.Context) error {
	return c.Send(texts.HowToParticipate())
}

func (h *Handlers) rules(c tele.Context) error {
	return c.Send(texts.Rules())
}

func (h *Handlers) chances(c tele.Context) error {
	user, ok := userOf(c)
	if !ok {
		return errors.New("chances requested without a user in context")
	}
	v, err := h.Chances.View(ctxOf(c), user.ID)
	if err != nil {
		return fmt.Errorf("chances view: %w", err)
	}
	days := make([]texts.DayLine, len(v.Days))
	for i, d := range v.Days {
		days[i] = texts.DayLine{Date: d.Day, Partner: d.Partner}
	}
	partners := make([]texts.PartnerLine, len(v.Partners))
	for i, p := range v.Partners {
		partners[i] = texts.PartnerLine{Name: p.Name, Promo: p.Promo, Entry: p.Entry}
	}
	return c.Send(texts.ChancesScreen(v.Chances, v.Max, days, partners, h.Campaign.DaysLeft()), tele.ModeHTML)
}

func (h *Handlers) getPromo(c tele.Context) error {
	user, ok := userOf(c)
	if !ok {
		return errors.New("promo requested without a user in context")
	}
	return h.withActiveDay(c, func(ad *service.ActiveDay) error {
		res, err := h.Promo.Issue(ctxOf(c), user.ID, ad)
		if errors.Is(err, service.ErrNoPromoCodes) {
			markup := &tele.ReplyMarkup{}
			markup.Inline(markup.Row(h.join))
			return c.Send(texts.PromoSoldOut(ad.Partner.Name), tele.ModeHTML, markup)
		}
		if err != nil {
			return fmt.Errorf("issue promo: %w", err)
		}
		var until *time.Time
		if ad.Partner.PromoValidUntil.Valid {
			until = &ad.Partner.PromoValidUntil.Time
		}
		return c.Send(texts.PromoIssued(ad.Partner.Name, res.Code, until, ad.Partner.PromoText, h.Location), tele.ModeHTML)
	})
}

// withActiveDay runs fn if a campaign day is active; otherwise it politely
// tells the user why there is nothing to do.
func (h *Handlers) withActiveDay(c tele.Context, fn func(*service.ActiveDay) error) error {
	ad, err := h.Campaign.Active(ctxOf(c))
	if err != nil {
		msg, ok := unavailableText(err, h.Campaign)
		if !ok {
			return fmt.Errorf("resolve active day: %w", err)
		}
		return c.Send(msg)
	}
	return fn(ad)
}

// unavailableText maps "no campaign right now" errors to user messages.
// ok is false for unexpected errors.
func unavailableText(err error, camp *service.Campaign) (msg string, ok bool) {
	switch {
	case errors.Is(err, service.ErrCampaignNotStarted):
		return texts.CampaignNotStarted(camp.Start()), true
	case errors.Is(err, service.ErrCampaignFinished):
		return texts.CampaignFinished, true
	case errors.Is(err, service.ErrNoCampaignDay):
		return texts.NoPartnerToday, true
	}
	return "", false
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// ChancesViewer builds the chances screen data (implemented by *service.Chances).
type ChancesViewer interface {
	View(ctx context.Context, userID int64) (service.ChancesView, error)
}
