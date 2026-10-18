package handlers

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	tele "gopkg.in/telebot.v4"

	"chat_bot/internal/texts"
)

// Metrics is what the middleware reports about itself (implemented by
// obs.Metrics; nil means "do not report").
type Metrics interface {
	Duplicate()
	Panic()
}

type noopMetrics struct{}

func (noopMetrics) Duplicate() {}
func (noopMetrics) Panic()     {}

// Router is what Register needs from the bot: middleware and routes. *tele.Bot
// and the instrumented router of package obs satisfy it.
type Router interface {
	Use(middleware ...tele.MiddlewareFunc)
	Handle(endpoint interface{}, h tele.HandlerFunc, m ...tele.MiddlewareFunc)
}

// ErrOverloaded is returned for an update that could not get a processing slot
// in time.
var ErrOverloaded = errors.New("too many updates in flight")

// dedupTTL is how long an update id is remembered. Telegram redelivers an
// update when the previous attempt was not acknowledged in time (webhook
// retries, a restart during polling); the business operations are idempotent
// anyway, this only avoids doing the work and sending the replies twice.
const dedupTTL = 10 * time.Minute

// maxDedupEntries bounds the memory of the dedup table.
const maxDedupEntries = 100_000

// dedup remembers recently seen update ids.
type dedup struct {
	mu   sync.Mutex
	seen map[int]time.Time
	now  func() time.Time
}

func newDedup() *dedup { return &dedup{seen: map[int]time.Time{}, now: time.Now} }

// First reports whether id is seen for the first time (and remembers it).
func (d *dedup) First(id int) bool {
	if id == 0 {
		return true // not an update from Telegram (tests, synthetic contexts)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if t, ok := d.seen[id]; ok && now.Sub(t) < dedupTTL {
		return false
	}
	if len(d.seen) >= maxDedupEntries {
		for k, t := range d.seen {
			if now.Sub(t) >= dedupTTL {
				delete(d.seen, k)
			}
		}
	}
	d.seen[id] = now
	return true
}

// dedupMW drops updates whose id has already been handled.
func dedupMW(d *dedup, log *slog.Logger, m Metrics) tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			if id := c.Update().ID; !d.First(id) {
				m.Duplicate()
				log.Info("duplicate update dropped", "update_id", id)
				return nil
			}
			return next(c)
		}
	}
}

// tellBusy answers a dropped update on a best-effort basis: a button press gets
// a short pop-up (which also stops the spinner), a message gets a reply.
func tellBusy(c tele.Context) {
	if c.Callback() != nil {
		_ = c.Respond(&tele.CallbackResponse{Text: texts.Busy})
		return
	}
	_ = c.Send(texts.Busy)
}

// limitMW lets at most n updates run at once. The rest wait for a slot for up
// to wait; after that the update is dropped with ErrOverloaded rather than
// piling up goroutines (and database connection requests) without bound.
func limitMW(n int, wait time.Duration) tele.MiddlewareFunc {
	if n <= 0 {
		return func(next tele.HandlerFunc) tele.HandlerFunc { return next }
	}
	sem := make(chan struct{}, n)
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			select {
			case sem <- struct{}{}:
			default:
				t := time.NewTimer(wait)
				defer t.Stop()
				select {
				case sem <- struct{}{}:
				case <-t.C:
					tellBusy(c)
					return ErrOverloaded
				}
			}
			defer func() { <-sem }()
			return next(c)
		}
	}
}
