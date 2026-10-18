package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"chat_bot/internal/admin"
	"chat_bot/internal/storage"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/storage/storagetest"
)

const adminTG = int64(10)

type env struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	store *storage.Store
	svc   *admin.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storagetest.New(t)
	store := storage.NewStore(pool)
	d := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	return &env{
		ctx: context.Background(), pool: pool, store: store,
		svc: admin.NewService(store, d(1), d(30), time.UTC),
	}
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) partner(t *testing.T, name string) db.Partner {
	t.Helper()
	p, err := e.svc.AddPartner(e.ctx, adminTG, admin.PartnerInput{Name: name, URL: "https://t.me/x", ChatID: -100})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func day(n int) time.Time { return time.Date(2026, 9, n, 0, 0, 0, 0, time.UTC) }

func wantInput(t *testing.T, err error, sub string) {
	t.Helper()
	var ie *admin.InputError
	if !errors.As(err, &ie) || !strings.Contains(ie.Msg, sub) {
		t.Fatalf("err = %v, want InputError containing %q", err, sub)
	}
}

// lastAudit returns the newest audit row.
func (e *env) lastAudit(t *testing.T) (db.AuditLog, map[string]any) {
	t.Helper()
	rows, err := e.store.ListAuditLog(e.ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %v, err = %v", rows, err)
	}
	var details map[string]any
	if err := json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	return rows[0], details
}

func TestAddPartner(t *testing.T) {
	e := newEnv(t)
	until := time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)
	p, err := e.svc.AddPartner(e.ctx, adminTG, admin.PartnerInput{
		Name: "Кофейня", URL: "https://t.me/kofe", ChatID: -1001, Terms: "условия", PromoText: "на Арбате", ValidUntil: &until,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.store.GetPartner(e.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Кофейня" || got.Url != "https://t.me/kofe" || got.ChatID.Int64 != -1001 || !got.ChatID.Valid ||
		got.PromoTerms != "условия" || got.PromoText != "на Арбате" || !got.PromoValidUntil.Time.Equal(until) {
		t.Errorf("stored partner = %+v", got)
	}
	a, details := e.lastAudit(t)
	if a.Action != "partner_add" || a.AdminTelegramID != adminTG || a.EntityID.Int64 != p.ID || details["name"] != "Кофейня" {
		t.Errorf("audit = %+v %v", a, details)
	}
}

func TestAddPartnerDuplicateName(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "Кофейня")
	_, err := e.svc.AddPartner(e.ctx, adminTG, admin.PartnerInput{Name: "кофейня", URL: "https://t.me/y", ChatID: -5})
	wantInput(t, err, fmt.Sprintf("уже существует (id %d)", p.ID))
	if n := e.count(t, `SELECT count(*) FROM partners`); n != 1 {
		t.Errorf("partners = %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log`); n != 1 {
		t.Errorf("audit rows = %d, want 1 (failed attempts change nothing)", n)
	}
}

func TestSetDayCreateUpdateAndAudit(t *testing.T) {
	e := newEnv(t)
	p1, p2 := e.partner(t, "One"), e.partner(t, "Two")

	res, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p1.ID, Winners: 2, Prizes: "A"})
	if err != nil || !res.Created || res.Partner.ID != p1.ID {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	// no activity yet: partner, winners and prizes can all change
	res, err = e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p2.ID, Winners: 3, Prizes: "B"})
	if err != nil || res.Created {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	cd, err := e.store.GetCampaignDayByDate(e.ctx, pgtype.Date{Time: day(5), Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if cd.PartnerID != p2.ID || cd.WinnersCount != 3 || cd.PrizesDescription != "B" {
		t.Errorf("day = %+v", cd)
	}
	if n := e.count(t, `SELECT count(*) FROM campaign_days`); n != 1 {
		t.Errorf("campaign_days = %d, want 1", n)
	}
	a, details := e.lastAudit(t)
	if a.Action != "day_set" || a.EntityID.Int64 != cd.ID || details["created"] != false || int64(details["previous_partner_id"].(float64)) != p1.ID {
		t.Errorf("audit = %+v %v", a, details)
	}
}

func TestSetDayValidation(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One")

	_, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(1).AddDate(0, 0, -1), PartnerID: p.ID, Winners: 1})
	wantInput(t, err, "вне периода акции (2026-09-01 — 2026-09-30)")
	_, err = e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(30).AddDate(0, 0, 1), PartnerID: p.ID, Winners: 1})
	wantInput(t, err, "вне периода акции")
	_, err = e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: 999, Winners: 1})
	wantInput(t, err, "Партнёр с id 999 не найден")

	// the period borders are inside
	for _, d := range []time.Time{day(1), day(30)} {
		if _, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: d, PartnerID: p.ID, Winners: 1}); err != nil {
			t.Errorf("%s: %v", d.Format(time.DateOnly), err)
		}
	}
}

