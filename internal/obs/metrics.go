package obs

import (
	"runtime"
	"strings"
	"time"

	tele "gopkg.in/telebot.v4"
)

// Metrics are the bot's own metrics. Methods on it implement the small
// observer interfaces other packages define, so those packages never import obs.
type Metrics struct {
	Reg *Registry

	updates        *CounterVec
	updateDuration *HistogramVec
	duplicates     *CounterVec
	panics         *CounterVec
	inFlight       *Gauge
	subChecks      *CounterVec
	subDuration    *HistogramVec
	rateLimited    *CounterVec
	broadcast      *CounterVec
}

// NewMetrics registers the bot's metrics together with Go runtime gauges.
func NewMetrics() *Metrics {
	r := NewRegistry()
	m := &Metrics{
		Reg: r,
		updates: r.NewCounterVec("bot_updates_total",
			"Handled Telegram updates by handler and outcome (ok or error).", "handler", "status"),
		updateDuration: r.NewHistogramVec("bot_update_duration_seconds",
			"Time spent in a handler.", DefBuckets, "handler"),
		duplicates: r.NewCounterVec("bot_updates_duplicate_total",
			"Updates dropped because the same update id was already handled (Telegram redelivery)."),
		panics:   r.NewCounterVec("bot_panics_total", "Panics recovered in handlers."),
		inFlight: r.NewGauge("bot_updates_in_flight", "Updates being handled right now."),
		subChecks: r.NewCounterVec("bot_subscription_checks_total",
			"Subscription checks by result: subscribed, not_subscribed, error, cache_hit.", "result"),
		subDuration: r.NewHistogramVec("bot_subscription_check_duration_seconds",
			"Latency of a getChatMember call to Telegram (cache hits excluded).", DefBuckets, "result"),
		rateLimited: r.NewCounterVec("bot_telegram_rate_limited_total",
			"Answers 429 Too Many Requests from Telegram.", "source"),
		broadcast: r.NewCounterVec("bot_broadcast_messages_total",
			"Broadcast delivery attempts by result: sent, blocked, retry, failed, rate_limited.", "result"),
	}

	start := time.Now()
	r.NewGaugeFunc("process_start_time_seconds", "Start time of the process, Unix seconds.",
		func() float64 { return float64(start.Unix()) })
	r.NewGaugeFunc("go_goroutines", "Number of goroutines.", func() float64 { return float64(runtime.NumGoroutine()) })
	r.NewGaugeFunc("go_memstats_alloc_bytes", "Bytes of allocated heap objects.", func() float64 {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return float64(ms.Alloc)
	})
	return m
}

// PoolStat is a snapshot of the database connection pool.
type PoolStat struct {
	Acquired, Idle, Total, Max int32
	WaitCount                  int64
}

// RegisterPool exposes the connection pool as gauges.
func (m *Metrics) RegisterPool(stat func() PoolStat) {
	m.Reg.NewGaugeFunc("bot_db_pool_acquired_conns", "Connections in use.", func() float64 { return float64(stat().Acquired) })
	m.Reg.NewGaugeFunc("bot_db_pool_idle_conns", "Idle connections.", func() float64 { return float64(stat().Idle) })
	m.Reg.NewGaugeFunc("bot_db_pool_total_conns", "Open connections.", func() float64 { return float64(stat().Total) })
	m.Reg.NewGaugeFunc("bot_db_pool_max_conns", "Pool size limit.", func() float64 { return float64(stat().Max) })
	m.Reg.NewGaugeFunc("bot_db_pool_wait_total", "Acquires that had to wait for a free connection.",
		func() float64 { return float64(stat().WaitCount) })
}

// Duplicate counts a dropped duplicate update (handlers.Metrics).
func (m *Metrics) Duplicate() { m.duplicates.Inc() }

// Panic counts a recovered panic (handlers.Metrics).
func (m *Metrics) Panic() { m.panics.Inc() }

// CheckDone records a subscription check (subscription.Observer). Cache hits
// have no latency and pass d == 0.
func (m *Metrics) CheckDone(result string, d time.Duration) {
	m.subChecks.Inc(result)
	if result != "cache_hit" {
		m.subDuration.Observe(d.Seconds(), result)
	}
}

// RateLimited counts a 429 answer from Telegram (subscription.Observer).
func (m *Metrics) RateLimited(source string) { m.rateLimited.Inc(source) }

// BroadcastResult counts a broadcast delivery outcome (service.Broadcast hook).
func (m *Metrics) BroadcastResult(result string) { m.broadcast.Inc(result) }

// Router is what handlers need to register routes (implemented by *tele.Bot
// and by the instrumented router).
type Router interface {
	Use(middleware ...tele.MiddlewareFunc)
	Handle(endpoint interface{}, h tele.HandlerFunc, m ...tele.MiddlewareFunc)
}

// Instrument wraps a router so that every handler registered through it is
// counted and timed under the name of its endpoint. Endpoint names come from
// the code, never from user input, so the label cardinality is bounded.
func (m *Metrics) Instrument(r Router) Router { return &instrumented{Router: r, m: m} }

type instrumented struct {
	Router
	m *Metrics
}

func (i *instrumented) Handle(endpoint interface{}, h tele.HandlerFunc, mws ...tele.MiddlewareFunc) {
	name := EndpointName(endpoint)
	i.Router.Handle(endpoint, func(c tele.Context) error {
		i.m.inFlight.Add(1)
		start := time.Now()
		err := h(c)
		i.m.inFlight.Add(-1)

		status := "ok"
		if err != nil {
			status = "error"
		}
		i.m.updates.Inc(name, status)
		i.m.updateDuration.Observe(time.Since(start).Seconds(), name)
		return err
	}, mws...)
}

// EndpointName turns a telebot endpoint into a metric label.
func EndpointName(endpoint interface{}) string {
	switch e := endpoint.(type) {
	case string:
		return strings.TrimPrefix(e, "\a")
	case *tele.Btn:
		return "btn:" + e.Unique
	}
	return "other"
}

// InFlight returns the number of updates being handled right now.
func (m *Metrics) InFlight() int { return int(m.inFlight.Value()) }
