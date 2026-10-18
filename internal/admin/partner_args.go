package admin

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"chat_bot/internal/texts"
)

// Field validators shared by /partner_add and /partner_edit.

func parseName(raw string) (string, error) {
	if raw == "" {
		return "", inputErr(texts.AdminEmptyField(1, "название"))
	}
	if n := utf8.RuneCountInString(raw); n > maxPartnerName {
		return "", inputErr(texts.AdminNameTooLong(n, maxPartnerName))
	}
	return raw, nil
}

func parseURL(raw string) (string, error) {
	u, ok := normalizeURL(raw)
	if !ok {
		return "", inputErr(texts.AdminBadURL(raw))
	}
	return u, nil
}

func parseChatID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	switch {
	case err != nil:
		return 0, inputErr(texts.AdminBadChatID(raw, false))
	case id >= 0:
		return 0, inputErr(texts.AdminBadChatID(raw, true))
	}
	return id, nil
}

// parseValidUntil turns a date into the end of that day in loc; "" gives nil.
func parseValidUntil(raw string, loc *time.Location) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	d, err := time.ParseInLocation(time.DateOnly, raw, time.UTC)
	if err != nil {
		return nil, inputErr(texts.AdminBadDate("Дата «действует до»", raw))
	}
	end := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc).Add(-time.Second)
	return &end, nil
}

// parsePartnerArgs parses
// "name | url | chat_id | terms [| promo_text | valid_until]".
func parsePartnerArgs(payload string, loc *time.Location) (PartnerInput, error) {
	var in PartnerInput
	if n := fieldCount(payload); n < 4 || n > 6 {
		return in, inputErr(texts.AdminFieldCount("/partner_add", n, 4, 6, texts.UsagePartnerAdd) +
			"\n\nПример: " + texts.ExamplePartnerAdd)
	}
	f := splitFields(payload)

	var err error
	if in.Name, err = parseName(f[0]); err != nil {
		return in, err
	}
	if in.URL, err = parseURL(f[1]); err != nil {
		return in, err
	}
	if in.ChatID, err = parseChatID(f[2]); err != nil {
		return in, err
	}
	in.Terms = f[3]
	if len(f) > 4 {
		in.PromoText = f[4]
	}
	if len(f) > 5 {
		if in.ValidUntil, err = parseValidUntil(f[5], loc); err != nil {
			return in, err
		}
	}
	return in, nil
}

// Editable partner fields of /partner_edit.
const (
	FieldName       = "name"
	FieldURL        = "url"
	FieldChatID     = "chat_id"
	FieldTerms      = "terms"
	FieldPromoText  = "promo_text"
	FieldValidUntil = "valid_until"
)

// PartnerEdit is a validated /partner_edit request: one field of one partner.
// Only the member matching Field is set. Clear is true when promo_text or
// valid_until is being emptied.
type PartnerEdit struct {
	ID         int64
	Field      string
	Name       string
	URL        string
	ChatID     int64
	Terms      string
	PromoText  string
	ValidUntil *time.Time
	Clear      bool
}

// parseEditArgs parses "partner_id | field | value".
func parseEditArgs(payload string, loc *time.Location) (PartnerEdit, error) {
	var e PartnerEdit
	if n := fieldCount(payload); n != 3 {
		return e, inputErr(texts.AdminFieldCount("/partner_edit", n, 3, 3, texts.UsagePartnerEdit) +
			"\n\nПример: " + texts.ExamplePartnerEdit)
	}
	f := splitFields(payload)

	var err error
	if e.ID, err = strconv.ParseInt(f[0], 10, 64); err != nil || e.ID <= 0 {
		return e, inputErr(texts.AdminBadNumber("id партнёра", f[0]))
	}
	e.Field = strings.ToLower(f[1])
	value := f[2]
	if value == "-" {
		value = ""
	}

	if value == "" && e.Field != FieldPromoText && e.Field != FieldValidUntil {
		switch e.Field {
		case FieldName, FieldURL, FieldChatID, FieldTerms:
			return e, inputErr(texts.AdminFieldNotClearable(e.Field))
		}
	}

	switch e.Field {
	case FieldName:
		e.Name, err = parseName(value)
	case FieldURL:
		e.URL, err = parseURL(value)
	case FieldChatID:
		e.ChatID, err = parseChatID(value)
	case FieldTerms:
		e.Terms = value
	case FieldPromoText:
		e.PromoText, e.Clear = value, value == ""
	case FieldValidUntil:
		e.ValidUntil, err = parseValidUntil(value, loc)
		e.Clear = value == ""
	default:
		return e, inputErr(texts.AdminBadEditField(f[1]))
	}
	if err != nil {
		return e, err
	}
	return e, nil
}
