package admin

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func wantInputErr(t *testing.T, err error, sub string) {
	t.Helper()
	var ie *InputError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v, want *InputError containing %q", err, sub)
	}
	if !strings.Contains(ie.Msg, sub) {
		t.Errorf("message %q lacks %q", ie.Msg, sub)
	}
}

func TestParsePartnerArgs(t *testing.T) {
	msk, _ := time.LoadLocation("Europe/Moscow")
	in, err := parsePartnerArgs("Кофейня | https://t.me/kofe | -1001234567890 | Скидка 20% | на Арбате | 2026-09-30", msk)
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "Кофейня" || in.URL != "https://t.me/kofe" || in.ChatID != -1001234567890 ||
		in.Terms != "Скидка 20%" || in.PromoText != "на Арбате" {
		t.Errorf("in = %+v", in)
	}
	// valid through the end of Sept 30 Moscow time
	want := time.Date(2026, 9, 30, 23, 59, 59, 0, msk)
	if in.ValidUntil == nil || !in.ValidUntil.Equal(want) {
		t.Errorf("ValidUntil = %v, want %v", in.ValidUntil, want)
	}
}

func TestParsePartnerArgsShortAndAtURL(t *testing.T) {
	in, err := parsePartnerArgs("P | @kofe | -100500 | условия", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if in.URL != "https://t.me/kofe" || in.ValidUntil != nil || in.PromoText != "" {
		t.Errorf("in = %+v", in)
	}
}

func TestParsePartnerArgsErrors(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", "получено полей: 0"},
		{"too few", "P | https://t.me/x | -1", "получено полей: 3"},
		{"too many", "a|b|c|d|e|f|g", "получено полей: 7"},
		{"empty name", " | https://t.me/x | -100 | t", "Поле 1"},
		{"long name", strings.Repeat("я", 101) + " | https://t.me/x | -100 | t", "101 символов"},
		{"bad url", "P | not a url | -100 | t", "Ссылка «not a url»"},
		{"ftp url", "P | ftp://x.y/z | -100 | t", "Ссылка"},
		{"bare at", "P | @ | -100 | t", "Ссылка"},
		{"chat id text", "P | https://t.me/x | @channel | t", "не число"},
		{"chat id positive", "P | https://t.me/x | 12345 | t", "отрицательный"},
		{"chat id zero", "P | https://t.me/x | 0 | t", "отрицательный"},
		{"bad valid date", "P | https://t.me/x | -100 | t | где | 30.09.2026", "ГГГГ-ММ-ДД"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePartnerArgs(tt.in, time.UTC)
			wantInputErr(t, err, tt.want)
		})
	}
}

func TestParseDayArgs(t *testing.T) {
	in, err := parseDayArgs("2026-09-05 | 3 | 2 | Сертификат | и подписка")
	if err == nil {
		t.Fatalf("5 fields must be rejected, got %+v", in)
	}
	in, err = parseDayArgs("2026-09-05 | 3 | 2 | Сертификат и подписка")
	if err != nil {
		t.Fatal(err)
	}
	if !in.Day.Equal(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)) || in.PartnerID != 3 || in.Winners != 2 || in.Prizes != "Сертификат и подписка" {
		t.Errorf("in = %+v", in)
	}
}

func TestParseDayArgsErrors(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", "получено полей: 0"},
		{"bad date", "05.09.2026 | 3 | 2 | p", "ГГГГ-ММ-ДД"},
		{"impossible date", "2026-02-30 | 3 | 2 | p", "ГГГГ-ММ-ДД"},
		{"partner text", "2026-09-05 | abc | 2 | p", "id партнёра «abc»"},
		{"partner zero", "2026-09-05 | 0 | 2 | p", "id партнёра"},
		{"winners zero", "2026-09-05 | 3 | 0 | p", "Число победителей"},
		{"winners negative", "2026-09-05 | 3 | -1 | p", "Число победителей"},
		{"winners huge", "2026-09-05 | 3 | 99999999999 | p", "Число победителей"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDayArgs(tt.in)
			wantInputErr(t, err, tt.want)
		})
	}
}

