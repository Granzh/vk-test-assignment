package service

import (
	"context"
	"time"
)

// SetNow replaces the clock of the campaign (tests only).
func (c *Campaign) SetNow(now func() time.Time) { c.now = now }

// SetIntn replaces the random source of the draw (tests only).
func (d *DailyDraw) SetIntn(f func(n int) (int, error)) { d.intn = f }

// SetIntn replaces the random source of the final draw (tests only).
func (f *FinalDraw) SetIntn(fn func(n int) (int, error)) { f.intn = fn }

// PickByTickets exposes the ticket drawing for tests.
var PickByTickets = pickByTickets

// CryptoIntn exposes the production random source for tests.
var CryptoIntn = cryptoIntn

// SetClock replaces the clock and sleep of the broadcast worker (tests only).
func (b *Broadcast) SetClock(now func() time.Time, sleep func(context.Context, time.Duration) error) {
	b.now, b.sleep = now, sleep
}