func TestSetDayLockedAfterDraw(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One")
	res, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p.ID, Winners: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.SetCampaignDayStatus(e.ctx, db.SetCampaignDayStatusParams{ID: res.Day.ID, Status: "drawn"}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p.ID, Winners: 5})
	wantInput(t, err, "уже в статусе «drawn»")
}

func TestSetDayPartnerLockedByActivity(t *testing.T) {
	e := newEnv(t)
	p1, p2 := e.partner(t, "One"), e.partner(t, "Two")
	res, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p1.ID, Winners: 1})
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateDailyEntry(e.ctx, db.CreateDailyEntryParams{
		UserID: u.ID, CampaignDayID: res.Day.ID, SubscribedCheckedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p2.ID, Winners: 1})
	wantInput(t, err, "заявок в розыгрыше 1")
	// same partner, new prizes: allowed
	if _, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p1.ID, Winners: 4, Prizes: "new"}); err != nil {
		t.Errorf("updating prizes must work: %v", err)
	}
}

func parse(t *testing.T, csv string) *admin.PromoParse {
	t.Helper()
	p, err := admin.ParsePromoCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUploadPromoReport(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One")

	rep, err := e.svc.UploadPromo(e.ctx, adminTG, p.ID, parse(t, "code\nA1\nB2\nA1\nBAD CODE\nC3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Loaded != 3 || rep.DupInFile != 1 || len(rep.Invalid) != 1 || rep.AlreadyInDB != 0 || rep.Rows != 5 || !rep.HeaderSkipped {
		t.Errorf("report = %+v", rep)
	}
	if n := e.count(t, `SELECT count(*) FROM promo_codes WHERE partner_id = $1`, p.ID); n != 3 {
		t.Errorf("stored codes = %d, want 3", n)
	}

	// a second file overlapping the first: only the new code is added
	rep, err = e.svc.UploadPromo(e.ctx, adminTG, p.ID, parse(t, "A1\nB2\nD4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Loaded != 1 || rep.AlreadyInDB != 2 {
		t.Errorf("report = %+v, want 1 loaded and 2 already in db", rep)
	}
	a, details := e.lastAudit(t)
	if a.Action != "promo_upload" || details["loaded"] != float64(1) || details["already_in_db"] != float64(2) {
		t.Errorf("audit = %+v %v", a, details)
	}
}

func TestUploadPromoSameCodeForOtherPartnerIsFine(t *testing.T) {
	e := newEnv(t)
	p1, p2 := e.partner(t, "One"), e.partner(t, "Two")
	for _, p := range []db.Partner{p1, p2} {
		rep, err := e.svc.UploadPromo(e.ctx, adminTG, p.ID, parse(t, "SAME\n"))
		if err != nil || rep.Loaded != 1 {
			t.Fatalf("partner %d: rep = %+v, err = %v", p.ID, rep, err)
		}
	}
}

func TestUploadPromoUnknownPartnerAndNothingValid(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.UploadPromo(e.ctx, adminTG, 404, parse(t, "A1\n"))
	wantInput(t, err, "Партнёр с id 404 не найден")

	p := e.partner(t, "One")
	rep, err := e.svc.UploadPromo(e.ctx, adminTG, p.ID, parse(t, "BAD CODE\n"))
	if err != nil || rep.Loaded != 0 || len(rep.Invalid) != 1 {
		t.Errorf("rep = %+v, err = %v", rep, err)
	}
}

func TestPromoStats(t *testing.T) {
	e := newEnv(t)
	p1, p2 := e.partner(t, "One"), e.partner(t, "Two")
	if _, err := e.svc.UploadPromo(e.ctx, adminTG, p1.ID, parse(t, "A\nB\nC\n")); err != nil {
		t.Fatal(err)
	}
	// issue one code of p1 to a user
	u, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: 1})
	if _, err := e.store.ClaimPromoCode(e.ctx, db.ClaimPromoCodeParams{
		TargetUserID: pgtype.Int8{Int64: u.ID, Valid: true}, TargetPartnerID: p1.ID,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := e.svc.PromoStats(e.ctx, adminTG)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].ID != p1.ID || rows[0].Total != 3 || rows[0].Free != 2 {
		t.Errorf("partner 1 stats = %+v, want total 3 free 2", rows[0])
	}
	if rows[1].ID != p2.ID || rows[1].Total != 0 || rows[1].Free != 0 {
		t.Errorf("partner without codes = %+v, want zeros", rows[1])
	}
}

func TestExportParticipantsAndWinners(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One")
	res, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p.ID, Winners: 1, Prizes: "prize"})
	if err != nil {
		t.Fatal(err)
	}
	evil, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{
		TelegramID: 111, Username: pgtype.Text{String: "=HYPERLINK(1)", Valid: true}, FirstName: pgtype.Text{String: "Иван", Valid: true},
	})
	plain, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: 222})
	for _, u := range []db.User{evil, plain} {
		if _, err := e.store.CreateDailyEntry(e.ctx, db.CreateDailyEntryParams{
			UserID: u.ID, CampaignDayID: res.Day.ID, SubscribedCheckedAt: pgtype.Timestamptz{Time: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), Valid: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.store.InsertDailyWinner(e.ctx, db.InsertDailyWinnerParams{CampaignDayID: res.Day.ID, UserID: plain.ID, Prize: "prize"}); err != nil {
		t.Fatal(err)
	}

	// by date and by id give the same file
	byDate, err := e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportParticipants, Date: day(5), Ref: "2026-09-05"})
	if err != nil {
		t.Fatal(err)
	}
	byID, err := e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportParticipants, ID: res.Day.ID, Ref: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(byDate.Data) != string(byID.Data) || byDate.Rows != 2 || byDate.Name != "participants_2026-09-05.csv" {
		t.Errorf("by date: %q rows=%d name=%s", byDate.Data, byDate.Rows, byDate.Name)
	}
	csv := string(byDate.Data)
	for _, want := range []string{"\xEF\xBB\xBFtelegram_id,username,first_name,registered_at,excluded", "111,'=HYPERLINK(1),Иван,2026-09-05 12:00:00,", "222,,,"} {
		if !strings.Contains(csv, want) {
			t.Errorf("csv lacks %q:\n%s", want, csv)
		}
	}

	w, err := e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportWinners, ID: res.Day.ID, Ref: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Rows != 1 || !strings.Contains(string(w.Data), "222,,,prize,false,selected") {
		t.Errorf("winners csv (%d rows):\n%s", w.Rows, w.Data)
	}
	a, details := e.lastAudit(t)
	if a.Action != "export" || a.EntityType != "campaign_day" || details["kind"] != "winners" || details["rows"] != float64(1) {
		t.Errorf("audit = %+v %v", a, details)
	}
}

