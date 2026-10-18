package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"chat_bot/internal/storage/db"
)

// recordActiveDay counts the campaign day as active for the user, which gives
// one chance in the final draw. It is idempotent and respects the chance cap.
// It must run in the same transaction as the action that made the day active.
func recordActiveDay(ctx context.Context, q *db.Queries, userID, campaignDayID int64, maxChances int) error {
	n, err := q.CountActivityDays(ctx, userID)
	if err != nil {
		return fmt.Errorf("count activity days: %w", err)
	}
	if n >= int64(maxChances) {
		// The cap only matters for days not yet counted; a repeat is a no-op below.
		return nil
	}
	_, err = q.AddActivityDay(ctx, db.AddActivityDayParams{UserID: userID, CampaignDayID: campaignDayID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { // no row: already counted
		return fmt.Errorf("add activity day: %w", err)
	}
	return nil
}

// activityCounter is the query chance counting needs.
type activityCounter interface {
	CountActivityDays(ctx context.Context, userID int64) (int64, error)
}

// chanceCount returns the user's chances: the number of their active days,
// derived by counting rows (never stored separately), capped at maxChances.
func chanceCount(ctx context.Context, q activityCounter, userID int64, maxChances int) (int, error) {
	n, err := q.CountActivityDays(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("count activity days: %w", err)
	}
	return int(min(n, int64(maxChances))), nil
}
