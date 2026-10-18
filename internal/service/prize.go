package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

var (
	// ErrBadContactName means the text is not a plausible first and last name.
	ErrBadContactName = errors.New("not a valid full name")
	// ErrPurgeTooEarly means contacts are purged only after the campaign.
	ErrPurgeTooEarly = errors.New("the campaign is not over yet")
)

// ClaimKind is where a final winner stands in the prize claim.
type ClaimKind int

// Claim kinds.
const (
	// ClaimNone: the user has no prize to claim (or no dialog is running).
	ClaimNone ClaimKind = iota
	// ClaimReady: notified winner whose dialog has not started.
	ClaimReady
	// ClaimAskName: waiting for the first and last name.
	ClaimAskName
	// ClaimAskConsent: name received, waiting for the explicit consent.
	ClaimAskConsent
	// ClaimDone: consent given, data received.
	ClaimDone
	// ClaimForfeited: the prize was taken away (see Claim.Reason).
	ClaimForfeited
	// ClaimExpired: the response deadline has passed (the sweeper has not yet processed it).
	ClaimExpired
)

// Claim is the state of a user's prize claim.
type Claim struct {
	Kind   ClaimKind
	Name   string // the name awaiting consent, for ClaimAskConsent
	Reason string // forfeit reason for ClaimForfeited: expired, declined, undeliverable
}

// claimOf derives the claim state from a final_winners row.
func claimOf(w db.FinalWinner, now time.Time) Claim {
	switch w.Status {
	case "contact_received":
		return Claim{Kind: ClaimDone}
	case "forfeited":
		return Claim{Kind: ClaimForfeited, Reason: w.ForfeitReason.String}
	case "notified":
		if w.Deadline.Valid && !w.Deadline.Time.After(now) {
			return Claim{Kind: ClaimExpired}
		}
		switch w.DialogStep.String {
		case "name":
			return Claim{Kind: ClaimAskName}
		case "consent":
			return Claim{Kind: ClaimAskConsent, Name: w.ContactName.String}
		}
		return Claim{Kind: ClaimReady}
	}
	return Claim{Kind: ClaimNone}
}

// PrizeConfig holds the rules of the prize claim.
type PrizeConfig struct {
	Prize           string
	ResponseTimeout time.Duration
}

// FinalPrize runs the prize claim of the final draw winners: the contact
// dialog (a state machine stored in final_winners, so it survives restarts),
// the response deadline and the hand-over of expired prizes to the reserve queue.
type FinalPrize struct {
	tx     TxRunner
	sender interface {
		Sender
		ButtonSender
	}
	camp *Campaign
	cfg  PrizeConfig
	log  *slog.Logger
	// NotifyAdmins tells the admins about prize queue events (HTML); may be nil.
	NotifyAdmins func(ctx context.Context, html string)
	now          func() time.Time
}

// NewFinalPrize builds the service.
func NewFinalPrize(tx TxRunner, sender interface {
	Sender
	ButtonSender
}, camp *Campaign, cfg PrizeConfig, log *slog.Logger) *FinalPrize {
	return &FinalPrize{tx: tx, sender: sender, camp: camp, cfg: cfg, log: log, now: time.Now}
}

func (p *FinalPrize) winnerOf(ctx context.Context, q *db.Queries, userID int64) (db.FinalWinner, bool, error) {
	w, err := q.GetFinalWinnerOfUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.FinalWinner{}, false, nil
	}
	if err != nil {
		return db.FinalWinner{}, false, fmt.Errorf("get final winner: %w", err)
	}
	return w, true, nil
}

// State reports the running dialog of the user without changing anything;
// ClaimNone means there is nothing to react to.
func (p *FinalPrize) State(ctx context.Context, userID int64) (Claim, error) {
	var c Claim
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		w, ok, err := p.winnerOf(ctx, q, userID)
		if err != nil || !ok {
			return err
		}
		c = claimOf(w, p.now())
		if c.Kind == ClaimReady {
			c = Claim{Kind: ClaimNone}
		}
		return nil
	})
	return c, err
}

// Start handles the "Get prize" button: it begins the contact dialog (or
// repeats the current question) for an active winner.
func (p *FinalPrize) Start(ctx context.Context, userID int64) (Claim, error) {
	var c Claim
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		w, ok, err := p.winnerOf(ctx, q, userID)
		if err != nil || !ok {
			return err
		}
		c = claimOf(w, p.now())
		if c.Kind != ClaimReady {
			return nil
		}
		started, err := q.StartFinalContactDialog(ctx, w.ID)
		if errors.Is(err, pgx.ErrNoRows) { // lost a race with the deadline or another press
			again, err := q.GetFinalWinnerOfUser(ctx, userID)
			if err != nil {
				return fmt.Errorf("get final winner: %w", err)
			}
			c = claimOf(again, p.now())
			return nil
		}
		if err != nil {
			return fmt.Errorf("start contact dialog: %w", err)
		}
		c = claimOf(started, p.now())
		return nil
	})
	return c, err
}

