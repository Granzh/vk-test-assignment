package admin_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/admin"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/texts"
)

func TestWinnersViewAndContactExportRespectConsent(t *testing.T) {
	e := newEnv(t)
	fd, err := e.store.CreateFinalDraw(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(tg int64, name string, reserve bool, pos int32) db.FinalWinner {
		u, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: tg, Username: pgtype.Text{String: name, Valid: true}})
		w, err := e.store.InsertFinalWinnerRanked(e.ctx, db.InsertFinalWinnerRankedParams{
			FinalDrawID: fd.ID, UserID: u.ID, TicketsCount: 2, IsReserve: reserve, ReservePosition: pgtype.Int4{Int32: pos, Valid: pos > 0},
		})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	consented := mk(1, "ann", false, 0)
	typed := mk(2, "bob", false, 0) // gave a name but has not consented yet
	mk(3, "cat", true, 1)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(e.ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE final_winners SET status = 'notified', deadline = now() + interval '1 day' WHERE id IN ($1, $2)`, consented.ID, typed.ID)
	exec(`UPDATE final_winners SET contact_name = 'Анна Иванова', dialog_step = 'consent' WHERE id = $1`, consented.ID)
	exec(`UPDATE final_winners SET consent_given_at = now(), status = 'contact_received', dialog_step = NULL WHERE id = $1`, consented.ID)
	exec(`UPDATE final_winners SET contact_name = 'Борис Петров', dialog_step = 'consent' WHERE id = $1`, typed.ID)

	view, err := e.svc.Winners(e.ctx, adminTG, admin.WinnersRequest{Final: true})
	if err != nil {
		t.Fatal(err)
	}
	text := texts.AdminWinners(view)
	for _, want := range []string{"согласие и данные получены", "имя получено, ждём согласие", "резерв №1", "ждёт своей очереди"} {
		if !strings.Contains(text, want) {
			t.Errorf("view lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Иванова") || strings.Contains(text, "Петров") {
		t.Errorf("names must not appear in the chat view:\n%s", text)
	}

	f, err := e.svc.Export(e.ctx, adminTG, admin.ExportRequest{Kind: admin.ExportFinalists, ID: fd.ID, Ref: "x"})
	if err != nil {
		t.Fatal(err)
	}
	csv := string(f.Data)
	if !strings.Contains(csv, "Анна Иванова") {
		t.Errorf("the contact of the consenting winner is missing:\n%s", csv)
	}
	if strings.Contains(csv, "Борис") {
		t.Errorf("a name without consent was exported:\n%s", csv)
	}
	if !strings.Contains(csv, "forfeit_reason") || !strings.Contains(csv, "consent_at") {
		t.Errorf("header:\n%s", csv)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'winners' AND admin_telegram_id = $1`, adminTG); n != 1 {
		t.Errorf("winners audit rows = %d", n)
	}
}

func TestWinnersOfADayAndErrors(t *testing.T) {
	e := newEnv(t)
	p := e.partner(t, "One")
	res, _ := e.svc.SetDay(e.ctx, adminTG, admin.DayInput{Day: day(5), PartnerID: p.ID, Winners: 1})
	u, _ := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: 7, FirstName: pgtype.Text{String: "Анна", Valid: true}})
	if _, err := e.store.InsertDailyWinner(e.ctx, db.InsertDailyWinnerParams{CampaignDayID: res.Day.ID, UserID: u.ID, Prize: "prize"}); err != nil {
		t.Fatal(err)
	}
	view, err := e.svc.Winners(e.ctx, adminTG, admin.WinnersRequest{Date: day(5)})
	if err != nil || len(view.Lines) != 1 || view.Lines[0].Status != "выбран, ещё не уведомлён" || view.ID != res.Day.ID {
		t.Errorf("view = %+v, err = %v", view, err)
	}
	_, err = e.svc.Winners(e.ctx, adminTG, admin.WinnersRequest{Date: day(9)})
	wantInput(t, err, "не назначен партнёр")
	_, err = e.svc.Winners(e.ctx, adminTG, admin.WinnersRequest{Final: true})
	wantInput(t, err, "Финальный розыгрыш ещё не проводился")
}
