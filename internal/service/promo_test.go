package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"chat_bot/internal/service"
	"chat_bot/internal/storage"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/storage/storagetest"
)

type env struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	store   *storage.Store
	partner db.Partner
}

// newEnv creates an isolated database with one partner holding nCodes free codes.
func newEnv(t *testing.T, nCodes int) *env {
	t.Helper()
	pool := storagetest.New(t)
	e := &env{ctx: context.Background(), pool: pool, store: storage.NewStore(pool)}

	p, err := e.store.CreatePartner(e.ctx, db.CreatePartnerParams{Name: "Partner"})
	if err != nil {
		t.Fatal(err)
	}
	e.partner = p
	for i := 0; i < nCodes; i++ {
		if err := e.store.AddPromoCode(e.ctx, db.AddPromoCodeParams{PartnerID: p.ID, Code: fmt.Sprintf("CODE%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *env) day(t *testing.T, dayOfMonth int) *service.ActiveDay {
	t.Helper()
	d := pgtype.Date{Time: time.Date(2026, 9, dayOfMonth, 0, 0, 0, 0, time.UTC), Valid: true}
	cd, err := e.store.CreateCampaignDay(e.ctx, db.CreateCampaignDayParams{Day: d, PartnerID: e.partner.ID, WinnersCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	return &service.ActiveDay{Day: cd, Partner: e.partner}
}

func (e *env) user(t *testing.T, telegramID int64) int64 {
	t.Helper()
	u, err := e.store.UpsertUser(e.ctx, db.UpsertUserParams{TelegramID: telegramID})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPromoConcurrentIssue(t *testing.T) {
	const codes, users = 20, 50
	e := newEnv(t, codes)
	ad := e.day(t, 1)
	svc := service.NewPromo(e.store, 10)

	ids := make([]int64, users)
	for i := range ids {
		ids[i] = e.user(t, int64(1000+i))
	}

	type result struct {
		res service.IssuedPromo
		err error
	}
	results := make([]result, users)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i].res, results[i].err = svc.Issue(e.ctx, ids[i], ad)
		}()
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	var ok, soldOut int
	for i, r := range results {
		switch {
		case r.err == nil:
			ok++
			if r.res.Already {
				t.Errorf("user %d: first request must not be Already", i)
			}
			if seen[r.res.Code] {
				t.Errorf("code %s issued twice", r.res.Code)
			}
			seen[r.res.Code] = true
		case errors.Is(r.err, service.ErrNoPromoCodes):
			soldOut++
		default:
			t.Errorf("user %d: unexpected error: %v", i, r.err)
		}
	}
	if ok != codes || soldOut != users-codes {
		t.Fatalf("issued %d, sold out %d; want %d and %d", ok, soldOut, codes, users-codes)
	}

	if n := e.count(t, `SELECT count(*) FROM promo_codes WHERE assigned_to_user_id IS NOT NULL`); n != codes {
		t.Errorf("assigned codes = %d, want %d", n, codes)
	}
	if n := e.count(t, `SELECT count(*) FROM promo_issues`); n != codes {
		t.Errorf("promo_issues = %d, want %d", n, codes)
	}
	// Only users who got a code earn the active day.
	if n := e.count(t, `SELECT count(*) FROM activity_days`); n != codes {
		t.Errorf("activity_days = %d, want %d", n, codes)
	}
}

func TestPromoSameUserGetsSameCode(t *testing.T) {
	e := newEnv(t, 5)
	ad := e.day(t, 1)
	svc := service.NewPromo(e.store, 10)
	uid := e.user(t, 1)

	first, err := svc.Issue(e.ctx, uid, ad)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Issue(e.ctx, uid, ad)
	if err != nil {
		t.Fatal(err)
	}
	if first.Code != second.Code || first.Already || !second.Already {
		t.Errorf("first = %+v, second = %+v", first, second)
	}
	if n := e.count(t, `SELECT count(*) FROM promo_codes WHERE assigned_to_user_id IS NOT NULL`); n != 1 {
		t.Errorf("assigned codes = %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days`); n != 1 {
		t.Errorf("activity_days = %d, want 1", n)
	}
}

func TestPromoConcurrentRequestsOfOneUser(t *testing.T) {
	e := newEnv(t, 20)
	ad := e.day(t, 1)
	svc := service.NewPromo(e.store, 10)
	uid := e.user(t, 1)

	const n = 5
	codes := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var r service.IssuedPromo
			r, errs[i] = svc.Issue(e.ctx, uid, ad)
			codes[i] = r.Code
		}()
	}
	close(start)
	wg.Wait()

	for i := range codes {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if codes[i] != codes[0] {
			t.Errorf("request %d got %s, request 0 got %s", i, codes[i], codes[0])
		}
	}
	if got := e.count(t, `SELECT count(*) FROM promo_codes WHERE assigned_to_user_id IS NOT NULL`); got != 1 {
		t.Errorf("assigned codes = %d, want exactly 1 (the rest must return to the pool)", got)
	}
	if got := e.count(t, `SELECT count(*) FROM activity_days`); got != 1 {
		t.Errorf("activity_days = %d, want 1", got)
	}
}

func TestPromoNextDayGivesNewCode(t *testing.T) {
	e := newEnv(t, 5)
	day1, day2 := e.day(t, 1), e.day(t, 2)
	svc := service.NewPromo(e.store, 10)
	uid := e.user(t, 1)

	a, err := svc.Issue(e.ctx, uid, day1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Issue(e.ctx, uid, day2)
	if err != nil {
		t.Fatal(err)
	}
	if a.Code == b.Code || b.Already {
		t.Errorf("day 1 = %+v, day 2 = %+v; want different codes", a, b)
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days WHERE user_id = $1`, uid); n != 2 {
		t.Errorf("activity_days = %d, want 2", n)
	}
}

func TestPromoRespectsChanceCap(t *testing.T) {
	e := newEnv(t, 5)
	day1, day2 := e.day(t, 1), e.day(t, 2)
	svc := service.NewPromo(e.store, 1)
	uid := e.user(t, 1)

	for _, ad := range []*service.ActiveDay{day1, day2} {
		if _, err := svc.Issue(e.ctx, uid, ad); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM promo_issues WHERE user_id = $1`, uid); n != 2 {
		t.Errorf("promo_issues = %d, want 2 (codes are still issued)", n)
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days WHERE user_id = $1`, uid); n != 1 {
		t.Errorf("activity_days = %d, want 1 (cap)", n)
	}
}

func TestPromoSoldOutDoesNotCountDay(t *testing.T) {
	e := newEnv(t, 0)
	ad := e.day(t, 1)
	svc := service.NewPromo(e.store, 10)
	uid := e.user(t, 1)

	if _, err := svc.Issue(e.ctx, uid, ad); !errors.Is(err, service.ErrNoPromoCodes) {
		t.Fatalf("err = %v, want ErrNoPromoCodes", err)
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days`); n != 0 {
		t.Errorf("activity_days = %d, want 0", n)
	}
}
