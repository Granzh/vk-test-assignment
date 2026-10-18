package service

import (
	"context"
	"fmt"
	"time"

	"chat_bot/internal/storage/db"
)

// DayCount is a number of events on a campaign day.
type DayCount struct {
	Day   time.Time // campaign date
	Count int
}

// PartnerStock is the promo code stock of a partner.
type PartnerStock struct {
	ID     int64
	Name   string
	Total  int
	Free   int
	Issued int
}

// DrawStat is one assigned campaign day with its draw and promo numbers.
type DrawStat struct {
	Day             time.Time
	Partner         string
	Status          string
	Entries         int // registrations in the daily draw
	Excluded        int // dropped because they unsubscribed before the draw
	PromoRequesters int // users who asked for the day's promo code
	PromoIssued     int // codes given that day
}

// ChanceBucket says how many users have exactly Chances chances.
type ChanceBucket struct {
	Chances int
	Users   int
}

// StatsReport is the analytics of spec section 8.
type StatsReport struct {
	GeneratedAt time.Time

	TotalUsers   int // everyone who started the bot
	BlockedUsers int // of them, blocked the bot

	NewUsers []DayCount // by campaign day, oldest first

	PromoRequests   int // all presses of the promo button
	PromoRequesters int // distinct (user, day) pairs that asked
	PromoGotCode    int // of them, got a code
	PromoNoCode     int // of them, found the pool empty
	PromoIssued     int // codes issued in total

	Partners []PartnerStock
	Days     []DrawStat

	MaxChances   int
	Distribution []ChanceBucket // 0..MaxChances, zeros included
	FullChances  int            // users with the maximum number of chances
}

// Stats builds the analytics report. Every query is an aggregate that uses an
// index or one pass over its table; see docs/stats.md for the plans.
type Stats struct {
	tx         TxRunner
	loc        *time.Location
	boundary   time.Duration
	maxChances int
	now        func() time.Time
}

// NewStats builds the service. loc and boundary define the campaign day (as in
// domain.Calendar); maxChances is the chance cap.
func NewStats(tx TxRunner, loc *time.Location, boundary time.Duration, maxChances int) *Stats {
	return &Stats{tx: tx, loc: loc, boundary: boundary, maxChances: maxChances, now: time.Now}
}

// Report collects the numbers in one read transaction.
func (s *Stats) Report(ctx context.Context) (StatsReport, error) {
	rep := StatsReport{GeneratedAt: s.now(), MaxChances: s.maxChances}
	err := s.tx.InTx(ctx, func(q *db.Queries) error {
		users, err := q.StatsUsers(ctx)
		if err != nil {
			return fmt.Errorf("stats users: %w", err)
		}
		rep.TotalUsers, rep.BlockedUsers = int(users.Total), int(users.Blocked)

		byDay, err := q.StatsNewUsersByDay(ctx, db.StatsNewUsersByDayParams{
			Tz: s.loc.String(), BoundaryMinutes: int32(s.boundary / time.Minute),
		})
		if err != nil {
			return fmt.Errorf("stats new users: %w", err)
		}
		for _, r := range byDay {
			rep.NewUsers = append(rep.NewUsers, DayCount{Day: r.Day.Time, Count: int(r.Users)})
		}

		pr, err := q.StatsPromoRequests(ctx)
		if err != nil {
			return fmt.Errorf("stats promo requests: %w", err)
		}
		rep.PromoRequests, rep.PromoRequesters = int(pr.Requests), int(pr.Requesters)
		rep.PromoGotCode, rep.PromoNoCode = int(pr.GotCode), int(pr.NoCode)
		issued, err := q.StatsIssuedPromoCodes(ctx)
		if err != nil {
			return fmt.Errorf("stats issued codes: %w", err)
		}
		rep.PromoIssued = int(issued)

		stock, err := q.ListPromoStats(ctx)
		if err != nil {
			return fmt.Errorf("stats promo stock: %w", err)
		}
		for _, r := range stock {
			rep.Partners = append(rep.Partners, PartnerStock{
				ID: r.ID, Name: r.Name, Total: int(r.Total), Free: int(r.Free), Issued: int(r.Total - r.Free),
			})
		}

		days, err := q.StatsDays(ctx)
		if err != nil {
			return fmt.Errorf("stats days: %w", err)
		}
		for _, r := range days {
			rep.Days = append(rep.Days, DrawStat{
				Day: r.Day.Time, Partner: r.PartnerName, Status: r.Status, Entries: int(r.Entries), Excluded: int(r.Excluded),
				PromoRequesters: int(r.PromoRequesters), PromoIssued: int(r.PromoIssued),
			})
		}

		dist, err := q.StatsChanceDistribution(ctx)
		if err != nil {
			return fmt.Errorf("stats chances: %w", err)
		}
		rep.Distribution, rep.FullChances = buckets(dist, rep.TotalUsers, s.maxChances)
		return nil
	})
	return rep, err
}

// buckets turns the counts of users per number of chances into a distribution
// 0..maxChances. Users without activity are everyone not counted in a bucket.
func buckets(rows []db.StatsChanceDistributionRow, totalUsers, maxChances int) (dist []ChanceBucket, full int) {
	dist = make([]ChanceBucket, maxChances+1)
	for i := range dist {
		dist[i].Chances = i
	}
	withChances := 0
	for _, r := range rows {
		n := min(int(r.Chances), maxChances) // the cap is enforced on write; clamp anyway
		dist[n].Users += int(r.Users)
		withChances += int(r.Users)
	}
	dist[0].Users = max(totalUsers-withChances, 0)
	return dist, dist[maxChances].Users
}
