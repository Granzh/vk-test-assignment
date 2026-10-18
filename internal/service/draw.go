package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

var (
	// ErrDrawDayNotFound means no partner is assigned to the date.
	ErrDrawDayNotFound = errors.New("no campaign day for the date")
	// ErrDrawDayNotOver means the day is still running; the draw needs force.
	ErrDrawDayNotOver = errors.New("the campaign day is not over yet")
	// ErrDrawDayInFuture means the day has not started.
	ErrDrawDayInFuture = errors.New("the campaign day has not started")
	// ErrDrawNotRun means the day has not been drawn yet (nothing to confirm).
	ErrDrawNotRun = errors.New("the draw has not been held yet")
)

// drawTimeout bounds one draw including the subscription re-checks; it is
// independent of the (short) per-update context.
const drawTimeout = 5 * time.Minute

// SystemAdminID is the admin_telegram_id recorded for actions of the scheduler.
const SystemAdminID int64 = 0

// Sender delivers an HTML message to a Telegram user.
type Sender interface {
	Send(ctx context.Context, telegramID int64, html string) error
}

// DrawWinner is a selected winner.
type DrawWinner struct {
	TelegramID int64
	Username   string
	FirstName  string
	Prize      string
	Status     string
}

// DrawResult describes a held daily draw.
type DrawResult struct {
	Day     db.CampaignDay
	Partner db.Partner
	// Already is true when the draw had been held before this call; the
	// stored result is returned unchanged.
	Already      bool
	DrawnAt      time.Time
	Entries      int // all registrations of the day
	Excluded     int // dropped because they were no longer subscribed
	WinnersCount int // winners the day was configured for
	Winners      []DrawWinner
}

// DailyDraw holds the partner draws of campaign days (spec 5.1).
type DailyDraw struct {
	tx      TxRunner
	checker SubscriptionChecker
	sender  Sender
	camp    *Campaign
	log     *slog.Logger
	// intn returns a uniform random int in [0, n); crypto/rand in production.
	intn func(n int) (int, error)
}

// NewDailyDraw builds the service. sender delivers the winner notifications
// after the admin confirms the results.
func NewDailyDraw(tx TxRunner, checker SubscriptionChecker, sender Sender, camp *Campaign, log *slog.Logger) *DailyDraw {
	return &DailyDraw{tx: tx, checker: checker, sender: sender, camp: camp, log: log, intn: cryptoIntn}
}

func cryptoIntn(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, fmt.Errorf("crypto/rand: %w", err)
	}
	return int(v.Int64()), nil
}

// shuffle permutes n items uniformly (Fisher–Yates) using intn.
func shuffle(n int, intn func(int) (int, error), swap func(i, j int)) error {
	for i := n - 1; i > 0; i-- {
		j, err := intn(i + 1)
		if err != nil {
			return err
		}
		swap(i, j)
	}
	return nil
}

// Run holds the draw of the campaign day dated date, or returns the stored
// result if it was held already (the result never changes). The draw is only
// allowed after the day is over unless force is set (then today is allowed
// too). Winners are picked in uniformly random order; each candidate's
// subscription is re-checked and unsubscribed ones are excluded and replaced
// by the next candidate. If Telegram cannot answer for a candidate the whole
// draw is rolled back so it can be repeated: skipping the person silently
// would be unfair. Nothing is sent to users; see Confirm.
func (d *DailyDraw) Run(ctx context.Context, adminID int64, date time.Time, force bool) (DrawResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drawTimeout)
	defer cancel()

	var res DrawResult
	err := d.tx.InTx(ctx, func(q *db.Queries) error {
		day, err := q.GetCampaignDayByDate(ctx, pgtype.Date{Time: date, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDrawDayNotFound
		}
		if err != nil {
			return fmt.Errorf("get campaign day: %w", err)
		}
		// The exclusive lock serializes concurrent runs and entry creation.
		if day, err = q.LockCampaignDay(ctx, day.ID); err != nil {
			return fmt.Errorf("lock campaign day: %w", err)
		}
		if day.Status != "pending" {
			res, err = d.load(ctx, q, day)
			res.Already = true
			return err
		}

		switch today := d.camp.Today(); {
		case date.After(today):
			return ErrDrawDayInFuture
		case date.Equal(today) && !force:
			return ErrDrawDayNotOver
		}

		if res, err = d.draw(ctx, q, day); err != nil {
			return err
		}
		return auditLog(ctx, q, adminID, "draw_run", "campaign_day", day.ID, map[string]any{
			"day": date.Format(time.DateOnly), "force": force, "entries": res.Entries,
			"excluded": res.Excluded, "winners": len(res.Winners), "winners_wanted": res.WinnersCount,
		})
	})
	if err != nil {
		return DrawResult{}, err
	}
	return res, nil
}

