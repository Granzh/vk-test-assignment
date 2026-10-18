package obs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v4"
)

func TestCounterExposition(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounterVec("jobs_total", "Jobs by \"kind\".\nSecond line.", "kind", "status")
	c.Inc("a", "ok")
	c.Add(2, "a", "ok")
	c.Inc("b\"x\\y\nz", "err")
	c.Add(-5, "a", "ok") // counters never go down

	out := r.Gather()
	for _, want := range []string{
		"# HELP jobs_total Jobs by \"kind\".\\nSecond line.\n",
		"# TYPE jobs_total counter\n",
		"jobs_total{kind=\"a\",status=\"ok\"} 3\n",
		"jobs_total{kind=\"b\\\"x\\\\y\\nz\",status=\"err\"} 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if c.Value("a", "ok") != 3 || c.Value("none", "x") != 0 {
		t.Errorf("values: %v %v", c.Value("a", "ok"), c.Value("none", "x"))
	}
}

func TestCounterWithoutLabels(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounterVec("panics_total", "Panics.")
	c.Inc()
	c.Inc()
	if out := r.Gather(); !strings.Contains(out, "panics_total 2\n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestLabelCountMismatchPanics(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounterVec("x_total", "x", "a")
	defer func() {
		if recover() == nil {
			t.Error("expected a panic: a wrong label count is a programming error")
		}
	}()
	c.Inc("1", "2")
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	r := NewRegistry()
	r.NewGauge("g", "g")
	defer func() {
		if recover() == nil {
			t.Error("expected a panic for a duplicate metric name")
		}
	}()
	r.NewGauge("g", "g")
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogramVec("latency_seconds", "Latency.", []float64{0.1, 1}, "op")
	for _, v := range []float64{0.05, 0.1, 0.5, 2, 3} { // 0.1 falls into the le=0.1 bucket
		h.Observe(v, "get")
	}
	out := r.Gather()
	for _, want := range []string{
		"# TYPE latency_seconds histogram\n",
		"latency_seconds_bucket{op=\"get\",le=\"0.1\"} 2\n",
		"latency_seconds_bucket{op=\"get\",le=\"1\"} 3\n",
		"latency_seconds_bucket{op=\"get\",le=\"+Inf\"} 5\n",
		"latency_seconds_sum{op=\"get\"} 5.65\n",
		"latency_seconds_count{op=\"get\"} 5\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if h.Count("get") != 5 {
		t.Errorf("count = %d", h.Count("get"))
	}
}

func TestGauges(t *testing.T) {
	r := NewRegistry()
	g := r.NewGauge("in_flight", "In flight.")
	g.Add(3)
	g.Add(-1)
	r.NewGaugeFunc("answer", "The answer.", func() float64 { return 42 })
	out := r.Gather()
	if !strings.Contains(out, "in_flight 2\n") || !strings.Contains(out, "answer 42\n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestMetricsHandlerContentType(t *testing.T) {
	m := NewMetrics()
	m.CheckDone("subscribed", 30*time.Millisecond)
	m.CheckDone("cache_hit", 0)
	rec := httptest.NewRecorder()
	m.Reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`bot_subscription_checks_total{result="subscribed"} 1`,
		`bot_subscription_checks_total{result="cache_hit"} 1`,
		`bot_subscription_check_duration_seconds_count{result="subscribed"} 1`,
		"go_goroutines ", "process_start_time_seconds ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if strings.Contains(body, `bot_subscription_check_duration_seconds_count{result="cache_hit"}`) {
		t.Error("cache hits have no Telegram latency and must not be observed")
	}
}

func TestPoolGauges(t *testing.T) {
	m := NewMetrics()
	m.RegisterPool(func() PoolStat { return PoolStat{Acquired: 3, Idle: 2, Total: 5, Max: 20, WaitCount: 7} })
	body := m.Reg.Gather()
	for _, want := range []string{"bot_db_pool_acquired_conns 3\n", "bot_db_pool_max_conns 20\n", "bot_db_pool_wait_total 7\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q", want)
		}
	}
}

type fakeRouter struct {
	handlers map[string]tele.HandlerFunc
}

func (f *fakeRouter) Use(...tele.MiddlewareFunc) {}
func (f *fakeRouter) Handle(endpoint interface{}, h tele.HandlerFunc, _ ...tele.MiddlewareFunc) {
	if f.handlers == nil {
		f.handlers = map[string]tele.HandlerFunc{}
	}
	f.handlers[EndpointName(endpoint)] = h
}

func TestInstrumentCountsAndTimesHandlers(t *testing.T) {
	m := NewMetrics()
	fr := &fakeRouter{}
	r := m.Instrument(fr)
	boom := errors.New("boom")
	btn := (&tele.ReplyMarkup{}).Data("label", "get_promo")
	r.Handle("/start", func(tele.Context) error { return nil })
	r.Handle(&btn, func(tele.Context) error { return boom })
	r.Handle(tele.OnText, func(tele.Context) error { return nil })

	for name, want := range map[string]error{"/start": nil, "btn:get_promo": boom, "text": nil} {
		h, ok := fr.handlers[name]
		if !ok {
			t.Fatalf("handler %q was not registered under that name; have %v", name, fr.handlers)
		}
		if err := h(nil); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want the handler's own error", name, err)
		}
	}
	body := m.Reg.Gather()
	for _, want := range []string{
		`bot_updates_total{handler="/start",status="ok"} 1`,
		`bot_updates_total{handler="btn:get_promo",status="error"} 1`,
		`bot_updates_total{handler="text",status="ok"} 1`,
		`bot_update_duration_seconds_count{handler="/start"} 1`,
		"bot_updates_in_flight 0\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestEndpointNameIsBounded(t *testing.T) {
	if got := EndpointName(42); got != "other" {
		t.Errorf("got %q", got)
	}
}

func TestHealthEndpoints(t *testing.T) {
	var ready error
	mux := Mux(NewRegistry(), func(context.Context) error { return ready })
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	if rec := get("/healthz"); rec.Code != 200 {
		t.Errorf("healthz = %d", rec.Code)
	}
	if rec := get("/readyz"); rec.Code != 200 {
		t.Errorf("readyz = %d", rec.Code)
	}
	ready = errors.New("database is down")
	rec := get("/readyz")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "database is down") {
		t.Errorf("readyz while down = %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/healthz"); rec.Code != 200 {
		t.Error("liveness must not depend on the database: a restart would not fix it")
	}
	if rec := get("/metrics"); rec.Code != 200 {
		t.Errorf("metrics = %d", rec.Code)
	}
}

func TestServeAndProbe(t *testing.T) {
	var ready error
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	addr := "127.0.0.1:0"

	// reserve a free port, then serve on it
	srvAddr := freeAddr(t)
	go func() {
		done <- Serve(ctx, srvAddr, Mux(NewRegistry(), func(context.Context) error { return ready }), log)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for Probe(srvAddr) != nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := Probe(srvAddr); err != nil {
		t.Fatalf("probe of a ready server: %v", err)
	}
	ready = errors.New("no database")
	if err := Probe(srvAddr); err == nil {
		t.Error("probe must fail while the server is not ready")
	}
	_ = addr

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
	if err := Probe(srvAddr); err == nil {
		t.Error("probe of a stopped server must fail")
	}
	if err := Probe("not an address"); err == nil {
		t.Error("bad address must fail")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	return addr
}
