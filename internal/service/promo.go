package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
)

// ErrNoPromoCodes means the partner has no free promo codes left.
var ErrNoPromoCodes = errors.New("no free promo codes")

// errAlreadyIssued aborts the transaction when a concurrent request issued a
// code to the same user for the same day first.
var errAlreadyIssued = errors.New("promo already issued concurrently")

// TxRunner runs fn in a database transaction (implemented by storage.Store).
type TxRunner interface {
	InTx(ctx context.Context, fn func(q *db.Queries) error) error
}

// IssuedPromo is the result of a promo code request.
type IssuedPromo struct {
	Code string
	// Already is true when the user had this day's code before.
	Already bool
}

// Promo issues promo codes.
type Promo struct {
	tx         TxRunner
	maxChances int
}

// NewPromo builds the service; maxChances caps the chances a user can collect.
func NewPromo(tx TxRunner, maxChances int) *Promo {
	return &Promo{tx: tx, maxChances: maxChances}
}

// Issue returns the user's promo code for the active day. The first call of
// the day claims a free code of the day's partner and counts the day as active;
// later calls return the same code. When the pool is empty it returns
// ErrNoPromoCodes and counts nothing.
func (p *Promo) Issue(ctx context.Context, userID int64, ad *ActiveDay) (IssuedPromo, error) {
	res, err := p.issueCode(ctx, userID, ad)
	switch {
	case err == nil:
		p.recordRequest(ctx, userID, ad, true)
	case errors.Is(err, ErrNoPromoCodes):
		p.recordRequest(ctx, userID, ad, false)
	}
	return res, err
}

// recordRequest counts the press of the promo button for the statistics. It is
// outside the issuing transaction (a sold-out request rolls that back) and a
// failure is only logged: the user must still get their answer.
func (p *Promo) recordRequest(ctx context.Context, userID int64, ad *ActiveDay, issued bool) {
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		return q.RecordPromoRequest(ctx, db.RecordPromoRequestParams{UserID: userID, CampaignDayID: ad.Day.ID, Issued: issued})
	})
	if err != nil {
		slog.Warn("record promo request", "user_id", userID, "day_id", ad.Day.ID, "err", err)
	}
}

func (p *Promo) issueCode(ctx context.Context, userID int64, ad *ActiveDay) (IssuedPromo, error) {
	var res IssuedPromo
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		var err error
		res, err = p.issue(ctx, q, userID, ad)
		return err
	})
	if errors.Is(err, errAlreadyIssued) {
		// The transaction was rolled back, which returned our claimed code to
		// the pool; read the code the winning request issued.
		return p.existing(ctx, userID, ad)
	}
	if err != nil {
		return IssuedPromo{}, err
	}
	return res, nil
}

func (p *Promo) issue(ctx context.Context, q *db.Queries, userID int64, ad *ActiveDay) (IssuedPromo, error) {
	row, err := q.GetPromoIssueCode(ctx, db.GetPromoIssueCodeParams{UserID: userID, CampaignDayID: ad.Day.ID})
	if err == nil {
		return IssuedPromo{Code: row.Code, Already: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IssuedPromo{}, fmt.Errorf("get issued promo: %w", err)
	}

	code, err := q.ClaimPromoCode(ctx, db.ClaimPromoCodeParams{
		TargetUserID:    pgtype.Int8{Int64: userID, Valid: true},
		TargetPartnerID: ad.Partner.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IssuedPromo{}, ErrNoPromoCodes
	}
	if err != nil {
		return IssuedPromo{}, fmt.Errorf("claim promo code: %w", err)
	}

	_, err = q.CreatePromoIssue(ctx, db.CreatePromoIssueParams{
		UserID: userID, CampaignDayID: ad.Day.ID, PromoCodeID: code.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IssuedPromo{}, errAlreadyIssued
	}
	if err != nil {
		return IssuedPromo{}, fmt.Errorf("create promo issue: %w", err)
	}

	if err := recordActiveDay(ctx, q, userID, ad.Day.ID, p.maxChances); err != nil {
		return IssuedPromo{}, err
	}
	return IssuedPromo{Code: code.Code}, nil
}

func (p *Promo) existing(ctx context.Context, userID int64, ad *ActiveDay) (IssuedPromo, error) {
	var res IssuedPromo
	err := p.tx.InTx(ctx, func(q *db.Queries) error {
		row, err := q.GetPromoIssueCode(ctx, db.GetPromoIssueCodeParams{UserID: userID, CampaignDayID: ad.Day.ID})
		if err != nil {
			return fmt.Errorf("get issued promo after conflict: %w", err)
		}
		res = IssuedPromo{Code: row.Code, Already: true}
		return nil
	})
	return res, err
}
