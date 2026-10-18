package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/domain"
	"chat_bot/internal/storage/db"
)

var (
	// ErrCampaignNotStarted means today is before CAMPAIGN_START.
	ErrCampaignNotStarted = errors.New("campaign has not started")
	// ErrCampaignFinished means today is after CAMPAIGN_END.
	ErrCampaignFinished = errors.New("campaign has finished")
	// ErrNoCampaignDay means the campaign is running but no partner is set for today.
	ErrNoCampaignDay = errors.New("no campaign day for today")
)

// CampaignStore is the part of the storage layer the campaign service needs.
type CampaignStore interface {
	GetCampaignDayByDate(ctx context.Context, day pgtype.Date) (db.CampaignDay, error)
	GetPartner(ctx context.Context, id int64) (db.Partner, error)
}

// ActiveDay is today's campaign day together with its partner.
type ActiveDay struct {
	Day     db.CampaignDay
	Partner db.Partner
}

// Campaign resolves what is going on today.
type Campaign struct {
	store CampaignStore
	cal   domain.Calendar
	start time.Time // first campaign day, as a date
	end   time.Time // last campaign day, as a date
	now   func() time.Time

	// A short-lived copy of today's day and partner: every press of every button
	// needs them, and they change only when an admin edits the day.
	cacheTTL  time.Duration
	mu        sync.Mutex
	cached    *ActiveDay
	cachedAt  time.Time
	cachedFor time.Time // the campaign date the cached day belongs to
}

// NewCampaign builds the service. start and end are the first and last
// campaign days as dates (midnight UTC).
func NewCampaign(store CampaignStore, cal domain.Calendar, start, end time.Time) *Campaign {
	return &Campaign{store: store, cal: cal, start: start, end: end, now: time.Now}
}

// WithCache makes Active reuse its answer for ttl. Admin changes of today's day
// (/day_set, /partner_edit) reach users within ttl; 0 turns the cache off.
func (c *Campaign) WithCache(ttl time.Duration) *Campaign {
	c.cacheTTL = ttl
	return c
}

// Start returns the first campaign day.
func (c *Campaign) Start() time.Time { return c.start }

// Today returns the current campaign day as a date.
func (c *Campaign) Today() time.Time { return c.cal.DayOf(c.now()) }

// Active returns today's campaign day and partner. It returns
// ErrCampaignNotStarted, ErrCampaignFinished or ErrNoCampaignDay when there
// is nothing to participate in.
func (c *Campaign) Active(ctx context.Context) (*ActiveDay, error) {
	today := c.Today()
	switch {
	case today.Before(c.start):
		return nil, ErrCampaignNotStarted
	case today.After(c.end):
		return nil, ErrCampaignFinished
	}

	if ad, ok := c.fromCache(today); ok {
		return ad, nil
	}

	day, err := c.store.GetCampaignDayByDate(ctx, pgtype.Date{Time: today, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoCampaignDay
	}
	if err != nil {
		return nil, fmt.Errorf("get campaign day %s: %w", today.Format(time.DateOnly), err)
	}
	partner, err := c.store.GetPartner(ctx, day.PartnerID)
	if err != nil {
		return nil, fmt.Errorf("get partner %d: %w", day.PartnerID, err)
	}
	ad := &ActiveDay{Day: day, Partner: partner}
	c.toCache(today, ad)
	return ad, nil
}

// fromCache returns a copy of the cached day if it is for today and fresh.
func (c *Campaign) fromCache(today time.Time) (*ActiveDay, bool) {
	if c.cacheTTL <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached == nil || !c.cachedFor.Equal(today) || c.now().Sub(c.cachedAt) >= c.cacheTTL {
		return nil, false
	}
	cp := *c.cached // callers may modify their copy
	return &cp, true
}

func (c *Campaign) toCache(today time.Time, ad *ActiveDay) {
	if c.cacheTTL <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := *ad
	c.cached, c.cachedAt, c.cachedFor = &cp, c.now(), today
}

// DaysLeft returns how many campaign days remain, today included: the whole
// campaign before the start, zero after the end.
func (c *Campaign) DaysLeft() int {
	from := c.Today()
	if from.Before(c.start) {
		from = c.start
	}
	if from.After(c.end) {
		return 0
	}
	return int(c.end.Sub(from)/(24*time.Hour)) + 1
}