// SubmitName stores the full name sent by the user and moves the dialog to the
// consent step. It returns ErrBadContactName (changing nothing) for text that
// is not a plausible name, and a state other than ClaimAskConsent if the
// dialog is not at the name step any more.
func (p *FinalPrize) SubmitName(ctx context.Context, userID int64, text string) (Claim, error) {
	name, ok := ParseContactName(text)
	if !ok {
		return Claim{}, ErrBadContactName
	}
	var c Claim
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		w, ok, err := p.winnerOf(ctx, q, userID)
		if err != nil || !ok {
			return err
		}
		saved, err := q.SaveFinalContactName(ctx, db.SaveFinalContactNameParams{ID: w.ID, FullName: name})
		if errors.Is(err, pgx.ErrNoRows) {
			c = claimOf(w, p.now())
			return nil
		}
		if err != nil {
			return fmt.Errorf("save contact name: %w", err)
		}
		c = claimOf(saved, p.now())
		return nil
	})
	return c, err
}

// Consent records the answer to the consent question. Agreeing completes the
// claim (ClaimDone) with the consent time stored; refusing forfeits the
// prize, wipes the name and hands the prize to the next reserve
// (ClaimForfeited, reason "declined"). Any other state is returned unchanged.
func (p *FinalPrize) Consent(ctx context.Context, userID int64, agree bool) (Claim, error) {
	var (
		c         Claim
		forfeited *db.FinalWinner
	)
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		w, ok, err := p.winnerOf(ctx, q, userID)
		if err != nil || !ok {
			return err
		}
		c = claimOf(w, p.now())
		if c.Kind != ClaimAskConsent {
			return nil
		}
		if agree {
			given, err := q.GiveFinalConsent(ctx, w.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				c = Claim{Kind: ClaimExpired}
				return nil
			}
			if err != nil {
				return fmt.Errorf("give consent: %w", err)
			}
			c = claimOf(given, p.now())
			return nil
		}
		f, err := q.ForfeitFinalWinner(ctx, db.ForfeitFinalWinnerParams{ID: w.ID, Reason: "declined"})
		if errors.Is(err, pgx.ErrNoRows) {
			c = Claim{Kind: ClaimNone}
			return nil
		}
		if err != nil {
			return fmt.Errorf("forfeit winner: %w", err)
		}
		forfeited = &f
		c = claimOf(f, p.now())
		return auditLog(ctx, q, SystemAdminID, "prize_declined", "final_winner", f.ID, nil)
	})
	if err != nil {
		return c, err
	}
	if forfeited != nil {
		p.passOn(ctx, forfeited.FinalDrawID, "declined")
	}
	return c, nil
}

// SweepExpired processes every notified winner whose response deadline has
// passed: the prize is taken away, the old winner is told that the time is
// over and the prize goes to the next reserve, who is notified. It returns
// how many prizes expired. It is safe to run concurrently with itself.
func (p *FinalPrize) SweepExpired(ctx context.Context) (int, error) {
	var due []db.FinalWinner
	if err := p.tx.InTx(ctx, func(q *db.Queries) (err error) {
		due, err = q.ListExpiredFinalWinners(ctx, pgtype.Timestamptz{Time: p.now().UTC(), Valid: true})
		return err
	}); err != nil {
		return 0, fmt.Errorf("list expired winners: %w", err)
	}

	n := 0
	for _, w := range due {
		var gone *db.FinalWinner
		var telegramID int64
		err := p.tx.InTx(ctx, func(q *db.Queries) error {
			f, err := q.ForfeitFinalWinner(ctx, db.ForfeitFinalWinnerParams{ID: w.ID, Reason: "expired", OnlyExpired: true})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // answered or processed in the meantime
			}
			if err != nil {
				return fmt.Errorf("forfeit winner: %w", err)
			}
			u, err := q.GetUserByID(ctx, f.UserID)
			if err != nil {
				return fmt.Errorf("get user: %w", err)
			}
			gone, telegramID = &f, u.TelegramID
			return auditLog(ctx, q, SystemAdminID, "prize_expired", "final_winner", f.ID, nil)
		})
		if err != nil {
			return n, err
		}
		if gone == nil {
			continue
		}
		n++
		if err := p.sender.Send(ctx, telegramID, texts.PrizeExpired); err != nil {
			p.log.Error("tell winner the time is over", "telegram_id", telegramID, "err", err)
		}
		p.passOn(ctx, gone.FinalDrawID, "expired")
	}
	return n, nil
}