func TestExportFinalistsAndNotFound(t *testing.T) {
	e := newEnv(t)
	fd, err := e.store.CreateFinalDraw(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: 5, Username: pgtype.Text{String: "ann", Valid: true}})
	if _, err := e.store.InsertFinalWinner(e.ctx, db.InsertFinalWinnerParams{FinalDrawID: fd.ID, UserID: u.ID, TicketsCount: 3}); err != nil {
		t.Fatal(err)
	}

	f, err := e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportFinalists, ID: fd.ID, Ref: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Rows != 1 || !strings.Contains(string(f.Data), "5,ann,,3,false,,selected") {
		t.Errorf("finalists csv:\n%s", f.Data)
	}

	_, err = e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportFinalists, ID: 999, Ref: "999"})
	wantInput(t, err, "Финальный розыгрыш с id 999 не найден")
	_, err = e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportParticipants, Date: day(9), Ref: "2026-09-09"})
	wantInput(t, err, "День «2026-09-09» не найден")
	_, err = e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportWinners, ID: 999, Ref: "999"})
	wantInput(t, err, "не найден")
}

func TestEveryAdminActionIsAudited(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One") // partner_add
	steps := []struct {
		action string
		run    func() error
	}{
		{"partner_list", func() error { _, err := e.svc.ListPartners(e.ctx, adminTG); return err }},
		{"day_set", func() error {
			_, err := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p.ID, Winners: 1})
			return err
		}},
		{"promo_upload", func() error { _, err := e.svc.UploadPromo(e.ctx, adminTG, p.ID, parse(t, "A\n")); return err }},
		{"promo_stats", func() error { _, err := e.svc.PromoStats(e.ctx, adminTG); return err }},
		{"export", func() error {
			_, err := e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportParticipants, Date: day(5), Ref: "x"})
			return err
		}},
		{"menu", func() error { return e.svc.Audit(e.ctx, adminTG, admin.ActionMenu, nil) }},
	}
	for _, s := range steps {
		before := e.count(t, `SELECT count(*) FROM audit_log WHERE action = $1`, s.action)
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.action, err)
		}
		if after := e.count(t, `SELECT count(*) FROM audit_log WHERE action = $1 AND admin_telegram_id = $2`, s.action, adminTG); after != before+1 {
			t.Errorf("%s: audit rows %d -> %d, want +1", s.action, before, after)
		}
	}
}

