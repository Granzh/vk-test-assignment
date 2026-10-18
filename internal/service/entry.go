package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
)

var (
	// ErrNotSubscribed means the user is not subscribed to the partner's channel.
	ErrNotSubscribed = errors.New("user is not subscribed to the partner")
	// ErrSubscriptionCheck means the subscription could not be determined
	// (Telegram API failure, partner without chat_id). It wraps the cause.
	ErrSubscriptionCheck = errors.New("subscription check failed")
	// ErrDayClosed means the day's draw has already been held, so new entries are not accepted.
	ErrDayClosed = errors.New("the day's draw has already been held")
)

// SubscriptionChecker tells whether a Telegram user is subscribed to a chat.
// A non-nil error means "could not find out", not "not subscribed".
type SubscriptionChecker interface {
	IsSubscribed(ctx context.Context, chatID, telegramUserID int64) (bool, error)
}

// JoinResult is the outcome of a successful participation request.
type JoinResult struct {
	// Already is true when the user was registered for the day before.
	Already bool
	// Chances is the user's total number of chances in the final draw.
	Chances int
}

// Entry registers users in the daily partner draw.
type Entry struct {
	tx         TxRunner
	checker    SubscriptionChecker
	maxChances int
	now        func() time.Time
}

// NewEntry builds the service; maxChances caps the chances a user can collect.
func NewEntry(tx TxRunner, checker SubscriptionChecker, maxChances int) *Entry {
	return &Entry{tx: tx, checker: checker, maxChances: maxChances, now: time.Now}
}

// Join registers the user in the active day's draw if they are subscribed to
// the partner. It returns ErrNotSubscribed when they are not and an error
// wrapping ErrSubscriptionCheck when the subscription cannot be verified.
// Repeat calls and concurrent calls create one entry and one activity day;
// the others get Already.
func (e *Entry) Join(ctx context.Context, user db.User, ad *ActiveDay) (JoinResult, error) {
	// An existing entry needs no Telegram call.
	if res, found, err := e.existing(ctx, user.ID, ad); err != nil || found {
		return res, err
	}

	if !ad.Partner.ChatID.Valid {
		return JoinResult{}, fmt.Errorf("%w: partner %d has no chat_id", ErrSubscriptionCheck, ad.Partner.ID)
	}
	ok, err := e.checker.IsSubscribed(ctx, ad.Partner.ChatID.Int64, user.TelegramID)
	if err != nil {
		return JoinResult{}, fmt.Errorf("%w: %w", ErrSubscriptionCheck, err)
	}
	if !ok {
		return JoinResult{}, ErrNotSubscribed
	}

	var res JoinResult
	err = e.tx.InTx(ctx, func(q *db.Queries) error {
		// A shared lock keeps the draw (which locks the day exclusively) from
		// starting between this status check and the insert.
		day, err := q.GetCampaignDayShared(ctx, ad.Day.ID)
		if err != nil {
			return fmt.Errorf("lock campaign day: %w", err)
		}
		if day.Status != "pending" {
			return ErrDayClosed
		}
		_, err = q.CreateDailyEntry(ctx, db.CreateDailyEntryParams{
			UserID:              user.ID,
			CampaignDayID:       ad.Day.ID,
			SubscribedCheckedAt: pgtype.Timestamptz{Time: e.now().UTC(), Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) { // a concurrent request registered the user first
			res.Already = true
		} else if err != nil {
			return fmt.Errorf("create daily entry: %w", err)
		} else if err := recordActiveDay(ctx, q, user.ID, ad.Day.ID, e.maxChances); err != nil {
			return err
		}
		res.Chances, err = e.chances(ctx, q, user.ID)
		return err
	})
	if err != nil {
		return JoinResult{}, err
	}
	return res, nil
}

func (e *Entry) existing(ctx context.Context, userID int64, ad *ActiveDay) (res JoinResult, found bool, err error) {
	err = e.tx.InTx(ctx, func(q *db.Queries) error {
		_, err := q.GetDailyEntry(ctx, db.GetDailyEntryParams{UserID: userID, CampaignDayID: ad.Day.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get daily entry: %w", err)
		}
		found = true
		res.Already = true
		res.Chances, err = e.chances(ctx, q, userID)
		return err
	})
	return res, found, err
}

func (e *Entry) chances(ctx context.Context, q *db.Queries, userID int64) (int, error) {
	return chanceCount(ctx, q, userID, e.maxChances)
}
