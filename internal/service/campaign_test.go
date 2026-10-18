package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/domain"
	"chat_bot/internal/storage/db"
)

type fakeStore struct {
	days     map[time.Time]db.CampaignDay
	partners map[int64]db.Partner
	err      error
}

func (f fakeStore) GetCampaignDayByDate(_ context.Context, d pgtype.Date) (db.CampaignDay, error) {
	if f.err != nil {
		return db.CampaignDay{}, f.err
	}
	day, ok := f.days[d.Time]
	if !ok {
		return db.CampaignDay{}, pgx.ErrNoRows
	}
	return day, nil
}

func (f fakeStore) GetPartner(_ context.Context, id int64) (db.Partner, error) {
	p, ok := f.partners[id]
	if !ok {
		return db.Partner{}, pgx.ErrNoRows
	}
	return p, nil
}

func TestCampaignActive(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	d := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	store := fakeStore{
		days:     map[time.Time]db.CampaignDay{d(2): {ID: 7, PartnerID: 3}},
		partners: map[int64]db.Partner{3: {ID: 3, Name: "P"}},
	}
	svc := NewCampaign(store, domain.Calendar{Location: msk}, d(1), d(3))

	tests := []struct {
		name    string
		now     time.Time
		wantErr error
		wantDay int64
	}{
		{"before start", time.Date(2026, 8, 31, 23, 59, 0, 0, msk), ErrCampaignNotStarted, 0},
		{"first day without partner", time.Date(2026, 9, 1, 0, 0, 0, 0, msk), ErrNoCampaignDay, 0},
		{"day with partner", time.Date(2026, 9, 2, 12, 0, 0, 0, msk), nil, 7},
		{"last day without partner", time.Date(2026, 9, 3, 23, 59, 59, 0, msk), ErrNoCampaignDay, 0},
		{"after end", time.Date(2026, 9, 4, 0, 0, 0, 0, msk), ErrCampaignFinished, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc.now = func() time.Time { return tt.now }
			got, err := svc.Active(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (got.Day.ID != tt.wantDay || got.Partner.Name != "P") {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestCampaignActiveStoreError(t *testing.T) {
	boom := errors.New("boom")
	svc := NewCampaign(fakeStore{err: boom}, domain.Calendar{Location: time.UTC},
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := svc.Active(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
}

func TestCampaignDaysLeft(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name string
		now  time.Time
		want int
	}{
		{"before start: whole campaign", d(1).AddDate(0, 0, -5), 10},
		{"first day", d(1), 10},
		{"middle, today counts", d(4), 7},
		{"last day", d(10), 1},
		{"after end", d(11), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCampaign(fakeStore{}, domain.Calendar{Location: time.UTC}, d(1), d(10))
			c.now = func() time.Time { return tt.now.Add(12 * time.Hour) }
			if got := c.DaysLeft(); got != tt.want {
				t.Errorf("DaysLeft = %d, want %d", got, tt.want)
			}
		})
	}
}

type countingStore struct {
	fakeStore
	dayCalls, partnerCalls int
}

func (c *countingStore) GetCampaignDayByDate(ctx context.Context, d pgtype.Date) (db.CampaignDay, error) {
	c.dayCalls++
	return c.fakeStore.GetCampaignDayByDate(ctx, d)
}

func (c *countingStore) GetPartner(ctx context.Context, id int64) (db.Partner, error) {
	c.partnerCalls++
	return c.fakeStore.GetPartner(ctx, id)
}

func TestCampaignActiveCache(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	store := &countingStore{fakeStore: fakeStore{
		days:     map[time.Time]db.CampaignDay{d(2): {ID: 7, PartnerID: 3}, d(3): {ID: 8, PartnerID: 3}},
		partners: map[int64]db.Partner{3: {ID: 3, Name: "P"}},
	}}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	c := NewCampaign(store, domain.Calendar{Location: time.UTC}, d(1), d(5)).WithCache(5 * time.Second)
	c.now = func() time.Time { return now }
	ctx := context.Background()

	first, err := c.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first.Partner.Name = "changed by a caller" // must not leak into the cache
	second, err := c.Active(ctx)
	if err != nil || second.Partner.Name != "P" || second.Day.ID != 7 {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if store.dayCalls != 1 || store.partnerCalls != 1 {
		t.Errorf("queries: day %d, partner %d; want one of each for two calls", store.dayCalls, store.partnerCalls)
	}

	now = now.Add(6 * time.Second) // the cache expires
	if _, err := c.Active(ctx); err != nil || store.dayCalls != 2 {
		t.Errorf("after the TTL: day queries %d, err %v", store.dayCalls, err)
	}

	now = d(3).Add(time.Hour) // the next campaign day: yesterday's entry must not be served
	third, err := c.Active(ctx)
	if err != nil || third.Day.ID != 8 {
		t.Errorf("next day: %+v, %v", third, err)
	}

	// without WithCache every call asks the store
	plain := NewCampaign(store, domain.Calendar{Location: time.UTC}, d(1), d(5))
	plain.now = func() time.Time { return now }
	before := store.dayCalls
	_, _ = plain.Active(ctx)
	_, _ = plain.Active(ctx)
	if store.dayCalls != before+2 {
		t.Errorf("an uncached campaign made %d queries for 2 calls", store.dayCalls-before)
	}
}

func TestCampaignActiveDoesNotCacheErrors(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	store := &countingStore{fakeStore: fakeStore{days: map[time.Time]db.CampaignDay{}, partners: map[int64]db.Partner{3: {ID: 3}}}}
	c := NewCampaign(store, domain.Calendar{Location: time.UTC}, d(1), d(5)).WithCache(time.Minute)
	c.now = func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }

	if _, err := c.Active(context.Background()); !errors.Is(err, ErrNoCampaignDay) {
		t.Fatalf("err = %v", err)
	}
	store.days[d(2)] = db.CampaignDay{ID: 9, PartnerID: 3} // an admin assigns the day
	if ad, err := c.Active(context.Background()); err != nil || ad.Day.ID != 9 {
		t.Errorf("after /day_set: %+v, %v; a missing day must not be remembered", ad, err)
	}
}