func TestEditPartner(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One") // chat_id -100
	until := time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)

	steps := []struct {
		edit     admin.PartnerEdit
		old, new string
		check    func(db.Partner) bool
	}{
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldChatID, ChatID: -777}, "-100", "-777", func(p db.Partner) bool { return p.ChatID.Int64 == -777 }},
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldName, Name: "Renamed"}, "One", "Renamed", func(p db.Partner) bool { return p.Name == "Renamed" }},
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldURL, URL: "https://t.me/new"}, "https://t.me/x", "https://t.me/new", func(p db.Partner) bool { return p.Url == "https://t.me/new" }},
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldTerms, Terms: "T"}, "", "T", func(p db.Partner) bool { return p.PromoTerms == "T" }},
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldPromoText, PromoText: "на сайте"}, "", "на сайте", func(p db.Partner) bool { return p.PromoText == "на сайте" }},
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldValidUntil, ValidUntil: &until}, "", "2026-09-30", func(p db.Partner) bool { return p.PromoValidUntil.Valid && p.PromoValidUntil.Time.Equal(until) }},
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldValidUntil, Clear: true}, "2026-09-30", "", func(p db.Partner) bool { return !p.PromoValidUntil.Valid }},
		{admin.PartnerEdit{ID: p.ID, Field: admin.FieldPromoText, Clear: true}, "на сайте", "", func(p db.Partner) bool { return p.PromoText == "" }},
	}
	for _, s := range steps {
		res, err := e.svc.EditPartner(e.ctx, adminTG, s.edit)
		if err != nil {
			t.Fatalf("%s: %v", s.edit.Field, err)
		}
		if !res.Changed || res.Old != s.old || res.New != s.new {
			t.Errorf("%s: res = %+v, want %q -> %q", s.edit.Field, res, s.old, s.new)
		}
		got, err := e.store.GetPartner(e.ctx, p.ID)
		if err != nil || !s.check(got) {
			t.Errorf("%s: stored partner = %+v, err %v", s.edit.Field, got, err)
		}
	}
	// the other fields survived all edits
	got, _ := e.store.GetPartner(e.ctx, p.ID)
	if got.Name != "Renamed" || got.ChatID.Int64 != -777 || got.PromoTerms != "T" {
		t.Errorf("partner after edits = %+v", got)
	}
	a, details := e.lastAudit(t)
	if a.Action != "partner_edit" || a.EntityID.Int64 != p.ID || details["field"] != "promo_text" || details["old"] != "на сайте" {
		t.Errorf("audit = %+v %v", a, details)
	}
}

