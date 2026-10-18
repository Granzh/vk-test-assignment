package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

var (
	// ErrFinalNotOver means the campaign is still running; the final draw needs force.
	ErrFinalNotOver = errors.New("the campaign is not over yet")
	// ErrFinalNoTickets means nobody has a chance, so there is nothing to draw.
	ErrFinalNoTickets = errors.New("no tickets in the drum")
	// ErrFinalNotRun means the final draw has not been held yet.
	ErrFinalNotRun = errors.New("the final draw has not been held yet")
)

// ButtonSender delivers an HTML message with one callback button.
type ButtonSender interface {
	SendWithButton(ctx context.Context, telegramID int64, html, label, unique string) error
}

// FinalDrawConfig holds the rules of the final draw.
type FinalDrawConfig struct {
	Winners         int           // users who win; each wins at most once
	Reserves        int           // length of the reserve queue
	PrizeTickets    int           // prize tickets each winner gets
	Prize           string        // what winners are told they won
	ResponseTimeout time.Duration // how long a notified winner has to respond
	MaxChances      int           // cap of tickets per user
}

// FinalWinner is a winner or reserve of the final draw.
type FinalWinner struct {
	TelegramID int64
	Username   string
	FirstName  string
	Tickets    int // drum tickets the user held
	Reserve    bool
	Position   int // place in the reserve queue (1 = first), 0 for winners
	Status     string
	Deadline   time.Time
	// Contact is the full name, set only while a consent is on record.
	Contact       string
	ConsentAt     time.Time
	DialogStep    string // name, consent or empty
	ForfeitReason string // expired, declined, undeliverable for forfeited prizes
	Promoted      bool   // a reserve who has been given the prize
	Purged        bool   // the name was removed after the campaign
}

// FinalResult describes the held final draw.
type FinalResult struct {
	Draw db.FinalDraw
	// Already is true when the draw had been held before this call; the stored
	// result is returned unchanged.
	Already bool
	Winners []FinalWinner // winners first, then the reserve queue in order
}

// FinalDraw holds the final prize draw (spec 5.2).
//
// Rules: every active day of a user is one ticket in the drum. Winners are
// drawn ticket by ticket; once a user is drawn, ALL their remaining tickets
// leave the drum, so a user wins at most once (and is never both a winner and
// a reserve). Each winner then gets PrizeTickets prize tickets. After the
// winners, the reserve queue is drawn from the same drum the same way.
type FinalDraw struct {
	tx     TxRunner
	sender ButtonSender
	camp   *Campaign
	cfg    FinalDrawConfig
	log    *slog.Logger
	intn   func(n int) (int, error)
}

// NewFinalDraw builds the service.
func NewFinalDraw(tx TxRunner, sender ButtonSender, camp *Campaign, cfg FinalDrawConfig, log *slog.Logger) *FinalDraw {
	return &FinalDraw{tx: tx, sender: sender, camp: camp, cfg: cfg, log: log, intn: cryptoIntn}
}

// pickByTickets draws up to k distinct holders. tickets[i] is the number of
// tickets of holder i. Each step takes one uniformly random ticket from the
// drum and removes every ticket of its owner, so the owner cannot be drawn
// again: this is weighted sampling without replacement, where the chance to
// be drawn first is proportional to the ticket count. It returns holder
// indexes in the order drawn; fewer than k if the drum runs out of holders.
func pickByTickets(tickets []int, k int, intn func(int) (int, error)) ([]int, error) {
	left := make([]int, len(tickets))
	total := 0
	for i, n := range tickets {
		if n > 0 {
			left[i] = n
			total += n
		}
	}
	var picked []int
	for len(picked) < k && total > 0 {
		r, err := intn(total)
		if err != nil {
			return nil, err
		}
		for i, n := range left {
			if r < n {
				picked = append(picked, i)
				total -= n
				left[i] = 0 // the winner's other tickets leave the drum
				break
			}
			r -= n
		}
	}
	return picked, nil
}

// Run holds the final draw, or returns the stored result if it was held
// already (it is held once and never changes). It needs the campaign to be
// over unless force is set. Nothing is sent to users; see Confirm.
func (f *FinalDraw) Run(ctx context.Context, adminID int64, force bool) (FinalResult, error) {
	var res FinalResult
	err := f.tx.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockFinalDrawStage(ctx); err != nil {
			return fmt.Errorf("lock final draw: %w", err)
		}
		existing, err := q.GetFinalDrawOnly(ctx)
		if err == nil {
			res, err = f.load(ctx, q, existing)
			res.Already = true
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get final draw: %w", err)
		}

		if !force && !f.camp.Today().After(f.camp.end) {
			return ErrFinalNotOver
		}
		res, err = f.draw(ctx, q, adminID)
		return err
	})
	if err != nil {
		return FinalResult{}, err
	}
	if !res.Already {
		f.log.Info("final draw held", "admin_id", adminID, "held_at", res.Draw.HeldAt.Time.UTC().Format(time.RFC3339),
			"drum_tickets", res.Draw.DrumTickets, "drum_users", res.Draw.DrumUsers,
			"winners", countWinners(res.Winners, false), "reserves", countWinners(res.Winners, true))
	}
	return res, nil
}

