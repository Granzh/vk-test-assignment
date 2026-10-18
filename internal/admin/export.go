package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

// ExportFile is a generated CSV file.
type ExportFile struct {
	Name string
	Data []byte
	Rows int // data rows, header excluded
}

// Export builds the CSV for an /export request and audits it.
func (s *Service) Export(ctx context.Context, adminID int64, req ExportRequest) (*ExportFile, error) {
	var file *ExportFile
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		var (
			entityType string
			entityID   int64
			err        error
		)
		switch req.Kind {
		case ExportFinalists:
			entityType, entityID = "final_draw", req.ID
			file, err = s.exportFinalists(ctx, q, req)
		default:
			var day db.CampaignDay
			if day, err = s.resolveDay(ctx, q, req); err != nil {
				return err
			}
			entityType, entityID = "campaign_day", day.ID
			if req.Kind == ExportParticipants {
				file, err = s.exportParticipants(ctx, q, day)
			} else if req.Kind == ExportPartnerWins {
				file, err = s.exportPartnerWinners(ctx, q, day)
			} else {
				file, err = s.exportWinners(ctx, q, day)
			}
		}
		if err != nil {
			return err
		}
		return audit(ctx, q, adminID, ActionExport, entityType, entityID, map[string]any{
			"kind": string(req.Kind), "ref": req.Ref, "rows": file.Rows,
		})
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (s *Service) resolveDay(ctx context.Context, q *db.Queries, req ExportRequest) (db.CampaignDay, error) {
	var (
		day db.CampaignDay
		err error
	)
	if req.ID != 0 {
		day, err = q.GetCampaignDay(ctx, req.ID)
	} else {
		day, err = q.GetCampaignDayByDate(ctx, pgtype.Date{Time: req.Date, Valid: true})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.CampaignDay{}, inputErr(texts.AdminDayNotFound(req.Ref))
	}
	if err != nil {
		return db.CampaignDay{}, fmt.Errorf("get campaign day: %w", err)
	}
	return day, nil
}

func (s *Service) exportParticipants(ctx context.Context, q *db.Queries, day db.CampaignDay) (*ExportFile, error) {
	rows, err := q.ListDayParticipants(ctx, day.ID)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	w := newCSV("telegram_id", "username", "first_name", "registered_at", "excluded")
	for _, r := range rows {
		w.row(strconv.FormatInt(r.TelegramID, 10), r.Username.String, r.FirstName.String, s.ts(r.SubscribedCheckedAt), r.ExcludedReason.String)
	}
	return w.file("participants_" + day.Day.Time.Format(time.DateOnly) + ".csv")
}

func (s *Service) exportWinners(ctx context.Context, q *db.Queries, day db.CampaignDay) (*ExportFile, error) {
	rows, err := q.ListDayWinners(ctx, day.ID)
	if err != nil {
		return nil, fmt.Errorf("list winners: %w", err)
	}
	w := newCSV("telegram_id", "username", "first_name", "prize", "reserve", "status")
	for _, r := range rows {
		w.row(strconv.FormatInt(r.TelegramID, 10), r.Username.String, r.FirstName.String, r.Prize, strconv.FormatBool(r.IsReserve), r.Status)
	}
	return w.file("winners_" + day.Day.Time.Format(time.DateOnly) + ".csv")
}

func (s *Service) exportFinalists(ctx context.Context, q *db.Queries, req ExportRequest) (*ExportFile, error) {
	if _, err := q.GetFinalDraw(ctx, req.ID); errors.Is(err, pgx.ErrNoRows) {
		return nil, inputErr(texts.AdminFinalDrawNotFound(req.ID))
	} else if err != nil {
		return nil, fmt.Errorf("get final draw: %w", err)
	}
	rows, err := q.ListFinalDrawWinners(ctx, req.ID)
	if err != nil {
		return nil, fmt.Errorf("list finalists: %w", err)
	}
	w := newCSV("telegram_id", "username", "first_name", "tickets", "reserve", "reserve_position", "status", "forfeit_reason", "response_deadline", "contact_name", "consent_at")
	for _, r := range rows {
		w.row(strconv.FormatInt(r.TelegramID, 10), r.Username.String, r.FirstName.String, strconv.Itoa(int(r.TicketsCount)),
			strconv.FormatBool(r.IsReserve), position(r.ReservePosition), r.Status, r.ForfeitReason.String, s.ts(r.Deadline),
			contactOf(r.ContactName, r.ConsentGivenAt), s.ts(r.ConsentGivenAt))
	}
	return w.file("finalists_" + strconv.FormatInt(req.ID, 10) + ".csv")
}

// ts formats an instant in the campaign timezone for the export.
func (s *Service) ts(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.In(s.loc).Format("2006-01-02 15:04:05")
}

// csvBuilder writes a UTF-8 CSV with a BOM, so Excel opens Cyrillic correctly.
type csvBuilder struct {
	buf  bytes.Buffer
	w    *csv.Writer
	rows int
}

func newCSV(header ...string) *csvBuilder {
	b := &csvBuilder{}
	b.buf.WriteString("\xEF\xBB\xBF")
	b.w = csv.NewWriter(&b.buf)
	_ = b.w.Write(header)
	return b
}

func (b *csvBuilder) row(cells ...string) {
	for i, c := range cells {
		cells[i] = safeCell(c)
	}
	_ = b.w.Write(cells)
	b.rows++
}

func (b *csvBuilder) file(name string) (*ExportFile, error) {
	b.w.Flush()
	if err := b.w.Error(); err != nil {
		return nil, fmt.Errorf("write csv: %w", err)
	}
	return &ExportFile{Name: name, Data: b.buf.Bytes(), Rows: b.rows}, nil
}

// safeCell defuses spreadsheet formulas: user-controlled text (usernames,
// names) starting with = + - @ would otherwise be executed by Excel.
func safeCell(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r') {
		return "'" + s
	}
	return s
}

// exportPartnerWinners is the list handed to the partner: who won what, with
// Telegram contacts but without internal ids. It exists only for confirmed
// results.
func (s *Service) exportPartnerWinners(ctx context.Context, q *db.Queries, day db.CampaignDay) (*ExportFile, error) {
	date := day.Day.Time.Format(time.DateOnly)
	if day.Status != "confirmed" && day.Status != "notified" {
		return nil, inputErr(texts.AdminResultsNotConfirmed(date, day.Status))
	}
	partner, err := q.GetPartner(ctx, day.PartnerID)
	if err != nil {
		return nil, fmt.Errorf("get partner: %w", err)
	}
	rows, err := q.ListDayWinners(ctx, day.ID)
	if err != nil {
		return nil, fmt.Errorf("list winners: %w", err)
	}
	w := newCSV("date", "partner", "username", "first_name", "prize")
	for _, r := range rows {
		w.row(date, partner.Name, r.Username.String, r.FirstName.String, r.Prize)
	}
	return w.file("partner_winners_" + date + ".csv")
}

// position formats a reserve queue position; empty for winners.
func position(p pgtype.Int4) string {
	if !p.Valid {
		return ""
	}
	return strconv.Itoa(int(p.Int32))
}

// contactOf returns the collected name only when the winner consented to its
// transfer; a name without a consent is never exported.
func contactOf(name pgtype.Text, consent pgtype.Timestamptz) string {
	if !consent.Valid {
		return ""
	}
	return name.String
}
