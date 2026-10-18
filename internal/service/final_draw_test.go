package service_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"chat_bot/internal/domain"
	"chat_bot/internal/service"
	"chat_bot/internal/storage/db"
)

// --- pure drum tests ---

func TestPickByTicketsNoRepeatsAndBounded(t *testing.T) {
	tickets := []int{3, 1, 4, 1, 5, 9, 2, 6}
	for k := 0; k <= len(tickets)+3; k++ {
		got, err := service.PickByTickets(tickets, k, service.CryptoIntn)
		if err != nil {
			t.Fatal(err)
		}
		want := min(k, len(tickets))
		if len(got) != want {
			t.Fatalf("k=%d: picked %d, want %d", k, len(got), want)
		}
		seen := map[int]bool{}
		for _, i := range got {
			if seen[i] {
				t.Fatalf("k=%d: holder %d drawn twice: %v", k, i, got)
			}
			seen[i] = true
		}
	}
}

func TestPickByTicketsSkipsHoldersWithoutTickets(t *testing.T) {
	got, err := service.PickByTickets([]int{0, 2, 0, -1, 1}, 10, service.CryptoIntn)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(got)
	if fmt.Sprint(got) != "[1 4]" {
		t.Errorf("picked %v, want only holders 1 and 4", got)
	}
	if got, _ := service.PickByTickets(nil, 5, service.CryptoIntn); len(got) != 0 {
		t.Errorf("empty drum picked %v", got)
	}
}

func TestPickByTicketsWalksTheDrum(t *testing.T) {
	// holders own tickets [0,1] [2] [3,4,5]; ticket numbers map to owners in order
	for ticket, owner := range []int{0, 0, 1, 2, 2, 2} {
		first := true
		intn := func(n int) (int, error) {
			if first {
				first = false
				return ticket, nil
			}
			return 0, nil
		}
		got, err := service.PickByTickets([]int{2, 1, 3}, 1, intn)
		if err != nil || len(got) != 1 || got[0] != owner {
			t.Errorf("ticket %d: got %v, %v; want holder %d", ticket, got, err, owner)
		}
	}
}

func TestPickByTicketsRemovesWinnersOtherTickets(t *testing.T) {
	// the second draw must range over the remaining tickets only
	var asked []int
	intn := func(n int) (int, error) { asked = append(asked, n); return 0, nil }
	if _, err := service.PickByTickets([]int{5, 2, 3}, 3, intn); err != nil {
		t.Fatal(err)
	}
	// drum sizes: 10, then 5 (holder 0's five tickets gone), then 3 (holder 1's two gone)
	if fmt.Sprint(asked) != "[10 5 3]" {
		t.Errorf("drum sizes = %v, want [10 5 3]", asked)
	}
}