func countWinners(ws []FinalWinner, reserve bool) int {
	n := 0
	for _, w := range ws {
		if w.Reserve == reserve {
			n++
		}
	}
	return n
}

func (f *FinalDraw) draw(ctx context.Context, q *db.Queries, adminID int64) (FinalResult, error) {
	rows, err := q.ListChancesPerUser(ctx)
	if err != nil {
		return FinalResult{}, fmt.Errorf("list tickets: %w", err)
	}
	holders := make([]int64, 0, len(rows)) // user ids
	tickets := make([]int, 0, len(rows))
	drum := 0
	for _, r := range rows {
		n := min(int(r.Tickets), f.cfg.MaxChances)
		if n <= 0 {
			continue
		}
		holders = append(holders, r.UserID)
		tickets = append(tickets, n)
		drum += n
	}
	if drum == 0 {
		return FinalResult{}, ErrFinalNoTickets
	}

	order, err := pickByTickets(tickets, f.cfg.Winners+f.cfg.Reserves, f.intn)
	if err != nil {
		return FinalResult{}, err
	}

	fd, err := q.CreateFinalDrawHeld(ctx, db.CreateFinalDrawHeldParams{
		HeldByTelegramID: adminID, DrumTickets: int32(drum), DrumUsers: int32(len(holders)),
		WinnersCount: int32(f.cfg.Winners), ReservesCount: int32(f.cfg.Reserves), PrizeTickets: int32(f.cfg.PrizeTickets),
	})
	if err != nil {
		return FinalResult{}, fmt.Errorf("create final draw: %w", err)
	}
	for i, idx := range order {
		reserve := i >= f.cfg.Winners
		pos := pgtype.Int4{}
		if reserve {
			pos = pgtype.Int4{Int32: int32(i - f.cfg.Winners + 1), Valid: true}
		}
		if _, err := q.InsertFinalWinnerRanked(ctx, db.InsertFinalWinnerRankedParams{
			FinalDrawID: fd.ID, UserID: holders[idx], TicketsCount: int32(tickets[idx]), IsReserve: reserve, ReservePosition: pos,
		}); err != nil {
			return FinalResult{}, fmt.Errorf("insert final winner: %w", err)
		}
	}
	if err := auditLog(ctx, q, adminID, "final_run", "final_draw", fd.ID, map[string]any{
		"drum_tickets": drum, "drum_users": len(holders), "winners_wanted": f.cfg.Winners,
		"reserves_wanted": f.cfg.Reserves, "prize_tickets": f.cfg.PrizeTickets, "picked": len(order),
	}); err != nil {
		return FinalResult{}, err
	}
	return f.load(ctx, q, fd)
}

// Result returns the stored final draw.
func (f *FinalDraw) Result(ctx context.Context) (FinalResult, error) {
	var res FinalResult
	err := f.tx.InTx(ctx, func(q *db.Queries) error {
		fd, err := q.GetFinalDrawOnly(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFinalNotRun
		}
		if err != nil {
			return fmt.Errorf("get final draw: %w", err)
		}
		res, err = f.load(ctx, q, fd)
		res.Already = true
		return err
	})
	return res, err
}

func (f *FinalDraw) load(ctx context.Context, q *db.Queries, fd db.FinalDraw) (FinalResult, error) {
	rows, err := q.ListFinalDrawWinners(ctx, fd.ID)
	if err != nil {
		return FinalResult{}, fmt.Errorf("list final winners: %w", err)
	}
	res := FinalResult{Draw: fd}
	for _, r := range rows {
		w := FinalWinner{
			TelegramID: r.TelegramID, Username: r.Username.String, FirstName: r.FirstName.String,
			Tickets: int(r.TicketsCount), Reserve: r.IsReserve, Position: int(r.ReservePosition.Int32),
			Status: r.Status, Deadline: r.Deadline.Time, ConsentAt: r.ConsentGivenAt.Time,
			DialogStep: r.DialogStep.String, ForfeitReason: r.ForfeitReason.String, Promoted: r.PromotedAt.Valid,
			Purged: r.ContactPurgedAt.Valid,
		}
		if r.ConsentGivenAt.Valid { // a name without consent is never exposed
			w.Contact = r.ContactName.String
		}
		res.Winners = append(res.Winners, w)
	}
	return res, nil
}