func TestEditPartnerNoChangeIsNotAudited(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One")
	res, err := e.svc.EditPartner(e.ctx, adminTG, admin.PartnerEdit{ID: p.ID, Field: admin.FieldName, Name: "One"})
	if err != nil || res.Changed {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'partner_edit'`); n != 0 {
		t.Errorf("audit rows = %d, want 0", n)
	}
}

func TestEditPartnerErrors(t *testing.T) {
	e := newEnv(t)
	p1 := e.partner(t, "One")
	e.partner(t, "Two")

	_, err := e.svc.EditPartner(e.ctx, adminTG, admin.PartnerEdit{ID: 404, Field: admin.FieldName, Name: "X"})
	wantInput(t, err, "Партнёр с id 404 не найден")
	_, err = e.svc.EditPartner(e.ctx, adminTG, admin.PartnerEdit{ID: p1.ID, Field: admin.FieldName, Name: "TWO"})
	wantInput(t, err, "уже существует")
	// renaming to a different case of its own name is allowed
	if res, err := e.svc.EditPartner(e.ctx, adminTG, admin.PartnerEdit{ID: p1.ID, Field: admin.FieldName, Name: "ONE"}); err != nil || !res.Changed {
		t.Errorf("case-only rename: res = %+v, err = %v", res, err)
	}
}

func TestExportShowsExcludedParticipants(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One")
	res, _ := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p.ID, Winners: 1})
	u, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: 333})
	entry, err := e.store.CreateDailyEntry(e.ctx, db.CreateDailyEntryParams{
		UserID: u.ID, CampaignDayID: res.Day.ID, SubscribedCheckedAt: pgtype.Timestamptz{Time: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.ExcludeDailyEntry(e.ctx, db.ExcludeDailyEntryParams{ID: entry.ID, Reason: "unsubscribed"}); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportParticipants, ID: res.Day.ID, Ref: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(f.Data), "333,,,2026-09-05 12:00:00,unsubscribed") {
		t.Errorf("csv:\n%s", f.Data)
	}
}

func TestExportPartnerWinnersOnlyAfterConfirmation(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "Кофейня")
	res, _ := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p.ID, Winners: 1, Prizes: "prize"})
	u, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{
		TelegramID: 444, Username: pgtype.Text{String: "ann", Valid: true}, FirstName: pgtype.Text{String: "Анна", Valid: true},
	})
	if _, err := e.store.InsertDailyWinner(e.ctx, db.InsertDailyWinnerParams{CampaignDayID: res.Day.ID, UserID: u.ID, Prize: "prize"}); err != nil {
		t.Fatal(err)
	}
	req := admin.ExportRequest{Kind: admin.ExportPartnerWins, ID: res.Day.ID, Ref: "x"}

	for _, status := range []string{"pending", "drawn"} {
		if status != "pending" {
			if _, err := e.store.SetCampaignDayStatus(e.ctx, db.SetCampaignDayStatusParams{ID: res.Day.ID, Status: status}); err != nil {
				t.Fatal(err)
			}
		}
		_, err := e.svc.Export(e.ctx, adminTG, req)
		wantInput(t, err, "ещё не подтверждены (статус «"+status+"»)")
	}

	if _, err := e.store.SetCampaignDayStatus(e.ctx, db.SetCampaignDayStatusParams{ID: res.Day.ID, Status: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.Export(e.ctx, adminTG, req)
	if err != nil {
		t.Fatal(err)
	}
	if f.Rows != 1 || f.Name != "partner_winners_2026-09-05.csv" ||
		!strings.Contains(string(f.Data), "date,partner,username,first_name,prize\n2026-09-05,Кофейня,ann,Анна,prize") {
		t.Errorf("partner csv (%s):\n%s", f.Name, f.Data)
	}
	if strings.Contains(string(f.Data), "444") {
		t.Error("the partner file must not contain internal telegram ids")
	}
}
