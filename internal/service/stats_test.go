package service_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

func (e *env) setCreatedAt(t *testing.T, u db.User, at time.Time) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx, `UPDATE users SET created_at = $1 WHERE id = $2`, at, u.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPromoRequestsAreCounted(t *testing.T) {
	e := newEnv(t, 1) // one code only
	ad := e.day(t, 1)
	svc := service.NewPromo(e.store, 10)
	a, b := e.user(t, 1), e.user(t, 2)

	if _, err := svc.Issue(e.ctx, a, ad); err != nil { // issued
		t.Fatal(err)
	}
	if _, err := svc.Issue(e.ctx, a, ad); err != nil { // the same code again: still a request
		t.Fatal(err)
	}
	if _, err := svc.Issue(e.ctx, b, ad); err == nil { // pool is empty
		t.Fatal("expected the pool to be empty")
	}
	if _, err := svc.Issue(e.ctx, b, ad); err == nil {
		t.Fatal("expected the pool to be empty")
	}

	type row struct {
		requests int
		issued   bool
	}
	got := map[int64]row{}
	rows, err := e.pool.Query(e.ctx, `SELECT user_id, requests, issued FROM promo_requests`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		var r row
		if err := rows.Scan(&uid, &r.requests, &r.issued); err != nil {
			t.Fatal(err)
		}
		got[uid] = r
	}
	if got[a] != (row{2, true}) || got[b] != (row{2, false}) || len(got) != 2 {
		t.Errorf("promo_requests = %+v, want user a {2 true}, user b {2 false}", got)
	}
}