// FinalConfirmResult is the outcome of Confirm.
type FinalConfirmResult struct {
	Draw    db.FinalDraw
	Winners int // winners (reserves are not notified)
	Sent    int
	Already int
	Failed  []NotifyFailure
	Done    bool
}

// Confirm approves the held final draw and notifies its winners (not the
// reserves: they are called only if a winner drops out). The first call moves
// the draw to "confirmed"; failed deliveries can be retried by calling it
// again, and each winner is claimed atomically before sending, so nobody is
// notified twice. Notified winners get a response deadline of ResponseTimeout.
func (f *FinalDraw) Confirm(ctx context.Context, adminID int64) (FinalConfirmResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drawTimeout)
	defer cancel()

	var res FinalConfirmResult
	err := f.tx.InTx(ctx, func(q *db.Queries) error {
		fd, err := q.GetFinalDrawOnly(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFinalNotRun
		}
		if err != nil {
			return fmt.Errorf("get final draw: %w", err)
		}
		if fd, err = q.LockFinalDrawRow(ctx, fd.ID); err != nil {
			return fmt.Errorf("lock final draw: %w", err)
		}
		if fd.Status == "drawn" {
			if fd, err = q.SetFinalDrawStatus(ctx, db.SetFinalDrawStatusParams{ID: fd.ID, Status: "confirmed"}); err != nil {
				return fmt.Errorf("set final draw confirmed: %w", err)
			}
			if err := auditLog(ctx, q, adminID, "final_confirm", "final_draw", fd.ID, nil); err != nil {
				return err
			}
		}
		res.Draw = fd
		return nil
	})
	if err != nil {
		return FinalConfirmResult{}, err
	}
	if err := f.notify(ctx, adminID, &res); err != nil {
		return res, err
	}
	return res, nil
}

func (f *FinalDraw) notify(ctx context.Context, adminID int64, res *FinalConfirmResult) error {
	var rows []db.ListFinalDrawWinnersRow
	if err := f.tx.InTx(ctx, func(q *db.Queries) (err error) {
		rows, err = q.ListFinalDrawWinners(ctx, res.Draw.ID)
		return err
	}); err != nil {
		return fmt.Errorf("list final winners: %w", err)
	}

	text := texts.FinalWinner(f.cfg.Prize, int(res.Draw.PrizeTickets))
	for _, w := range rows {
		if w.IsReserve {
			continue
		}
		res.Winners++
		if w.Status != "selected" {
			res.Already++
			continue
		}

		claimed := false
		deadline := pgtype.Timestamptz{Time: time.Now().Add(f.cfg.ResponseTimeout).UTC(), Valid: true}
		if err := f.tx.InTx(ctx, func(q *db.Queries) error {
			_, err := q.ClaimFinalWinnerForNotify(ctx, db.ClaimFinalWinnerForNotifyParams{ID: w.ID, Deadline: deadline})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			claimed = err == nil
			return err
		}); err != nil {
			return fmt.Errorf("claim final winner: %w", err)
		}
		if !claimed {
			res.Already++
			continue
		}

		if sendErr := f.sender.SendWithButton(ctx, w.TelegramID, text, texts.BtnGetPrize, UniqueGetPrize); sendErr != nil {
			f.log.Error("notify final winner", "telegram_id", w.TelegramID, "err", sendErr)
			res.Failed = append(res.Failed, NotifyFailure{TelegramID: w.TelegramID, Err: sendErr})
			if err := f.tx.InTx(ctx, func(q *db.Queries) error { return q.ReleaseFinalWinner(ctx, w.ID) }); err != nil {
				return fmt.Errorf("release final winner claim: %w", err)
			}
			continue
		}
		res.Sent++
	}

	res.Done = len(res.Failed) == 0
	return f.tx.InTx(ctx, func(q *db.Queries) error {
		if res.Done && res.Draw.Status != "notified" {
			fd, err := q.SetFinalDrawStatus(ctx, db.SetFinalDrawStatusParams{ID: res.Draw.ID, Status: "notified"})
			if err != nil {
				return fmt.Errorf("set final draw notified: %w", err)
			}
			res.Draw = fd
		}
		return auditLog(ctx, q, adminID, "final_notify", "final_draw", res.Draw.ID, map[string]any{
			"winners": res.Winners, "sent": res.Sent, "already": res.Already, "failed": len(res.Failed),
		})
	})
}

// UniqueGetPrize is the callback id of the "get prize" button of the winner notification.
const UniqueGetPrize = "get_prize"
