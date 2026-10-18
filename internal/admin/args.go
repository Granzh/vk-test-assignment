package admin

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"chat_bot/internal/texts"
)

const maxPartnerName = 100

func inputErr(msg string) error { return &InputError{Msg: msg} }

// splitFields splits a command payload on "|" and trims each field.
func splitFields(payload string) []string {
	parts := strings.Split(payload, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// fieldCount is the number of |-separated fields, 0 for an empty payload.
func fieldCount(payload string) int {
	if strings.TrimSpace(payload) == "" {
		return 0
	}
	return len(strings.Split(payload, "|"))
}

// PartnerInput is a validated /partner_add request.
type PartnerInput struct {
	Name       string
	URL        string
	ChatID     int64
	Terms      string
	PromoText  string // where the promo code works ("на ...")
	ValidUntil *time.Time
}

// normalizeURL accepts https://..., http://... and @channel (-> https://t.me/channel).
func normalizeURL(raw string) (string, bool) {
	if name, ok := strings.CutPrefix(raw, "@"); ok {
		if name == "" || strings.ContainsAny(name, " /") {
			return "", false
		}
		return "https://t.me/" + name, true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(raw, " ") {
		return "", false
	}
	return raw, true
}

// DayInput is a validated /day_set request.
type DayInput struct {
	Day       time.Time // campaign date, midnight UTC
	PartnerID int64
	Winners   int32
	Prizes    string
}

// parseDayArgs parses "date | partner_id | winners | prizes". Whether the date
// is inside the campaign is checked by the service, which knows the period.
func parseDayArgs(payload string) (DayInput, error) {
	var in DayInput
	if n := fieldCount(payload); n != 4 {
		return in, inputErr(texts.AdminFieldCount("/day_set", n, 4, 4, texts.UsageDaySet) +
			"\n\nПример: " + texts.ExampleDaySet)
	}
	f := splitFields(payload)

	d, err := time.ParseInLocation(time.DateOnly, f[0], time.UTC)
	if err != nil {
		return in, inputErr(texts.AdminBadDate("Дата", f[0]))
	}
	in.Day = d

	if in.PartnerID, err = strconv.ParseInt(f[1], 10, 64); err != nil || in.PartnerID <= 0 {
		return in, inputErr(texts.AdminBadNumber("id партнёра", f[1]))
	}
	w, err := strconv.ParseInt(f[2], 10, 32)
	if err != nil || w <= 0 {
		return in, inputErr(texts.AdminBadNumber("Число победителей", f[2]))
	}
	in.Winners = int32(w)
	in.Prizes = f[3]
	return in, nil
}

// ExportKind is a type of /export.
type ExportKind string

// Export types.
const (
	ExportParticipants ExportKind = "participants"
	ExportWinners      ExportKind = "winners"
	ExportPartnerWins  ExportKind = "partner_winners"
	ExportFinalists    ExportKind = "finalists"
)

// ExportRequest is a parsed /export command. Day exports take a day id or a
// date; the finalists export takes a final draw id.
type ExportRequest struct {
	Kind ExportKind
	ID   int64     // day id or final draw id; 0 when Date is set
	Date time.Time // campaign date for day exports, zero when ID is set
	Ref  string    // the argument as typed, for messages
}

func parseExportArgs(payload string) (ExportRequest, error) {
	fields := strings.Fields(payload)
	if len(fields) != 2 {
		return ExportRequest{}, inputErr(texts.AdminFieldCount("/export", len(fields), 2, 2, texts.UsageExport) +
			"\n\nПример: " + texts.ExampleExport)
	}
	req := ExportRequest{Kind: ExportKind(strings.ToLower(fields[0])), Ref: fields[1]}
	switch req.Kind {
	case ExportParticipants, ExportWinners, ExportPartnerWins, ExportFinalists:
	default:
		return ExportRequest{}, inputErr(texts.AdminBadExportKind(fields[0]))
	}

	if id, err := strconv.ParseInt(fields[1], 10, 64); err == nil && id > 0 {
		req.ID = id
		return req, nil
	}
	if req.Kind != ExportFinalists {
		if d, err := time.ParseInLocation(time.DateOnly, fields[1], time.UTC); err == nil {
			req.Date = d
			return req, nil
		}
	}
	field := "id дня или дата"
	if req.Kind == ExportFinalists {
		field = "id финального розыгрыша"
	}
	return ExportRequest{}, inputErr(texts.AdminBadNumber(field, fields[1]))
}

// parseUploadCaption extracts the partner id from "/promo_upload <id>".
func parseUploadCaption(caption string) (int64, bool) {
	fields := strings.Fields(caption)
	if len(fields) != 2 {
		return 0, false
	}
	cmd, _, _ := strings.Cut(fields[0], "@") // /promo_upload@botname
	if cmd != "/promo_upload" {
		return 0, false
	}
	id, err := strconv.ParseInt(fields[1], 10, 64)
	return id, err == nil && id > 0
}
