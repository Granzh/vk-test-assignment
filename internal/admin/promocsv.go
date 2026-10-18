package admin

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"chat_bot/internal/texts"
)

const (
	// MaxPromoCodeLen is the longest accepted promo code.
	MaxPromoCodeLen = 64
	// MaxPromoRows is the most rows accepted in one file.
	MaxPromoRows = 100_000
)

var headerNames = map[string]bool{
	"code": true, "promo": true, "promocode": true, "promo_code": true,
	"промокод": true, "код": true,
}

// RowIssue is a rejected CSV row.
type RowIssue struct {
	Line   int
	Value  string
	Reason string
}

// PromoParse is the result of parsing an uploaded promo code file.
type PromoParse struct {
	Codes         []string   // valid, unique within the file, in file order
	Invalid       []RowIssue // rows that are not valid codes
	Duplicates    []RowIssue // repeats of a code seen earlier in the file
	Rows          int        // non-empty rows examined (header excluded)
	HeaderSkipped bool
}

// ParsePromoCSV reads a single-column CSV of promo codes. It tolerates a UTF-8
// BOM, CRLF, a header row, blank lines, surrounding spaces and the delimiters
// comma, semicolon and tab. A file with several filled columns is rejected as
// a whole, because a second column most likely means a different layout
// (e.g. partner id + code) that must not be guessed.
func ParsePromoCSV(r io.Reader) (*PromoParse, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(data) {
		return nil, &InputError{Msg: texts.AdminPromoCSVSyntax(errors.New("файл не в кодировке UTF-8"))}
	}

	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = detectDelimiter(data)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true

	res := &PromoParse{}
	seen := map[string]bool{}
	first := true
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &InputError{Msg: texts.AdminPromoCSVSyntax(err)}
		}
		line, _ := cr.FieldPos(0)

		for _, extra := range rec[1:] {
			if strings.TrimSpace(extra) != "" {
				return nil, &InputError{Msg: texts.AdminPromoMultiColumn(line)}
			}
		}
		code := strings.TrimSpace(rec[0])
		if code == "" {
			continue // blank line or a lone delimiter
		}
		if first {
			first = false
			if headerNames[strings.ToLower(code)] {
				res.HeaderSkipped = true
				continue
			}
		}

		res.Rows++
		if res.Rows > MaxPromoRows {
			return nil, &InputError{Msg: texts.AdminPromoTooManyRows(MaxPromoRows)}
		}
		if reason := invalidReason(code); reason != "" {
			res.Invalid = append(res.Invalid, RowIssue{Line: line, Value: code, Reason: reason})
			continue
		}
		if seen[code] {
			res.Duplicates = append(res.Duplicates, RowIssue{Line: line, Value: code, Reason: texts.PromoReasonDuplicate})
			continue
		}
		seen[code] = true
		res.Codes = append(res.Codes, code)
	}
	return res, nil
}

// invalidReason returns why code is not a valid promo code, or "".
func invalidReason(code string) string {
	if n := utf8.RuneCountInString(code); n > MaxPromoCodeLen {
		return texts.PromoReasonTooLong(n, MaxPromoCodeLen)
	}
	for _, r := range code {
		switch {
		case unicode.IsSpace(r):
			return texts.PromoReasonSpaces
		case !unicode.IsPrint(r):
			return texts.PromoReasonBadChars
		}
	}
	return ""
}

// detectDelimiter picks the most frequent of , ; tab on the first non-empty line.
func detectDelimiter(data []byte) rune {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		best, bestN := ',', 0
		for _, d := range []rune{',', ';', '\t'} {
			if n := bytes.Count(line, []byte(string(d))); n > bestN {
				best, bestN = d, n
			}
		}
		return best
	}
	return ','
}