func TestParseExportArgs(t *testing.T) {
	tests := []struct {
		in   string
		want ExportRequest
	}{
		{"participants 5", ExportRequest{Kind: ExportParticipants, ID: 5, Ref: "5"}},
		{"WINNERS 12", ExportRequest{Kind: ExportWinners, ID: 12, Ref: "12"}},
		{"finalists 1", ExportRequest{Kind: ExportFinalists, ID: 1, Ref: "1"}},
		{"participants 2026-09-05", ExportRequest{Kind: ExportParticipants, Date: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Ref: "2026-09-05"}},
	}
	for _, tt := range tests {
		got, err := parseExportArgs(tt.in)
		if err != nil {
			t.Errorf("%q: %v", tt.in, err)
			continue
		}
		if got.Kind != tt.want.Kind || got.ID != tt.want.ID || !got.Date.Equal(tt.want.Date) || got.Ref != tt.want.Ref {
			t.Errorf("%q: got %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseExportArgsErrors(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "получено полей: 0"},
		{"participants", "получено полей: 1"},
		{"participants 1 2", "получено полей: 3"},
		{"users 5", "Неизвестный тип выгрузки «users»"},
		{"participants abc", "id дня или дата"},
		{"participants 0", "id дня или дата"},
		{"finalists 2026-09-05", "id финального розыгрыша"},
	}
	for _, tt := range tests {
		_, err := parseExportArgs(tt.in)
		wantInputErr(t, err, tt.want)
	}
}

func TestParseUploadCaption(t *testing.T) {
	tests := []struct {
		in string
		id int64
		ok bool
	}{
		{"/promo_upload 3", 3, true},
		{"  /promo_upload   42 ", 42, true},
		{"/promo_upload@my_bot 7", 7, true},
		{"/promo_upload", 0, false},
		{"/promo_upload abc", 0, false},
		{"/promo_upload 0", 0, false},
		{"/promo_upload 3 4", 0, false},
		{"/partner_add 3", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		id, ok := parseUploadCaption(tt.in)
		if id != tt.id || ok != tt.ok {
			t.Errorf("%q: got (%d, %v), want (%d, %v)", tt.in, id, ok, tt.id, tt.ok)
		}
	}
}

func TestSafeCell(t *testing.T) {
	for in, want := range map[string]string{
		"plain": "plain", "": "", "=SUM(A1)": "'=SUM(A1)", "+1": "'+1", "-1": "'-1", "@x": "'@x", "a=b": "a=b",
	} {
		if got := safeCell(in); got != want {
			t.Errorf("safeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitMessage(t *testing.T) {
	text := strings.Repeat("строка\n", 10)
	parts := splitMessage(text, 20)
	if strings.Join(parts, "") != text {
		t.Error("split lost or reordered text")
	}
	for _, p := range parts {
		if len([]rune(p)) > 20 {
			t.Errorf("part of %d runes exceeds the limit", len([]rune(p)))
		}
	}
	if got := splitMessage("short", 20); len(got) != 1 {
		t.Errorf("short text split into %d parts", len(got))
	}
}

func TestParseEditArgs(t *testing.T) {
	msk, _ := time.LoadLocation("Europe/Moscow")
	tests := []struct {
		in    string
		check func(PartnerEdit) bool
	}{
		{"3 | name | Новое имя", func(e PartnerEdit) bool { return e.ID == 3 && e.Field == FieldName && e.Name == "Новое имя" }},
		{"3 | URL | @chan", func(e PartnerEdit) bool { return e.Field == FieldURL && e.URL == "https://t.me/chan" }},
		{"3 | chat_id | -100500", func(e PartnerEdit) bool { return e.Field == FieldChatID && e.ChatID == -100500 }},
		{"3 | terms | 20% скидка", func(e PartnerEdit) bool { return e.Terms == "20% скидка" }},
		{"3 | promo_text | на сайте", func(e PartnerEdit) bool { return e.PromoText == "на сайте" && !e.Clear }},
		{"3 | promo_text | -", func(e PartnerEdit) bool { return e.PromoText == "" && e.Clear }},
		{"3 | valid_until | 2026-09-30", func(e PartnerEdit) bool {
			return e.ValidUntil != nil && e.ValidUntil.Equal(time.Date(2026, 9, 30, 23, 59, 59, 0, msk))
		}},
		{"3 | valid_until | ", func(e PartnerEdit) bool { return e.ValidUntil == nil && e.Clear }},
	}
	for _, tt := range tests {
		e, err := parseEditArgs(tt.in, msk)
		if err != nil || !tt.check(e) {
			t.Errorf("%q: got %+v, err %v", tt.in, e, err)
		}
	}
}

func TestParseEditArgsErrors(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "получено полей: 0"},
		{"3 | name", "получено полей: 2"},
		{"abc | name | x", "id партнёра «abc»"},
		{"0 | name | x", "id партнёра"},
		{"3 | color | red", "Неизвестное поле «color»"},
		{"3 | chat_id | 77", "отрицательный"},
		{"3 | chat_id | @chan", "не число"},
		{"3 | chat_id | -", "нельзя очистить"},
		{"3 | name | ", "нельзя очистить"},
		{"3 | url | ", "нельзя очистить"},
		{"3 | terms | -", "нельзя очистить"},
		{"3 | url | not a url", "Ссылка"},
		{"3 | valid_until | 30.09.2026", "ГГГГ-ММ-ДД"},
		{"3 | name | " + strings.Repeat("я", 101), "101 символов"},
	}
	for _, tt := range tests {
		_, err := parseEditArgs(tt.in, time.UTC)
		wantInputErr(t, err, tt.want)
	}
}
