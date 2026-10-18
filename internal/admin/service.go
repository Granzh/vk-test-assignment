package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

// TxRunner runs fn in a database transaction (implemented by storage.Store).
type TxRunner interface {
	InTx(ctx context.Context, fn func(q *db.Queries) error) error
}

// Service implements the admin use cases. Every method records the action in
// audit_log; changes are audited in the same transaction as the change.
type Service struct {
	tx         TxRunner
	draw       Drawer
	final      FinalDrawer
	purger     ContactPurger
	bcast      Broadcaster
	stats      StatsSource
	start, end time.Time // first and last campaign day
	loc        *time.Location
}

// NewService builds the service. start and end are the first and last
// campaign days as dates (midnight UTC).
func NewService(tx TxRunner, start, end time.Time, loc *time.Location) *Service {
	return &Service{tx: tx, start: start, end: end, loc: loc}
}

// Audit actions.
const (
	ActionPartnerAdd  = "partner_add"
	ActionPartnerList = "partner_list"
	ActionDaySet      = "day_set"
	ActionPromoUpload = "promo_upload"
	ActionPromoStats  = "promo_stats"
	ActionExport      = "export"
	ActionMenu        = "menu"
)

func audit(ctx context.Context, q *db.Queries, adminID int64, action, entityType string, entityID int64, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal audit details: %w", err)
	}
	if err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{
		AdminTelegramID: adminID,
		Action:          action,
		EntityType:      entityType,
		EntityID:        pgtype.Int8{Int64: entityID, Valid: entityID != 0},
		Details:         raw,
	}); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}

// Audit records an action that changes nothing (menu, listings).
func (s *Service) Audit(ctx context.Context, adminID int64, action string, details map[string]any) error {
	return s.tx.InTx(ctx, func(q *db.Queries) error {
		return audit(ctx, q, adminID, action, "admin", 0, details)
	})
}

// AddPartner creates a partner.
func (s *Service) AddPartner(ctx context.Context, adminID int64, in PartnerInput) (db.Partner, error) {
	var p db.Partner
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		existing, err := q.ListPartners(ctx)
		if err != nil {
			return fmt.Errorf("list partners: %w", err)
		}
		for _, e := range existing {
			if strings.EqualFold(e.Name, in.Name) {
				return inputErr(texts.AdminDuplicatePartner(e.Name, e.ID))
			}
		}
		arg := db.CreatePartnerParams{
			Name:       in.Name,
			Url:        in.URL,
			ChatID:     pgtype.Int8{Int64: in.ChatID, Valid: true},
			PromoText:  in.PromoText,
			PromoTerms: in.Terms,
		}
		if in.ValidUntil != nil {
			arg.PromoValidUntil = pgtype.Timestamptz{Time: in.ValidUntil.UTC(), Valid: true}
		}
		if p, err = q.CreatePartner(ctx, arg); err != nil {
			return fmt.Errorf("create partner: %w", err)
		}
		return audit(ctx, q, adminID, ActionPartnerAdd, "partner", p.ID, map[string]any{
			"name": p.Name, "url": p.Url, "chat_id": in.ChatID, "terms": in.Terms,
			"promo_text": in.PromoText, "valid_until": in.ValidUntil,
		})
	})
	return p, err
}

// ListPartners returns all partners.
func (s *Service) ListPartners(ctx context.Context, adminID int64) ([]db.Partner, error) {
	var ps []db.Partner
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		var err error
		if ps, err = q.ListPartners(ctx); err != nil {
			return fmt.Errorf("list partners: %w", err)
		}
		return audit(ctx, q, adminID, ActionPartnerList, "partner", 0, map[string]any{"count": len(ps)})
	})
	return ps, err
}

// DaySetResult is the outcome of SetDay.
type DaySetResult struct {
	Day     db.CampaignDay
	Partner db.Partner
	Created bool
}

