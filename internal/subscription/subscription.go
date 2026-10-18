// Package subscription checks channel subscriptions through the Telegram Bot API.
//
// Telegram limits how fast a bot may call its API (about 30 requests a second
// for all methods together), and a promotion can make thousands of people press
// "check subscription" at the same moment. So the checker is built from layers:
//
//   - Checker calls getChatMember through a rate limiter that spreads the
//     calls over time, and waits out Telegram's own 429 answers;
//   - Cached remembers positive answers for a short time and merges
//     concurrent checks of the same user into a single call.
package subscription

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/secret"
)

// Observer receives measurements; it may be nil.
type Observer interface {
	// CheckDone reports a finished check: result is subscribed, not_subscribed,
	// error or cache_hit, d the time the Telegram call took (0 for cache hits).
	CheckDone(result string, d time.Duration)
	// RateLimited reports a 429 answer from Telegram.
	RateLimited(source string)
}

type noopObserver struct{}

func (noopObserver) CheckDone(string, time.Duration) {}
func (noopObserver) RateLimited(string)              {}

// MemberLookup is the part of *tele.Bot the checker uses (tests and load tests
// substitute their own).

type MemberLookup interface {
	ChatMemberOf(chat, user tele.Recipient) (*tele.ChatMember, error)
}

// Options tune a Checker.
type Options struct {
	// Rate is the most getChatMember calls per second; 0 means unlimited.
	Rate int
	// Burst is how many calls may go out at once after a pause; default Rate/5, at least 1.
	Burst int
	// MaxRetries is how many times a 429 answer is waited out and retried; default 2.
	MaxRetries int
	// Observer receives metrics; may be nil.
	Observer Observer
}

// Checker implements service.SubscriptionChecker with getChatMember.
// The bot must be an administrator of the checked channel or group.
type Checker struct {
	bot        MemberLookup
	lim        *limiter
	maxRetries int
	obs        Observer
	now        func() time.Time
	sleep      func(ctx context.Context, d time.Duration) error
}

// NewChecker builds a checker on top of the bot.
func NewChecker(bot *tele.Bot, opts Options) *Checker { return newChecker(bot, opts) }

// NewCheckerWith builds a checker on top of any MemberLookup.
func NewCheckerWith(bot MemberLookup, opts Options) *Checker { return newChecker(bot, opts) }

func newChecker(bot MemberLookup, opts Options) *Checker {
	c := &Checker{bot: bot, maxRetries: opts.MaxRetries, obs: opts.Observer, now: time.Now, sleep: sleepCtx}
	if c.maxRetries == 0 {
		c.maxRetries = 2
	}
	if c.obs == nil {
		c.obs = noopObserver{}
	}
	if opts.Rate > 0 {
		burst := opts.Burst
		if burst <= 0 {
			burst = max(opts.Rate/5, 1)
		}
		c.lim = &limiter{interval: time.Second / time.Duration(opts.Rate), burst: burst, now: c.now, sleep: c.sleep}
	}
	return c
}

// IsSubscribed reports whether the user is a member, administrator or creator
// of the chat. Telegram API errors (bot not an admin, chat not found, rate
// limit) are returned as errors, never as "not subscribed".
func (c *Checker) IsSubscribed(ctx context.Context, chatID, telegramUserID int64) (bool, error) {
	start := c.now()
	ok, err := c.lookup(ctx, chatID, telegramUserID)
	d := c.now().Sub(start)
	switch {
	case err != nil:
		c.obs.CheckDone("error", d)
	case ok:
		c.obs.CheckDone("subscribed", d)
	default:
		c.obs.CheckDone("not_subscribed", d)
	}
	return ok, err
}

func (c *Checker) lookup(ctx context.Context, chatID, userID int64) (bool, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if c.lim != nil {
			if err := c.lim.wait(ctx); err != nil {
				return false, fmt.Errorf("wait for a free Telegram request slot: %w", err)
			}
		}
		m, err := c.bot.ChatMemberOf(&tele.Chat{ID: chatID}, &tele.User{ID: userID})
		if err == nil {
			return subscribed(m.Role), nil
		}

		var flood tele.FloodError
		if errors.As(err, &flood) && attempt < c.maxRetries {
			c.obs.RateLimited("getChatMember")
			wait := time.Duration(flood.RetryAfter) * time.Second
			if dl, ok := ctx.Deadline(); ok && c.now().Add(wait).After(dl) {
				return false, fmt.Errorf("get chat member (chat %d): Telegram asked to wait %s: %w", chatID, wait, err)
			}
			if sleepErr := c.sleep(ctx, wait); sleepErr != nil {
				return false, sleepErr
			}
			continue
		}
		if errors.As(err, &flood) {
			c.obs.RateLimited("getChatMember")
		}
		return false, secret.Wrap(fmt.Errorf("get chat member (chat %d): %w", chatID, err))
	}
}

