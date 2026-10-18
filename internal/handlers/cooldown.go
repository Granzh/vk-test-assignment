package handlers

import (
	"sync"
	"time"
)

// cooldown allows one action per key per interval, to keep users from
// flooding the Telegram API with subscription checks.
type cooldown struct {
	mu       sync.Mutex
	interval time.Duration
	last     map[int64]time.Time
	now      func() time.Time
}

func newCooldown(interval time.Duration) *cooldown {
	return &cooldown{interval: interval, last: map[int64]time.Time{}, now: time.Now}
}

// Allow reports whether key may act now and, if so, starts its cooldown.
func (c *cooldown) Allow(key int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if t, ok := c.last[key]; ok && now.Sub(t) < c.interval {
		return false
	}
	if len(c.last) >= 1024 { // drop expired entries so the map cannot grow unbounded
		for k, t := range c.last {
			if now.Sub(t) >= c.interval {
				delete(c.last, k)
			}
		}
	}
	c.last[key] = now
	return true
}