func TestStatsReport(t *testing.T) {
	e := newEnv(t, 3)
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := e.store.CreatePartner(e.ctx, db.CreatePartnerParams{Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.AddPromoCode(e.ctx, db.AddPromoCodeParams{PartnerID: p2.ID, Code: "X"}); err != nil {
		t.Fatal(err)
	}
	day1, day2 := e.day(t, 1), e.day(t, 2)

	// users and campaign days: the day starts at 03:00 Moscow time
	u := make([]db.User, 7)
	for i := range u {
		u[i] = e.userRow(t, int64(100+i))
	}
	at := func(d, h, m int) time.Time { return time.Date(2026, 9, d, h, m, 0, 0, msk) }
	e.setCreatedAt(t, u[0], at(5, 1, 0))  // before 03:00: still Sep 4
	e.setCreatedAt(t, u[1], at(5, 3, 0))  // Sep 5
	e.setCreatedAt(t, u[2], at(5, 23, 0)) // Sep 5
	e.setCreatedAt(t, u[3], at(6, 2, 59)) // Sep 5
	e.setCreatedAt(t, u[4], at(6, 3, 0))  // Sep 6
	e.setCreatedAt(t, u[5], at(6, 12, 0)) // Sep 6
	e.setCreatedAt(t, u[6], at(7, 12, 0)) // Sep 7
	if err := e.store.SetUserBlocked(e.ctx, db.SetUserBlockedParams{TelegramID: u[6].TelegramID, IsBlocked: true}); err != nil {
		t.Fatal(err)
	}

	// promo: u0 and u1 get codes on day 1, u2 gets one on day 2; u3 asks on day 2 after the partner ran dry
	promo := service.NewPromo(e.store, 10)
	for _, p := range []struct {
		u  db.User
		ad *service.ActiveDay
	}{{u[0], day1}, {u[1], day1}, {u[2], day2}} {
		if _, err := promo.Issue(e.ctx, p.u.ID, p.ad); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := promo.Issue(e.ctx, u[0].ID, day1); err != nil { // repeat
		t.Fatal(err)
	}
	if _, err := promo.Issue(e.ctx, u[3].ID, day2); err == nil {
		t.Fatal("expected sold out")
	}

	// draw entries: day 1 has three (one dropped), day 2 has one
	entry := func(user db.User, ad *service.ActiveDay) db.DailyEntry {
		en, err := e.store.CreateDailyEntry(e.ctx, db.CreateDailyEntryParams{
			UserID: user.ID, CampaignDayID: ad.Day.ID, SubscribedCheckedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return en
	}
	entry(u[0], day1)
	entry(u[1], day1)
	dropped := entry(u[2], day1)
	entry(u[4], day2)
	if err := e.store.ExcludeDailyEntry(e.ctx, db.ExcludeDailyEntryParams{ID: dropped.ID, Reason: "unsubscribed"}); err != nil {
		t.Fatal(err)
	}

	// Chances so far: u0 and u1 have 1 (promo and entry on day 1 are the same active day), u2 has 1 (day 2).
	// More active days are added directly: u2 gets day 1 (2 in total), u4 gets days 1-3 (3 in total).
	extra := func(user db.User, days ...*service.ActiveDay) {
		for _, d := range days {
			if _, err := e.store.AddActivityDay(e.ctx, db.AddActivityDayParams{UserID: user.ID, CampaignDayID: d.Day.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	day3 := e.day(t, 3)
	extra(u[2], day1) // u2: day 2 (from promo) + day 1
	extra(u[4], day1, day2, day3)

	rep, err := service.NewStats(e.store, msk, 3*time.Hour, 3).Report(e.ctx)
	if err != nil {
		t.Fatal(err)
	}

	if rep.TotalUsers != 7 || rep.BlockedUsers != 1 {
		t.Errorf("users = %d (blocked %d), want 7 (1)", rep.TotalUsers, rep.BlockedUsers)
	}
	wantNew := []service.DayCount{{Day: sep(4), Count: 1}, {Day: sep(5), Count: 3}, {Day: sep(6), Count: 2}, {Day: sep(7), Count: 1}}
	if fmt.Sprint(rep.NewUsers) != fmt.Sprint(wantNew) {
		t.Errorf("new users by day = %v, want %v (day starts at 03:00 Moscow time)", rep.NewUsers, wantNew)
	}

	if rep.PromoRequests != 5 || rep.PromoRequesters != 4 || rep.PromoGotCode != 3 || rep.PromoNoCode != 1 || rep.PromoIssued != 3 {
		t.Errorf("promo: requests %d, requesters %d, got %d, none %d, issued %d; want 5, 4, 3, 1, 3",
			rep.PromoRequests, rep.PromoRequesters, rep.PromoGotCode, rep.PromoNoCode, rep.PromoIssued)
	}

	if len(rep.Partners) != 2 || rep.Partners[0].Total != 3 || rep.Partners[0].Free != 0 || rep.Partners[0].Issued != 3 ||
		rep.Partners[1].Name != "Second" || rep.Partners[1].Total != 1 || rep.Partners[1].Free != 1 {
		t.Errorf("partners = %+v", rep.Partners)
	}

	if len(rep.Days) != 3 {
		t.Fatalf("days = %+v", rep.Days)
	}
	d1, d2, d3 := rep.Days[0], rep.Days[1], rep.Days[2]
	if d1.Entries != 3 || d1.Excluded != 1 || d1.PromoRequesters != 2 || d1.PromoIssued != 2 || d1.Partner != "Partner" {
		t.Errorf("day 1 = %+v", d1)
	}
	if d2.Entries != 1 || d2.Excluded != 0 || d2.PromoRequesters != 2 || d2.PromoIssued != 1 {
		t.Errorf("day 2 = %+v", d2)
	}
	if d3.Entries != 0 || d3.PromoRequesters != 0 || d3.PromoIssued != 0 {
		t.Errorf("a day without activity must show zeros, got %+v", d3)
	}

	// chances: u0 (promo+entry day 1) = 1, u1 = 1, u2 = 2, u4 = 3, the other three have none
	want := []service.ChanceBucket{{0, 3}, {1, 2}, {2, 1}, {3, 1}}
	if fmt.Sprint(rep.Distribution) != fmt.Sprint(want) || rep.FullChances != 1 || rep.MaxChances != 3 {
		t.Errorf("distribution = %v (full %d), want %v (full 1)", rep.Distribution, rep.FullChances, want)
	}
}

func TestStatsReportOfAnEmptyDatabase(t *testing.T) {
	e := newEnv(t, 0)
	rep, err := service.NewStats(e.store, time.UTC, 0, 10).Report(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalUsers != 0 || rep.PromoRequests != 0 || len(rep.NewUsers) != 0 || len(rep.Days) != 0 {
		t.Errorf("report = %+v", rep)
	}
	if len(rep.Distribution) != 11 || rep.Distribution[0].Users != 0 || rep.FullChances != 0 {
		t.Errorf("distribution = %v", rep.Distribution)
	}
}

// TestStatsReportOnALargeDatabase checks the numbers and the speed with 100 000
// users and about 500 000 active days (seeding takes ~40 s). Opt-in: STATS_PERF=1.
func TestStatsReportOnALargeDatabase(t *testing.T) {
	if os.Getenv("STATS_PERF") == "" {
		t.Skip("large data set; run with STATS_PERF=1 (make test-perf)")
	}
	e := newEnv(t, 0)
	const users = 100_000
	for d := 1; d <= 10; d++ {
		e.day(t, d)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(e.ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users (telegram_id, created_at, is_blocked)
	      SELECT 1000000 + g, timestamptz '2026-09-01 12:00+00' + (g % 20) * interval '1 day', g % 50 = 0
	      FROM generate_series(1, $1) g`, users)
	// user number k has k % 11 active days (0..10), on the first days of the campaign
	exec(`INSERT INTO activity_days (user_id, campaign_day_id)
	      SELECT u.id, cd.id FROM users u JOIN campaign_days cd ON cd.id <= (u.id % 11)`)
	exec(`INSERT INTO daily_entries (user_id, campaign_day_id, subscribed_checked_at)
	      SELECT user_id, campaign_day_id, now() FROM activity_days`)
	exec(`INSERT INTO promo_requests (user_id, campaign_day_id, requests, issued)
	      SELECT user_id, campaign_day_id, 2, false FROM activity_days`)
	exec(`ANALYZE`)

	start := time.Now()
	rep, err := service.NewStats(e.store, time.UTC, 0, 10).Report(e.ctx)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}

	if rep.TotalUsers != users || rep.BlockedUsers != users/50 {
		t.Errorf("users = %d (blocked %d)", rep.TotalUsers, rep.BlockedUsers)
	}
	if len(rep.NewUsers) != 20 {
		t.Errorf("new-user days = %d, want 20", len(rep.NewUsers))
	}
	want := map[int]int{} // ids are 1..users and user k has k % 11 active days
	for id := 1; id <= users; id++ {
		want[id%11]++
	}
	for _, b := range rep.Distribution {
		if b.Users != want[b.Chances] {
			t.Errorf("%d chances: %d users, want %d", b.Chances, b.Users, want[b.Chances])
		}
	}
	if rep.FullChances != rep.Distribution[10].Users || rep.FullChances == 0 {
		t.Errorf("full = %d", rep.FullChances)
	}
	if rep.Days[0].Entries != rep.Days[0].PromoRequesters || rep.PromoRequests != 2*rep.PromoRequesters {
		t.Errorf("day 1 = %+v, requests %d / requesters %d", rep.Days[0], rep.PromoRequests, rep.PromoRequesters)
	}
	if took > 3*time.Second {
		t.Errorf("the report took %s on %d users, want well under 3s", took, users)
	}
	t.Logf("stats report for %d users and %d active days took %s", users, rep.PromoRequesters, took)
}

// sep is a September 2026 campaign date.
func sep(n int) time.Time { return time.Date(2026, 9, n, 0, 0, 0, 0, time.UTC) }
