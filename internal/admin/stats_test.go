package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/service"
	"chat_bot/internal/texts"
)

var statsMSK = time.FixedZone("MSK", 3*3600)

func sampleReport() service.StatsReport {
	d := func(n int) time.Time { return time.Date(2026, 9, n, 0, 0, 0, 0, time.UTC) }
	var newUsers []service.DayCount
	for i := 1; i <= 20; i++ {
		newUsers = append(newUsers, service.DayCount{Day: d(i), Count: i * 10})
	}
	return service.StatsReport{
		GeneratedAt: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC),
		TotalUsers:  1000, BlockedUsers: 30,
		NewUsers:      newUsers,
		PromoRequests: 700, PromoRequesters: 500, PromoGotCode: 450, PromoNoCode: 50, PromoIssued: 450,
		Partners: []service.PartnerStock{{ID: 1, Name: "Кофейня <&>", Total: 500, Free: 50, Issued: 450}, {ID: 2, Name: "Пустой", Total: 10, Free: 0, Issued: 10}},
		Days: []service.DrawStat{
			{Day: d(5), Partner: "Кофейня <&>", Status: "drawn", Entries: 300, Excluded: 4, PromoRequesters: 400, PromoIssued: 380},
			{Day: d(6), Partner: "Пустой", Status: "pending"},
		},
		MaxChances: 10,
		Distribution: []service.ChanceBucket{
			{Chances: 0, Users: 400}, {Chances: 1, Users: 300}, {Chances: 2, Users: 200}, {Chances: 10, Users: 100},
		},
		FullChances: 100,
	}
}

func (f *fakeBackend) Stats(_ context.Context, _ int64, csv bool) (service.StatsReport, error) {
	if csv {
		f.calls = append(f.calls, "Stats csv")
	} else {
		f.calls = append(f.calls, "Stats")
	}
	return sampleReport(), f.err
}

func TestParseStatsArgs(t *testing.T) {
	for in, want := range map[string]bool{"": false, "  ": false, "csv": true, "CSV": true} {
		got, err := parseStatsArgs(in)
		if err != nil || got != want {
			t.Errorf("%q: got (%v, %v), want %v", in, got, err, want)
		}
	}
	for in, sub := range map[string]string{"xlsx": "Допустим только параметр csv", "csv extra": "получено полей: 2"} {
		_, err := parseStatsArgs(in)
		wantInputErr(t, err, sub)
	}
}

func TestStatsMessage(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "")
	if err := newHandlers(be).stats(c); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sentTexts(c), "\n")
	for _, want := range []string{
		"Запустили бот: 1000 (заблокировали бота: 30)",
		"последние 14 из 20", "• 20.09 — 200", "• 07.09 — 70",
		"Запросов: 700 от 500", "Получили код: 450, кодов не хватило: 50", "Выдано кодов: 450",
		"• Кофейня &lt;&amp;&gt; — свободно 50 из 500 (выдано 450)", "• Пустой — свободно 0 из 10 (выдано 10) ⚠️",
		"05.09 · Кофейня &lt;&amp;&gt; — участников 300 (выбыло 4); промокод: запросили 400, выдано 380",
		"06.09 · Пустой — участников 0;",
		"максимум 10", "• 0 — 400 (40.0%)", "• 10 — 100 (10.0%)", "Набрали максимум (10): 100", "/stats csv",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "• 06.09 — 60\n") && strings.Contains(got, "• 06.09 — 60") && false {
		t.Error("unreachable")
	}
	if strings.Contains(got, "• 06.09 — 60") {
		t.Errorf("only the last 14 days must be listed, but day 6 (the 6th of 20) is there:\n%s", got)
	}
	if len(be.calls) != 1 || be.calls[0] != "Stats" {
		t.Errorf("calls = %v", be.calls)
	}
}

func sentTexts(c *fakeCtx) []string {
	var out []string
	for _, s := range c.sent {
		out = append(out, s.text())
	}
	return out
}

func TestStatsFitsTelegramMessageLimit(t *testing.T) {
	r := sampleReport()
	for i := 0; i < 60; i++ { // a long campaign
		r.Days = append(r.Days, service.DrawStat{Day: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i), Partner: "Партнёр", Entries: 100})
	}
	for _, part := range splitMessage(texts.AdminStats(statsView(r), statsMSK), maxMessageRunes) {
		if n := len([]rune(part)); n > 4096 {
			t.Errorf("message part of %d characters exceeds Telegram's limit", n)
		}
	}
}

func TestStatsCSVCommand(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(10, "csv")
	if err := newHandlers(be).stats(c); err != nil {
		t.Fatal(err)
	}
	doc, ok := c.sent[0].what.(*tele.Document)
	if !ok || !strings.HasPrefix(doc.FileName, "stats_2026-10-18_1000") || !strings.HasSuffix(doc.FileName, ".csv") {
		t.Fatalf("sent = %+v", c.sent)
	}
	if len(be.calls) != 1 || be.calls[0] != "Stats csv" {
		t.Errorf("calls = %v", be.calls)
	}
}

func TestStatsCSVContent(t *testing.T) {
	f, err := StatsCSV(sampleReport(), statsMSK)
	if err != nil {
		t.Fatal(err)
	}
	csv := string(f.Data)
	for _, want := range []string{
		"\xEF\xBB\xBFsection,key,label,value,extra",
		"summary,generated_at,время формирования,2026-10-18 13:00:00,",
		"summary,total_users,запустили бот,1000,",
		"summary,blocked_users,заблокировали бота,30,",
		"summary,promo_requests,запросов промокода (нажатий),700,",
		"summary,promo_issued,выдано промокодов,450,",
		"summary,full_chances,пользователей с максимумом шансов (10),100,",
		"new_users,2026-09-01,новые пользователи,10,",
		"new_users,2026-09-20,новые пользователи,200,",
		"partner_stock,1,Кофейня <&>,50,total=500;issued=450",
		"draw,2026-09-05,Кофейня <&>,300,excluded=4;promo_requesters=400;promo_issued=380;status=drawn",
		"chances,0,пользователей с таким числом шансов,400,",
		"chances,10,пользователей с таким числом шансов,100,",
	} {
		if !strings.Contains(csv, want) {
			t.Errorf("csv lacks %q:\n%s", want, csv)
		}
	}
	// every new-user day is exported, not just the last 14
	if n := strings.Count(csv, "\nnew_users,"); n != 20 {
		t.Errorf("new_users rows = %d, want 20", n)
	}
	if f.Name != "stats_2026-10-18_1300.csv" {
		t.Errorf("name = %s", f.Name)
	}
}

func TestStatsNeedsAdmin(t *testing.T) {
	be := &fakeBackend{}
	c := adminCtx(99, "csv")
	if err := route([]int64{10}, newHandlers(be).stats)(c); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 0 || len(be.calls) != 0 {
		t.Errorf("non-admin: sent %v, calls %v", c.sent, be.calls)
	}
}