func TestPickByTicketsPropagatesRandomError(t *testing.T) {
	boom := errors.New("entropy exhausted")
	if _, err := service.PickByTickets([]int{1, 2}, 2, func(int) (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

// within checks |got-want| <= sigmas * sqrt(n p (1-p)).
func within(got, n int, p, sigmas float64) bool {
	want := float64(n) * p
	return math.Abs(float64(got)-want) <= sigmas*math.Sqrt(float64(n)*p*(1-p))
}

func TestPickByTicketsIsProportionalToTickets(t *testing.T) {
	tickets := []int{1, 2, 3, 4} // total 10
	const trials = 60000
	total := 10.0
	first := make([]int, 4)
	second := make([]int, 4)
	for i := 0; i < trials; i++ {
		got, err := service.PickByTickets(tickets, 2, service.CryptoIntn)
		if err != nil {
			t.Fatal(err)
		}
		first[got[0]]++
		second[got[1]]++
	}
	for h, w := range tickets {
		p1 := float64(w) / total
		if !within(first[h], trials, p1, 5) {
			t.Errorf("holder %d (%d tickets) won first place %d of %d times, expected about %.0f", h, w, first[h], trials, p1*trials)
		}
		// P(second = h) = sum over i != h of P(first = i) * w_h / (T - w_i)
		p2 := 0.0
		for i, wi := range tickets {
			if i != h {
				p2 += float64(wi) / total * float64(w) / (total - float64(wi))
			}
		}
		if !within(second[h], trials, p2, 5) {
			t.Errorf("holder %d came second %d of %d times, expected about %.0f", h, second[h], trials, p2*trials)
		}
	}
}

func TestMoreTicketsMeansMoreWinsButEveryoneCanWin(t *testing.T) {
	// 10 holders with 1 ticket and 1 holder with 10: the big one should win a
	// first place about half the time, a small one about 1/20.
	tickets := append(make([]int, 0, 11), 10)
	for i := 0; i < 10; i++ {
		tickets = append(tickets, 1)
	}
	const trials = 40000
	big, small0 := 0, 0
	for i := 0; i < trials; i++ {
		got, _ := service.PickByTickets(tickets, 1, service.CryptoIntn)
		switch got[0] {
		case 0:
			big++
		case 1:
			small0++
		}
	}
	if !within(big, trials, 0.5, 5) || !within(small0, trials, 0.05, 5) {
		t.Errorf("big holder won %d, small holder won %d of %d", big, small0, trials)
	}
}

// --- final draw on the database ---

type buttonOutbox struct {
	mu   sync.Mutex
	sent map[int64][]sentButton
	fail map[int64]bool
}

type sentButton struct{ html, label, unique string }

func newButtonOutbox() *buttonOutbox {
	return &buttonOutbox{sent: map[int64][]sentButton{}, fail: map[int64]bool{}}
}

func (o *buttonOutbox) SendWithButton(_ context.Context, id int64, html, label, unique string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail[id] {
		return errors.New("Forbidden: bot was blocked by the user")
	}
	o.sent[id] = append(o.sent[id], sentButton{html, label, unique})
	return nil
}

func (o *buttonOutbox) total() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, m := range o.sent {
		n += len(m)
	}
	return n
}

type finalEnv struct {
	*env
	out   *buttonOutbox
	final *service.FinalDraw
	camp  *service.Campaign
	days  []*service.ActiveDay
}

// newFinalEnv builds ten campaign days (Sep 1-10) and a clock showing Oct 1 (after the campaign).
func newFinalEnv(t *testing.T, winners, reserves int) *finalEnv {
	t.Helper()
	e := newEnv(t, 0)
	f := &finalEnv{env: e, out: newButtonOutbox()}
	for d := 1; d <= 10; d++ {
		f.days = append(f.days, e.day(t, d))
	}
	f.camp = service.NewCampaign(e.store, domain.Calendar{Location: time.UTC},
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	f.camp.SetNow(func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) })
	f.final = service.NewFinalDraw(e.store, f.out, f.camp, service.FinalDrawConfig{
		Winners: winners, Reserves: reserves, PrizeTickets: 2, Prize: "главный приз", ResponseTimeout: 48 * time.Hour, MaxChances: 10,
	}, discardLog)
	return f
}

// holder creates a user with n active days (n tickets) and returns them.
func (f *finalEnv) holder(t *testing.T, telegramID int64, n int) db.User {
	t.Helper()
	u := f.userRow(t, telegramID)
	for i := 0; i < n; i++ {
		if _, err := f.store.AddActivityDay(f.ctx, db.AddActivityDayParams{UserID: u.ID, CampaignDayID: f.days[i].Day.ID}); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func winnersOf(ws []service.FinalWinner, reserve bool) []service.FinalWinner {
	var out []service.FinalWinner
	for _, w := range ws {
		if w.Reserve == reserve {
			out = append(out, w)
		}
	}
	return out
}

func TestFinalDrawFiveWinnersFiveReserves(t *testing.T) {
	f := newFinalEnv(t, 5, 5)
	tickets := map[int64]int{}
	for i := 0; i < 14; i++ {
		n := i%10 + 1
		tickets[int64(100+i)] = n
		f.holder(t, int64(100+i), n)
	}

	res, err := f.final.Run(f.ctx, 77, false)
	if err != nil {
		t.Fatal(err)
	}
	ws, rs := winnersOf(res.Winners, false), winnersOf(res.Winners, true)
	if res.Already || len(ws) != 5 || len(rs) != 5 {
		t.Fatalf("already=%v winners=%d reserves=%d", res.Already, len(ws), len(rs))
	}
	seen := map[int64]bool{}
	for _, w := range res.Winners {
		if seen[w.TelegramID] {
			t.Errorf("user %d appears twice (winner/reserve)", w.TelegramID)
		}
		seen[w.TelegramID] = true
		if w.Tickets != tickets[w.TelegramID] {
			t.Errorf("user %d: stored %d tickets, had %d", w.TelegramID, w.Tickets, tickets[w.TelegramID])
		}
	}
	for i, r := range rs {
		if r.Position != i+1 {
			t.Errorf("reserve %d has queue position %d", i+1, r.Position)
		}
	}
	for _, w := range ws {
		if w.Position != 0 || w.Status != "selected" {
			t.Errorf("winner = %+v", w)
		}
	}

	// the protocol
	d := res.Draw
	wantTickets := 0
	for _, n := range tickets {
		wantTickets += n
	}
	if d.HeldByTelegramID != 77 || int(d.DrumTickets) != wantTickets || d.DrumUsers != 14 ||
		d.WinnersCount != 5 || d.ReservesCount != 5 || d.PrizeTickets != 2 || !d.HeldAt.Valid || d.Status != "drawn" {
		t.Errorf("protocol = %+v (want %d tickets)", d, wantTickets)
	}
	if n := f.count(t, `SELECT count(*) FROM final_winners`); n != 10 {
		t.Errorf("final_winners = %d, want 10", n)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_log WHERE action = 'final_run' AND admin_telegram_id = 77`); n != 1 {
		t.Errorf("final_run audit rows = %d", n)
	}
}

func TestFinalDrawFewerParticipantsThanWinners(t *testing.T) {
	f := newFinalEnv(t, 5, 5)
	for i := 0; i < 3; i++ {
		f.holder(t, int64(100+i), i+1)
	}
	res, err := f.final.Run(f.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if ws, rs := winnersOf(res.Winners, false), winnersOf(res.Winners, true); len(ws) != 3 || len(rs) != 0 {
		t.Errorf("winners = %d, reserves = %d; want 3 and 0", len(ws), len(rs))
	}
}

func TestFinalDrawReservesFillWhatIsLeft(t *testing.T) {
	f := newFinalEnv(t, 5, 5)
	for i := 0; i < 7; i++ {
		f.holder(t, int64(100+i), 1)
	}
	res, err := f.final.Run(f.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if ws, rs := winnersOf(res.Winners, false), winnersOf(res.Winners, true); len(ws) != 5 || len(rs) != 2 {
		t.Errorf("winners = %d, reserves = %d; want 5 and 2", len(ws), len(rs))
	}
}

func TestFinalDrawSingleParticipant(t *testing.T) {
	f := newFinalEnv(t, 5, 5)
	f.holder(t, 100, 4)
	res, err := f.final.Run(f.ctx, 1, false)
	if err != nil || len(res.Winners) != 1 || res.Winners[0].Reserve || res.Winners[0].Tickets != 4 {
		t.Errorf("res = %+v, err = %v", res.Winners, err)
	}
}

func TestFinalDrawNoTickets(t *testing.T) {
	f := newFinalEnv(t, 5, 5)
	f.userRow(t, 100) // a user without any active day
	if _, err := f.final.Run(f.ctx, 1, false); !errors.Is(err, service.ErrFinalNoTickets) {
		t.Errorf("err = %v, want ErrFinalNoTickets", err)
	}
	if n := f.count(t, `SELECT count(*) FROM final_draw`); n != 0 {
		t.Errorf("final_draw rows = %d, an empty draw must not be recorded", n)
	}
}

func TestFinalDrawTiming(t *testing.T) {
	f := newFinalEnv(t, 2, 0)
	f.holder(t, 100, 1)
	f.holder(t, 101, 1)
	f.camp.SetNow(func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }) // last campaign day

	if _, err := f.final.Run(f.ctx, 1, false); !errors.Is(err, service.ErrFinalNotOver) {
		t.Errorf("err = %v, want ErrFinalNotOver", err)
	}
	if _, err := f.final.Run(f.ctx, 1, true); err != nil {
		t.Errorf("force: %v", err)
	}
}

func TestFinalDrawIsHeldOnce(t *testing.T) {
	f := newFinalEnv(t, 3, 2)
	for i := 0; i < 9; i++ {
		f.holder(t, int64(100+i), i%5+1)
	}
	first, err := f.final.Run(f.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	f.holder(t, 999, 10) // a late holder cannot change the held draw
	second, err := f.final.Run(f.ctx, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already || first.Already || fmt.Sprint(second.Winners) != fmt.Sprint(first.Winners) ||
		second.Draw.HeldByTelegramID != 1 || !second.Draw.HeldAt.Time.Equal(first.Draw.HeldAt.Time) {
		t.Errorf("rerun changed the result:\n%+v\n%+v", first, second)
	}
	if n := f.count(t, `SELECT count(*) FROM final_draw`); n != 1 {
		t.Errorf("final_draw rows = %d", n)
	}
}

func TestFinalDrawConcurrentRuns(t *testing.T) {
	f := newFinalEnv(t, 3, 2)
	for i := 0; i < 20; i++ {
		f.holder(t, int64(100+i), i%7+1)
	}
	const n = 8
	results := make([]service.FinalResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.final.Run(f.ctx, int64(i+1), false)
		}()
	}
	wg.Wait()
	fresh := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if !r.Already {
			fresh++
		}
		if fmt.Sprint(r.Winners) != fmt.Sprint(results[0].Winners) {
			t.Errorf("run %d saw different winners", i)
		}
	}
	if fresh != 1 {
		t.Errorf("%d runs held the draw, want 1", fresh)
	}
	if c := f.count(t, `SELECT count(*) FROM final_winners`); c != 5 {
		t.Errorf("final_winners = %d, want 5", c)
	}
}

func TestFinalConfirmBeforeRun(t *testing.T) {
	f := newFinalEnv(t, 2, 1)
	if _, err := f.final.Confirm(f.ctx, 1); !errors.Is(err, service.ErrFinalNotRun) {
		t.Errorf("err = %v, want ErrFinalNotRun", err)
	}
	if _, err := f.final.Result(f.ctx); !errors.Is(err, service.ErrFinalNotRun) {
		t.Errorf("Result err = %v, want ErrFinalNotRun", err)
	}
}

func TestFinalConfirmNotifiesWinnersOnlyWithButton(t *testing.T) {
	f := newFinalEnv(t, 2, 2)
	for i := 0; i < 6; i++ {
		f.holder(t, int64(100+i), i+1)
	}
	run, err := f.final.Run(f.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if f.out.total() != 0 {
		t.Fatal("the draw itself must not notify anyone")
	}

	res, err := f.final.Confirm(f.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Winners != 2 || res.Sent != 2 || !res.Done || res.Draw.Status != "notified" {
		t.Fatalf("res = %+v", res)
	}
	for _, w := range run.Winners {
		msgs := f.out.sent[w.TelegramID]
		if w.Reserve {
			if len(msgs) != 0 {
				t.Errorf("reserve %d was notified", w.TelegramID)
			}
			continue
		}
		if len(msgs) != 1 || msgs[0].label != "Получить приз" || msgs[0].unique != service.UniqueGetPrize ||
			!strings.Contains(msgs[0].html, "главный приз") || !strings.Contains(msgs[0].html, "Количество билетов: 2") {
			t.Errorf("winner %d got %+v", w.TelegramID, msgs)
		}
	}
	// the response deadline is set for notified winners
	if n := f.count(t, `SELECT count(*) FROM final_winners WHERE status = 'notified' AND deadline > now() + interval '47 hours'`); n != 2 {
		t.Errorf("winners with a deadline = %d, want 2", n)
	}

	again, err := f.final.Confirm(f.ctx, 1)
	if err != nil || again.Sent != 0 || again.Already != 2 || f.out.total() != 2 {
		t.Errorf("repeat: %+v err %v, total sent %d", again, err, f.out.total())
	}
}

func TestFinalConfirmRetriesFailedDeliveries(t *testing.T) {
	f := newFinalEnv(t, 3, 0)
	for i := 0; i < 3; i++ {
		f.holder(t, int64(100+i), 1)
	}
	if _, err := f.final.Run(f.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	f.out.fail[101] = true
	res, err := f.final.Confirm(f.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || len(res.Failed) != 1 || res.Failed[0].TelegramID != 101 || res.Done || res.Draw.Status != "confirmed" {
		t.Fatalf("res = %+v", res)
	}
	if n := f.count(t, `SELECT count(*) FROM final_winners WHERE status = 'selected' AND deadline IS NULL`); n != 1 {
		t.Errorf("a failed delivery must be released (selected, no deadline), got %d", n)
	}

	delete(f.out.fail, 101)
	res, err = f.final.Confirm(f.ctx, 1)
	if err != nil || res.Sent != 1 || res.Already != 2 || !res.Done || res.Draw.Status != "notified" {
		t.Errorf("retry: %+v err %v", res, err)
	}
	for id, msgs := range f.out.sent {
		if len(msgs) != 1 {
			t.Errorf("user %d notified %d times", id, len(msgs))
		}
	}
}

func TestFinalConfirmConcurrentCallsNotifyOnce(t *testing.T) {
	f := newFinalEnv(t, 4, 2)
	for i := 0; i < 8; i++ {
		f.holder(t, int64(100+i), 1)
	}
	if _, err := f.final.Run(f.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.final.Confirm(f.ctx, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.out.total() != 4 {
		t.Errorf("sent %d notifications, want 4", f.out.total())
	}
	for id, msgs := range f.out.sent {
		if len(msgs) != 1 {
			t.Errorf("user %d notified %d times", id, len(msgs))
		}
	}
}

func TestFinalDrawCapsTicketsAtMaxChances(t *testing.T) {
	f := newFinalEnv(t, 1, 0)
	f.holder(t, 100, 10)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO campaign_days (day, partner_id, winners_count) VALUES ('2026-09-20', $1, 1)`, f.partner.ID); err != nil {
		t.Fatal(err)
	}
	var extra int64
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM campaign_days WHERE day = '2026-09-20'`).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	u, _ := f.store.GetUserByTelegramID(f.ctx, 100)
	if _, err := f.store.AddActivityDay(f.ctx, db.AddActivityDayParams{UserID: u.ID, CampaignDayID: extra}); err != nil {
		t.Fatal(err)
	}
	res, err := f.final.Run(f.ctx, 1, false)
	if err != nil || res.Winners[0].Tickets != 10 || res.Draw.DrumTickets != 10 {
		t.Errorf("res = %+v, err = %v; the 11th day must not become a ticket", res.Winners, err)
	}
}