// SetDay assigns a partner, winner count and prizes to a campaign date. It
// creates the day or updates it while it is still pending; once the day has
// been drawn nothing can change, and a day with user activity keeps its partner.
func (s *Service) SetDay(ctx context.Context, adminID int64, in DayInput) (DaySetResult, error) {
	if in.Day.Before(s.start) || in.Day.After(s.end) {
		return DaySetResult{}, inputErr(texts.AdminDayOutsideCampaign(in.Day, s.start, s.end))
	}
	var res DaySetResult
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		partner, err := q.GetPartner(ctx, in.PartnerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return inputErr(texts.AdminPartnerNotFound(in.PartnerID))
		}
		if err != nil {
			return fmt.Errorf("get partner: %w", err)
		}
		res.Partner = partner

		date := pgtype.Date{Time: in.Day, Valid: true}
		cur, err := q.GetCampaignDayByDate(ctx, date)
		prevPartner := int64(0)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Created = true
			res.Day, err = q.CreateCampaignDay(ctx, db.CreateCampaignDayParams{
				Day: date, PartnerID: partner.ID, WinnersCount: in.Winners, PrizesDescription: in.Prizes,
			})
			if err != nil {
				return fmt.Errorf("create campaign day: %w", err)
			}
		case err != nil:
			return fmt.Errorf("get campaign day: %w", err)
		default:
			prevPartner = cur.PartnerID
			if cur.Status != "pending" {
				return inputErr(texts.AdminDayLocked(in.Day, cur.Status))
			}
			if cur.PartnerID != partner.ID {
				entries, err := q.CountDailyEntries(ctx, cur.ID)
				if err != nil {
					return fmt.Errorf("count entries: %w", err)
				}
				issues, err := q.CountPromoIssuesByDay(ctx, cur.ID)
				if err != nil {
					return fmt.Errorf("count promo issues: %w", err)
				}
				if entries > 0 || issues > 0 {
					return inputErr(texts.AdminDayPartnerInUse(in.Day, entries, issues))
				}
			}
			res.Day, err = q.UpdateCampaignDay(ctx, db.UpdateCampaignDayParams{
				ID: cur.ID, NewPartnerID: partner.ID, NewWinnersCount: in.Winners, NewPrizes: in.Prizes,
			})
			if err != nil {
				return fmt.Errorf("update campaign day: %w", err)
			}
		}
		return audit(ctx, q, adminID, ActionDaySet, "campaign_day", res.Day.ID, map[string]any{
			"day": in.Day.Format(time.DateOnly), "partner_id": partner.ID, "previous_partner_id": prevPartner,
			"winners": in.Winners, "prizes": in.Prizes, "created": res.Created,
		})
	})
	return res, err
}

// PromoReport is the outcome of UploadPromo.
type PromoReport struct {
	Partner       db.Partner
	Rows          int
	Loaded        int
	AlreadyInDB   int
	DupInFile     int
	Invalid       []RowIssue
	HeaderSkipped bool
}

// UploadPromo stores the valid codes of a parsed file for a partner. Codes that
// already exist for the partner are skipped and counted.
func (s *Service) UploadPromo(ctx context.Context, adminID, partnerID int64, parsed *PromoParse) (PromoReport, error) {
	rep := PromoReport{
		Rows: parsed.Rows, DupInFile: len(parsed.Duplicates), Invalid: parsed.Invalid, HeaderSkipped: parsed.HeaderSkipped,
	}
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		partner, err := q.GetPartner(ctx, partnerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return inputErr(texts.AdminPartnerNotFound(partnerID))
		}
		if err != nil {
			return fmt.Errorf("get partner: %w", err)
		}
		rep.Partner = partner

		if len(parsed.Codes) > 0 {
			n, err := q.AddPromoCodes(ctx, db.AddPromoCodesParams{Pid: partnerID, Codes: parsed.Codes})
			if err != nil {
				return fmt.Errorf("add promo codes: %w", err)
			}
			rep.Loaded = int(n)
			rep.AlreadyInDB = len(parsed.Codes) - rep.Loaded
		}
		return audit(ctx, q, adminID, ActionPromoUpload, "partner", partnerID, map[string]any{
			"rows": rep.Rows, "loaded": rep.Loaded, "already_in_db": rep.AlreadyInDB,
			"duplicates_in_file": rep.DupInFile, "invalid": len(rep.Invalid),
		})
	})
	return rep, err
}

