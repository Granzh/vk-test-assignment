package service_test

import (
	"testing"

	"chat_bot/internal/service"
)

func TestChancesNoActivityIsZero(t *testing.T) {
	e := newEnv(t, 0)
	u := e.userRow(t, 1)

	v, err := service.NewChances(e.store, 10).View(e.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Chances != 0 || v.Max != 10 || len(v.Days) != 0 || len(v.Partners) != 0 {
		t.Errorf("view = %+v, want zero chances and empty lists", v)
	}
}

func TestChancesPromoAndEntryOnOneDayGiveOneChance(t *testing.T) {
	e := newEnv(t, 1)
	ad := withChat(e.day(t, 1))
	u := e.userRow(t, 1)
	if _, err := service.NewPromo(e.store, 10).Issue(e.ctx, u.ID, ad); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewEntry(e.store, &fakeChecker{subscribed: true}, 10).Join(e.ctx, u, ad); err != nil {
		t.Fatal(err)
	}

	v, err := service.NewChances(e.store, 10).View(e.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Chances != 1 || len(v.Days) != 1 {
		t.Errorf("chances = %d, days = %d; want 1 and 1", v.Chances, len(v.Days))
	}
	if len(v.Partners) != 1 || !v.Partners[0].Promo || !v.Partners[0].Entry {
		t.Errorf("partners = %+v, want one partner with promo and entry", v.Partners)
	}
}

func TestChancesEntryOrderDoesNotMatter(t *testing.T) {
	e := newEnv(t, 1)
	ad := withChat(e.day(t, 1))
	u := e.userRow(t, 1)
	if _, err := service.NewEntry(e.store, &fakeChecker{subscribed: true}, 10).Join(e.ctx, u, ad); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewPromo(e.store, 10).Issue(e.ctx, u.ID, ad); err != nil {
		t.Fatal(err)
	}
	v, err := service.NewChances(e.store, 10).View(e.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Chances != 1 {
		t.Errorf("chances = %d, want 1", v.Chances)
	}
}

func TestChancesCapAtMax(t *testing.T) {
	const days, max = 12, 10
	e := newEnv(t, days)
	u := e.userRow(t, 1)
	promo := service.NewPromo(e.store, max)
	for d := 1; d <= days; d++ {
		if _, err := promo.Issue(e.ctx, u.ID, e.day(t, d)); err != nil {
			t.Fatal(err)
		}
	}

	v, err := service.NewChances(e.store, max).View(e.ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Chances != max || len(v.Days) != max {
		t.Errorf("chances = %d, counted days = %d; want %d", v.Chances, len(v.Days), max)
	}
	// Partner interactions beyond the cap are still reported.
	if len(v.Partners) != 1 || !v.Partners[0].Promo {
		t.Errorf("partners = %+v", v.Partners)
	}
	if n := e.count(t, `SELECT count(*) FROM activity_days WHERE user_id = $1`, u.ID); n != max {
		t.Errorf("activity_days = %d, want %d", n, max)
	}
}

func TestChancesIsolatedPerUserAndOrdered(t *testing.T) {
	e := newEnv(t, 5)
	d1, d2 := e.day(t, 1), e.day(t, 2)
	a, b := e.userRow(t, 1), e.userRow(t, 2)
	promo := service.NewPromo(e.store, 10)
	for _, ad := range []*service.ActiveDay{d2, d1} {
		if _, err := promo.Issue(e.ctx, a.ID, ad); err != nil {
			t.Fatal(err)
		}
	}

	v, err := service.NewChances(e.store, 10).View(e.ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Chances != 2 || !v.Days[0].Day.Before(v.Days[1].Day) {
		t.Errorf("view = %+v, want 2 chances, days oldest first", v)
	}
	other, err := service.NewChances(e.store, 10).View(e.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Chances != 0 {
		t.Errorf("other user chances = %d, want 0", other.Chances)
	}
}