// Result returns the stored result of a drawn day.
func (d *DailyDraw) Result(ctx context.Context, date time.Time) (DrawResult, error) {
	var res DrawResult
	err := d.tx.InTx(ctx, func(q *db.Queries) error {
		day, err := q.GetCampaignDayByDate(ctx, pgtype.Date{Time: date, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDrawDayNotFound
		}
		if err != nil {
			return fmt.Errorf("get campaign day: %w", err)
		}
		if day.Status == "pending" {
			return ErrDrawNotRun
		}
		res, err = d.load(ctx, q, day)
		res.Already = true
		return err
	})
	return res, err
}

func (d *DailyDraw) draw(ctx context.Context, q *db.Queries, day db.CampaignDay) (DrawResult, error) {
	partner, err := q.GetPartner(ctx, day.PartnerID)
	if err != nil {
		return DrawResult{}, fmt.Errorf("get partner: %w", err)
	}
	if !partner.ChatID.Valid {
		return DrawResult{}, fmt.Errorf("%w: partner %d has no chat_id", ErrSubscriptionCheck, partner.ID)
	}
	cands, err := q.ListDrawCandidates(ctx, day.ID)
	if err != nil {
		return DrawResult{}, fmt.Errorf("list candidates: %w", err)
	}
	if err := shuffle(len(cands), d.intn, func(i, j int) { cands[i], cands[j] = cands[j], cands[i] }); err != nil {
		return DrawResult{}, err
	}

	want := int(day.WinnersCount)
	var won []db.ListDrawCandidatesRow
	for _, c := range cands {
		if len(won) == want {
			break
		}
		ok, err := d.checker.IsSubscribed(ctx, partner.ChatID.Int64, c.TelegramID)
		if err != nil {
			return DrawResult{}, fmt.Errorf("%w: %w", ErrSubscriptionCheck, err)
		}
		if !ok {
			if err := q.ExcludeDailyEntry(ctx, db.ExcludeDailyEntryParams{ID: c.EntryID, Reason: "unsubscribed"}); err != nil {
				return DrawResult{}, fmt.Errorf("exclude entry: %w", err)
			}
			continue
		}
		won = append(won, c)
	}

	for _, w := range won {
		_, err := q.InsertDailyWinner(ctx, db.InsertDailyWinnerParams{
			CampaignDayID: day.ID, UserID: w.UserID, Prize: day.PrizesDescription,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return DrawResult{}, fmt.Errorf("insert winner: %w", err)
		}
	}
	if day, err = q.SetCampaignDayStatus(ctx, db.SetCampaignDayStatusParams{ID: day.ID, Status: "drawn"}); err != nil {
		return DrawResult{}, fmt.Errorf("set day drawn: %w", err)
	}
	return d.load(ctx, q, day)
}

// load assembles the stored result of a drawn day.
func (d *DailyDraw) load(ctx context.Context, q *db.Queries, day db.CampaignDay) (DrawResult, error) {
	partner, err := q.GetPartner(ctx, day.PartnerID)
	if err != nil {
		return DrawResult{}, fmt.Errorf("get partner: %w", err)
	}
	entries, err := q.CountDailyEntries(ctx, day.ID)
	if err != nil {
		return DrawResult{}, fmt.Errorf("count entries: %w", err)
	}
	excluded, err := q.CountExcludedEntries(ctx, day.ID)
	if err != nil {
		return DrawResult{}, fmt.Errorf("count excluded: %w", err)
	}
	rows, err := q.ListDayWinners(ctx, day.ID)
	if err != nil {
		return DrawResult{}, fmt.Errorf("list winners: %w", err)
	}
	res := DrawResult{
		Day: day, Partner: partner, DrawnAt: day.DrawnAt.Time,
		Entries: int(entries), Excluded: int(excluded), WinnersCount: int(day.WinnersCount),
	}
	for _, r := range rows {
		res.Winners = append(res.Winners, DrawWinner{
			TelegramID: r.TelegramID, Username: r.Username.String, FirstName: r.FirstName.String,
			Prize: r.Prize, Status: r.Status,
		})
	}
	return res, nil
}

// NotifyFailure is a winner who could not be notified.
type NotifyFailure struct {
	TelegramID int64
	Err        error
}

// ConfirmResult is the outcome of Confirm.
type ConfirmResult struct {
	Day     db.CampaignDay
	Partner db.Partner
	Winners int // all winners of the day
	Sent    int // notified by this call
	Already int // had been notified before
	Failed  []NotifyFailure
	// Done is true when every winner has been notified.
	Done bool
}

// Confirm approves a held draw and notifies its winners. The first call moves
// the day to "confirmed"; if some notifications fail the day stays confirmed
// and calling Confirm again retries only the winners not yet notified. Each
// winner is claimed atomically before sending, so concurrent calls never
// notify anybody twice.
func (d *DailyDraw) Confirm(ctx context.Context, adminID int64, date time.Time) (ConfirmResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drawTimeout)
	defer cancel()

	var res ConfirmResult
	err := d.tx.InTx(ctx, func(q *db.Queries) error {
		day, err := q.GetCampaignDayByDate(ctx, pgtype.Date{Time: date, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDrawDayNotFound
		}
		if err != nil {
			return fmt.Errorf("get campaign day: %w", err)
		}
		if day, err = q.LockCampaignDay(ctx, day.ID); err != nil {
			return fmt.Errorf("lock campaign day: %w", err)
		}
		switch day.Status {
		case "pending":
			return ErrDrawNotRun
		case "drawn":
			if day, err = q.SetCampaignDayStatus(ctx, db.SetCampaignDayStatusParams{ID: day.ID, Status: "confirmed"}); err != nil {
				return fmt.Errorf("set day confirmed: %w", err)
			}
			if err := auditLog(ctx, q, adminID, "draw_confirm", "campaign_day", day.ID,
				map[string]any{"day": date.Format(time.DateOnly)}); err != nil {
				return err
			}
		}
		res.Day = day
		if res.Partner, err = q.GetPartner(ctx, day.PartnerID); err != nil {
			return fmt.Errorf("get partner: %w", err)
		}
		return nil
	})
	if err != nil {
		return ConfirmResult{}, err
	}
	if err := d.notify(ctx, adminID, &res); err != nil {
		return res, err
	}
	return res, nil
}

func (d *DailyDraw) notify(ctx context.Context, adminID int64, res *ConfirmResult) error {
	var winners []db.ListDayWinnersRow
	err := d.tx.InTx(ctx, func(q *db.Queries) (err error) {
		winners, err = q.ListDayWinners(ctx, res.Day.ID)
		return err
	})
	if err != nil {
		return fmt.Errorf("list winners: %w", err)
	}
	res.Winners = len(winners)

	for _, w := range winners {
		if w.Status != "selected" {
			res.Already++
			continue
		}
		// Claim before sending; a failure releases the claim for a retry.
		claimed := false
		err := d.tx.InTx(ctx, func(q *db.Queries) error {
			_, err := q.ClaimDailyWinnerForNotify(ctx, w.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			claimed = err == nil
			return err
		})
		if err != nil {
			return fmt.Errorf("claim winner: %w", err)
		}
		if !claimed {
			res.Already++ // another confirm got there first
			continue
		}

		text := texts.DailyWinner(w.Prize, res.Partner.Name, texts.DailyWinnerInstruction)
		if sendErr := d.sender.Send(ctx, w.TelegramID, text); sendErr != nil {
			d.log.Error("notify daily winner", "telegram_id", w.TelegramID, "day_id", res.Day.ID, "err", sendErr)
			res.Failed = append(res.Failed, NotifyFailure{TelegramID: w.TelegramID, Err: sendErr})
			if err := d.tx.InTx(ctx, func(q *db.Queries) error {
				return q.SetDailyWinnerStatus(ctx, db.SetDailyWinnerStatusParams{ID: w.ID, Status: "selected"})
			}); err != nil {
				return fmt.Errorf("release winner claim: %w", err)
			}
			continue
		}
		res.Sent++
	}

	res.Done = len(res.Failed) == 0
	return d.tx.InTx(ctx, func(q *db.Queries) error {
		if res.Done && res.Day.Status != "notified" {
			day, err := q.SetCampaignDayStatus(ctx, db.SetCampaignDayStatusParams{ID: res.Day.ID, Status: "notified"})
			if err != nil {
				return fmt.Errorf("set day notified: %w", err)
			}
			res.Day = day
		}
		return auditLog(ctx, q, adminID, "draw_notify", "campaign_day", res.Day.ID, map[string]any{
			"winners": res.Winners, "sent": res.Sent, "already": res.Already, "failed": len(res.Failed),
		})
	})
}

func auditLog(ctx context.Context, q *db.Queries, adminID int64, action, entityType string, entityID int64, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal audit details: %w", err)
	}
	if err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{
		AdminTelegramID: adminID, Action: action, EntityType: entityType,
		EntityID: pgtype.Int8{Int64: entityID, Valid: true}, Details: raw,
	}); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}