// PromoStats returns promo code counts per partner.
func (s *Service) PromoStats(ctx context.Context, adminID int64) ([]db.ListPromoStatsRow, error) {
	var rows []db.ListPromoStatsRow
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		var err error
		if rows, err = q.ListPromoStats(ctx); err != nil {
			return fmt.Errorf("list promo stats: %w", err)
		}
		return audit(ctx, q, adminID, ActionPromoStats, "partner", 0, map[string]any{"partners": len(rows)})
	})
	return rows, err
}

// ActionPartnerEdit is the audit action of EditPartner.
const ActionPartnerEdit = "partner_edit"

// PartnerEditResult is the outcome of EditPartner.
type PartnerEditResult struct {
	Partner  db.Partner // after the change
	Old, New string     // the field's value before and after, for display
	Changed  bool
}

// EditPartner changes one field of a partner. An unchanged value is reported
// as such and is not audited.
func (s *Service) EditPartner(ctx context.Context, adminID int64, e PartnerEdit) (PartnerEditResult, error) {
	var res PartnerEditResult
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.GetPartner(ctx, e.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return inputErr(texts.AdminPartnerNotFound(e.ID))
		}
		if err != nil {
			return fmt.Errorf("get partner: %w", err)
		}

		arg := db.UpdatePartnerParams{
			ID: cur.ID, NewName: cur.Name, NewUrl: cur.Url, NewChatID: cur.ChatID,
			NewPromoText: cur.PromoText, NewPromoTerms: cur.PromoTerms, NewValidUntil: cur.PromoValidUntil,
		}
		switch e.Field {
		case FieldName:
			all, err := q.ListPartners(ctx)
			if err != nil {
				return fmt.Errorf("list partners: %w", err)
			}
			for _, p := range all {
				if p.ID != cur.ID && strings.EqualFold(p.Name, e.Name) {
					return inputErr(texts.AdminDuplicatePartner(p.Name, p.ID))
				}
			}
			res.Old, res.New, arg.NewName = cur.Name, e.Name, e.Name
		case FieldURL:
			res.Old, res.New, arg.NewUrl = cur.Url, e.URL, e.URL
		case FieldChatID:
			if cur.ChatID.Valid {
				res.Old = strconv.FormatInt(cur.ChatID.Int64, 10)
			}
			res.New = strconv.FormatInt(e.ChatID, 10)
			arg.NewChatID = pgtype.Int8{Int64: e.ChatID, Valid: true}
		case FieldTerms:
			res.Old, res.New, arg.NewPromoTerms = cur.PromoTerms, e.Terms, e.Terms
		case FieldPromoText:
			res.Old, res.New, arg.NewPromoText = cur.PromoText, e.PromoText, e.PromoText
		case FieldValidUntil:
			if cur.PromoValidUntil.Valid {
				res.Old = cur.PromoValidUntil.Time.In(s.loc).Format(time.DateOnly)
			}
			arg.NewValidUntil = pgtype.Timestamptz{}
			if e.ValidUntil != nil {
				res.New = e.ValidUntil.In(s.loc).Format(time.DateOnly)
				arg.NewValidUntil = pgtype.Timestamptz{Time: e.ValidUntil.UTC(), Valid: true}
			}
		default:
			return fmt.Errorf("unsupported partner field %q", e.Field)
		}

		res.Partner = cur
		if res.Old == res.New {
			return nil
		}
		if res.Partner, err = q.UpdatePartner(ctx, arg); err != nil {
			return fmt.Errorf("update partner: %w", err)
		}
		res.Changed = true
		return audit(ctx, q, adminID, ActionPartnerEdit, "partner", cur.ID, map[string]any{
			"field": e.Field, "old": res.Old, "new": res.New,
		})
	})
	return res, err
}