// passOn gives a freed prize to the next reserve who can be reached and
// reports the outcome to the admins. A reserve who cannot be messaged is
// forfeited ("undeliverable") and the next one is tried.
func (p *FinalPrize) passOn(ctx context.Context, drawID int64, why string) {
	var undeliverable []int64
	promotedPos := 0
	for {
		var next *db.FinalWinner
		var telegramID int64
		var prizeTickets int32
		err := p.tx.InTx(ctx, func(q *db.Queries) error {
			r, err := q.PromoteNextReserve(ctx, db.PromoteNextReserveParams{
				DrawID:      drawID,
				NewDeadline: pgtype.Timestamptz{Time: p.now().Add(p.cfg.ResponseTimeout).UTC(), Valid: true},
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("promote reserve: %w", err)
			}
			u, err := q.GetUserByID(ctx, r.UserID)
			if err != nil {
				return fmt.Errorf("get user: %w", err)
			}
			fd, err := q.GetFinalDraw(ctx, drawID)
			if err != nil {
				return fmt.Errorf("get final draw: %w", err)
			}
			next, telegramID, prizeTickets = &r, u.TelegramID, fd.PrizeTickets
			return auditLog(ctx, q, SystemAdminID, "prize_promoted", "final_winner", r.ID,
				map[string]any{"reserve_position": r.ReservePosition.Int32})
		})
		if err != nil {
			p.log.Error("promote reserve", "draw_id", drawID, "err", err)
			p.tell(ctx, fmt.Sprintf("❌ Не удалось передать приз резервисту: %s", err))
			return
		}
		if next == nil { // the queue is empty
			p.tell(ctx, texts.PrizeAdminEvent(why, 0, true, undeliverable))
			return
		}

		text := texts.FinalWinner(p.cfg.Prize, int(prizeTickets))
		if err := p.sender.SendWithButton(ctx, telegramID, text, texts.BtnGetPrize, UniqueGetPrize); err != nil {
			p.log.Error("notify promoted reserve", "telegram_id", telegramID, "err", err)
			undeliverable = append(undeliverable, telegramID)
			if err := p.tx.InTx(ctx, func(q *db.Queries) error {
				_, err := q.ForfeitFinalWinner(ctx, db.ForfeitFinalWinnerParams{ID: next.ID, Reason: "undeliverable"})
				if errors.Is(err, pgx.ErrNoRows) {
					return nil
				}
				return err
			}); err != nil {
				p.log.Error("forfeit undeliverable reserve", "err", err)
				return
			}
			continue
		}
		promotedPos = int(next.ReservePosition.Int32)
		break
	}
	p.tell(ctx, texts.PrizeAdminEvent(why, promotedPos, false, undeliverable))
}

func (p *FinalPrize) tell(ctx context.Context, html string) {
	if p.NotifyAdmins != nil {
		p.NotifyAdmins(ctx, html)
	}
}

// PurgeResult is the outcome of PurgeContacts.
type PurgeResult struct {
	Stored int // names stored before the call
	Purged int // names removed by this call (0 for a dry run)
}

// PurgeContacts removes the collected full names once the campaign is over.
// Without confirm it only reports how many are stored. Consent records and
// statuses stay, so the outcome of every prize remains auditable.
func (p *FinalPrize) PurgeContacts(ctx context.Context, adminID int64, confirm bool) (PurgeResult, error) {
	if !p.camp.Today().After(p.camp.end) {
		return PurgeResult{}, ErrPurgeTooEarly
	}
	var res PurgeResult
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		n, err := q.CountFinalContacts(ctx)
		if err != nil {
			return fmt.Errorf("count contacts: %w", err)
		}
		res.Stored = int(n)
		if !confirm {
			return nil
		}
		purged, err := q.PurgeFinalContacts(ctx)
		if err != nil {
			return fmt.Errorf("purge contacts: %w", err)
		}
		res.Purged = int(purged)
		return auditLog(ctx, q, adminID, "contacts_purge", "final_winner", 0, map[string]any{"purged": purged})
	})
	return res, err
}

// ParseContactName normalizes a full name typed by a user. It needs at least
// two words made of letters (hyphens, dots and apostrophes allowed), 3 to 100
// characters in total.
func ParseContactName(text string) (string, bool) {
	words := strings.Fields(text)
	if len(words) < 2 || len(words) > 6 {
		return "", false
	}
	name := strings.Join(words, " ")
	if n := utf8.RuneCountInString(name); n < 3 || n > 100 {
		return "", false
	}
	for _, w := range words {
		hasLetter := false
		for _, r := range w {
			switch {
			case unicode.IsLetter(r):
				hasLetter = true
			case r == '-' || r == '.' || r == '\'' || r == '’':
			default:
				return "", false
			}
		}
		if !hasLetter {
			return "", false
		}
	}
	return name, true
}
