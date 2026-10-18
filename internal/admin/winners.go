package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

// Audit actions of this file.
const (
	ActionWinners       = "winners"
	ActionPurgeContacts = "contacts_purge_request"
)

// ContactPurger removes collected personal data (implemented by *service.FinalPrize).
type ContactPurger interface {
	PurgeContacts(ctx context.Context, adminID int64, confirm bool) (service.PurgeResult, error)
}

// WithContactPurger sets the service behind /purge_contacts.
func (s *Service) WithContactPurger(p ContactPurger) *Service {
	s.purger = p
	return s
}

// WinnersRequest is a parsed /winners command.
type WinnersRequest struct {
	Final bool
	Date  time.Time
	CSV   bool
}

func parseWinnersArgs(payload string) (WinnersRequest, error) {
	fields := strings.Fields(payload)
	if len(fields) < 1 || len(fields) > 2 {
		return WinnersRequest{}, inputErr(texts.AdminFieldCount("/winners", len(fields), 1, 2, texts.UsageWinners))
	}
	var req WinnersRequest
	if len(fields) == 2 {
		if !strings.EqualFold(fields[1], "csv") {
			return req, inputErr(fmt.Sprintf("Неизвестный параметр «%s». Допустим только csv.\nФормат: %s", fields[1], texts.UsageWinners))
		}
		req.CSV = true
	}
	if strings.EqualFold(fields[0], "final") {
		req.Final = true
		return req, nil
	}
	d, err := time.ParseInLocation(time.DateOnly, fields[0], time.UTC)
	if err != nil {
		return req, inputErr(texts.AdminBadDate("Розыгрыш", fields[0]) + " Или укажите final для финального розыгрыша.")
	}
	req.Date = d
	return req, nil
}

// Winners returns the statuses of the winners of a day or of the final draw.
// Names collected from winners are never part of this view.
func (s *Service) Winners(ctx context.Context, adminID int64, req WinnersRequest) (texts.AdminWinnersView, error) {
	var v texts.AdminWinnersView
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		var entityType string
		var entityID int64
		if req.Final {
			fd, err := q.GetFinalDrawOnly(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				return inputErr(texts.AdminNoFinalDraw)
			}
			if err != nil {
				return fmt.Errorf("get final draw: %w", err)
			}
			rows, err := q.ListFinalDrawWinners(ctx, fd.ID)
			if err != nil {
				return fmt.Errorf("list final winners: %w", err)
			}
			v = texts.AdminWinnersView{Title: fmt.Sprintf("🏆 Финальный розыгрыш (id %d)", fd.ID), Final: true, ID: fd.ID}
			for _, r := range rows {
				role, status := texts.FinalWinnerStatus(r.Status, r.DialogStep.String, r.ForfeitReason.String, r.IsReserve,
					r.PromotedAt.Valid, r.ContactPurgedAt.Valid, int(r.ReservePosition.Int32), r.Deadline.Time, r.ConsentGivenAt.Time, s.loc)
				v.Lines = append(v.Lines, texts.AdminWinnerLine{
					TelegramID: r.TelegramID, Username: r.Username.String, FirstName: r.FirstName.String, Role: role, Status: status,
				})
			}
			entityType, entityID = "final_draw", fd.ID
		} else {
			day, err := q.GetCampaignDayByDate(ctx, pgtype.Date{Time: req.Date, Valid: true})
			if errors.Is(err, pgx.ErrNoRows) {
				return inputErr(texts.AdminDrawNoDay(req.Date))
			}
			if err != nil {
				return fmt.Errorf("get campaign day: %w", err)
			}
			rows, err := q.ListDayWinners(ctx, day.ID)
			if err != nil {
				return fmt.Errorf("list winners: %w", err)
			}
			v = texts.AdminWinnersView{Title: fmt.Sprintf("🎲 Победители дня %s", req.Date.Format(time.DateOnly)), ID: day.ID}
			for _, r := range rows {
				v.Lines = append(v.Lines, texts.AdminWinnerLine{
					TelegramID: r.TelegramID, Username: r.Username.String, FirstName: r.FirstName.String,
					Role: "победитель", Status: texts.DailyWinnerStatus(r.Status),
				})
			}
			entityType, entityID = "campaign_day", day.ID
		}
		return audit(ctx, q, adminID, ActionWinners, entityType, entityID, map[string]any{"csv": req.CSV, "rows": len(v.Lines)})
	})
	return v, err
}

// PurgeContacts removes the collected names after the campaign (or only counts them).
func (s *Service) PurgeContacts(ctx context.Context, adminID int64, confirm bool) (service.PurgeResult, error) {
	res, err := s.purger.PurgeContacts(ctx, adminID, confirm)
	if errors.Is(err, service.ErrPurgeTooEarly) {
		return res, inputErr(texts.AdminPurgeTooEarly(s.end))
	}
	return res, err
}

func parsePurgeArgs(payload string) (confirm bool, err error) {
	fields := strings.Fields(payload)
	switch {
	case len(fields) == 0:
		return false, nil
	case len(fields) == 1 && strings.EqualFold(fields[0], "confirm"):
		return true, nil
	}
	return false, inputErr(texts.AdminFieldCount("/purge_contacts", len(fields), 0, 1, texts.UsagePurgeContacts) +
		"\nДопустим только параметр confirm.")
}
