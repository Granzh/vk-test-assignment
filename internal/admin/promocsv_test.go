package admin

import (
	"errors"
	"strings"
	"testing"
)

func TestParsePromoCSV(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		codes      []string
		invalid    int
		dups       int
		rows       int
		header     bool
		wantErrSub string
	}{
		{name: "plain list", in: "A1\nB2\nC3\n", codes: []string{"A1", "B2", "C3"}, rows: 3},
		{name: "header skipped", in: "code\nA1\nB2", codes: []string{"A1", "B2"}, rows: 2, header: true},
		{name: "russian header any case", in: "Промокод\r\nA1\r\n", codes: []string{"A1"}, rows: 1, header: true},
		{name: "bom and crlf", in: "\xEF\xBB\xBFA1\r\nB2\r\n", codes: []string{"A1", "B2"}, rows: 2},
		{name: "blank lines and spaces", in: "\n  A1  \n\n\tB2\n \n", codes: []string{"A1", "B2"}, rows: 2},
		{name: "semicolon delimiter with empty second column", in: "A1;\nB2;\n", codes: []string{"A1", "B2"}, rows: 2},
		{name: "quoted", in: "\"A1\"\n\"B2\"\n", codes: []string{"A1", "B2"}, rows: 2},
		{name: "duplicates in file", in: "A1\nB2\nA1\nA1\n", codes: []string{"A1", "B2"}, dups: 2, rows: 4},
		{name: "case differs is not a duplicate", in: "ab\nAB\n", codes: []string{"ab", "AB"}, rows: 2},
		{name: "space inside code", in: "A1\nSALE 10\nB2\n", codes: []string{"A1", "B2"}, invalid: 1, rows: 3},
		{name: "too long", in: "A1\n" + strings.Repeat("x", 65) + "\n", codes: []string{"A1"}, invalid: 1, rows: 2},
		{name: "max length is fine", in: strings.Repeat("x", 64), codes: []string{strings.Repeat("x", 64)}, rows: 1},
		{name: "control character", in: "A1\nB\x07B\n", codes: []string{"A1"}, invalid: 1, rows: 2},
		{name: "header only", in: "code\n", header: true},
		{name: "empty file", in: ""},
		{name: "header word later is a code", in: "A1\ncode\n", codes: []string{"A1", "code"}, rows: 2},
		{name: "two filled columns rejected", in: "7,A1\n7,B2\n", wantErrSub: "несколько столбцов"},
		{name: "bad utf8", in: "A1\n\xff\xfe\n", wantErrSub: "UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePromoCSV(strings.NewReader(tt.in))
			if tt.wantErrSub != "" {
				var ie *InputError
				if !errors.As(err, &ie) || !strings.Contains(ie.Msg, tt.wantErrSub) {
					t.Fatalf("err = %v, want InputError containing %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got.Codes, ",") != strings.Join(tt.codes, ",") {
				t.Errorf("codes = %q, want %q", got.Codes, tt.codes)
			}
			if len(got.Invalid) != tt.invalid || len(got.Duplicates) != tt.dups {
				t.Errorf("invalid = %d, dups = %d; want %d, %d", len(got.Invalid), len(got.Duplicates), tt.invalid, tt.dups)
			}
			if got.Rows != tt.rows || got.HeaderSkipped != tt.header {
				t.Errorf("rows = %d, header = %v; want %d, %v", got.Rows, got.HeaderSkipped, tt.rows, tt.header)
			}
		})
	}
}

func TestParsePromoCSVReportsLineNumbers(t *testing.T) {
	got, err := ParsePromoCSV(strings.NewReader("code\nA1\n\nBAD CODE\nA1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Invalid) != 1 || got.Invalid[0].Line != 4 || got.Invalid[0].Value != "BAD CODE" {
		t.Errorf("invalid = %+v, want line 4", got.Invalid)
	}
	if len(got.Duplicates) != 1 || got.Duplicates[0].Line != 5 {
		t.Errorf("duplicates = %+v, want line 5", got.Duplicates)
	}
}

func TestParsePromoCSVTooManyRows(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= MaxPromoRows; i++ {
		b.WriteString("C")
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(strings.Repeat("1", i%7))
		b.WriteString("\n")
	}
	_, err := ParsePromoCSV(strings.NewReader(b.String()))
	var ie *InputError
	if !errors.As(err, &ie) || !strings.Contains(ie.Msg, "строк") {
		t.Errorf("err = %v, want too-many-rows InputError", err)
	}
}