func subscribed(role tele.MemberStatus) bool {
	switch role {
	case tele.Member, tele.Administrator, tele.Creator:
		return true
	}
	return false
}

// --- rate limiter ---

// limiter hands out request slots at a fixed rate. Callers reserve the next
// free slot under a lock and sleep until it comes, so waiters are served in
// order and do not wake up in herds.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	burst    int
	next     time.Time
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
}

// wait blocks until a slot is free. It fails at once, without taking a slot,
// when the slot would come after the deadline of ctx: the caller could not use
// it anyway and the answer "try later" is better than a timeout.
func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := l.now()
	if earliest := now.Add(-time.Duration(l.burst-1) * l.interval); l.next.Before(earliest) {
		l.next = earliest
	}
	slot := l.next
	if dl, ok := ctx.Deadline(); ok && slot.After(dl) {
		l.mu.Unlock()
		return context.DeadlineExceeded
	}
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if d := slot.Sub(now); d > 0 {
		return l.sleep(ctx, d)
	}
	return ctx.Err()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// --- cache ---

// Inner is a checker that Cached wraps (satisfied by *Checker).
type Inner interface {
	IsSubscribed(ctx context.Context, chatID, telegramUserID int64) (bool, error)
}

// Cached wraps a checker with a short-lived cache and request merging.
//
//   - A positive answer is reused for ttl. A negative answer or an error is
//     never cached: a user who has just subscribed must be recognized at once.
//   - Concurrent checks of the same (chat, user) share one call to Telegram.
//     The shared call is detached from the context of the first caller, so one
//     impatient user cannot fail the others.
type Cached struct {
	inner Inner
	ttl   time.Duration
	obs   Observer
	now   func() time.Time

	mu       sync.Mutex
	entries  map[key]time.Time // key -> expiry of a positive answer
	inflight map[key]*call
}

type key struct{ chat, user int64 }

type call struct {
	done chan struct{}
	ok   bool
	err  error
}

// maxCacheEntries bounds the memory; expired entries are dropped first.
const maxCacheEntries = 100_000

// sharedCallTimeout bounds the Telegram call shared by merged requests.
const sharedCallTimeout = 20 * time.Second

// NewCached wraps inner. A ttl of 0 disables the cache but keeps the merging.
func NewCached(inner Inner, ttl time.Duration, obs Observer) *Cached {
	if obs == nil {
		obs = noopObserver{}
	}
	return &Cached{inner: inner, ttl: ttl, obs: obs, now: time.Now, entries: map[key]time.Time{}, inflight: map[key]*call{}}
}

// IsSubscribed implements service.SubscriptionChecker.
func (c *Cached) IsSubscribed(ctx context.Context, chatID, telegramUserID int64) (bool, error) {
	k := key{chatID, telegramUserID}
	c.mu.Lock()
	if exp, ok := c.entries[k]; ok {
		if c.now().Before(exp) {
			c.mu.Unlock()
			c.obs.CheckDone("cache_hit", 0)
			return true, nil
		}
		delete(c.entries, k)
	}
	cl, joined := c.inflight[k]
	if !joined {
		cl = &call{done: make(chan struct{})}
		c.inflight[k] = cl
		go c.run(k, cl)
	}
	c.mu.Unlock()

	select {
	case <-cl.done:
		return cl.ok, cl.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (c *Cached) run(k key, cl *call) {
	ctx, cancel := context.WithTimeout(context.Background(), sharedCallTimeout)
	defer cancel()
	cl.ok, cl.err = c.inner.IsSubscribed(ctx, k.chat, k.user)

	c.mu.Lock()
	delete(c.inflight, k)
	if cl.err == nil && cl.ok && c.ttl > 0 {
		if len(c.entries) >= maxCacheEntries {
			c.pruneLocked()
		}
		if len(c.entries) < maxCacheEntries {
			c.entries[k] = c.now().Add(c.ttl)
		}
	}
	c.mu.Unlock()
	close(cl.done)
}

func (c *Cached) pruneLocked() {
	now := c.now()
	for k, exp := range c.entries {
		if !now.Before(exp) {
			delete(c.entries, k)
		}
	}
}
