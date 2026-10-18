// Package obs provides observability: Prometheus metrics, health checks and
// the HTTP server that exposes them.
//
// The metrics are written in the Prometheus text exposition format by a small
// registry of our own, which avoids a client library dependency for a handful
// of counters, gauges and histograms.
package obs

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// family is a named metric that can write itself.
type family interface {
	write(w io.Writer)
}

// Registry holds metric families and serves them.
type Registry struct {
	mu       sync.Mutex
	families []family
	names    map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{names: map[string]bool{}} }

func (r *Registry) add(name string, f family) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[name] {
		panic("obs: duplicate metric " + name)
	}
	r.names[name] = true
	r.families = append(r.families, f)
}

// Handler serves the registry in the Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.mu.Lock()
		fams := append([]family(nil), r.families...)
		r.mu.Unlock()
		for _, f := range fams {
			f.write(w)
		}
	})
}

// Gather renders all metrics to a string (used by tests).
func (r *Registry) Gather() string {
	var b strings.Builder
	r.mu.Lock()
	fams := append([]family(nil), r.families...)
	r.mu.Unlock()
	for _, f := range fams {
		f.write(&b)
	}
	return b.String()
}

// --- counters ---

// CounterVec is a counter with labels.
type CounterVec struct {
	name, help string
	labels     []string
	mu         sync.Mutex
	series     map[string]*counterSeries
}

type counterSeries struct {
	values []string
	n      float64
}

// NewCounterVec registers a counter with the given label names.
func (r *Registry) NewCounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{name: name, help: help, labels: labels, series: map[string]*counterSeries{}}
	r.add(name, c)
	return c
}

// Inc adds one to the series with the given label values.
func (c *CounterVec) Inc(values ...string) { c.Add(1, values...) }

// Add adds v (which must not be negative) to the series.
func (c *CounterVec) Add(v float64, values ...string) {
	if len(values) != len(c.labels) {
		panic(fmt.Sprintf("obs: %s expects %d label values, got %d", c.name, len(c.labels), len(values)))
	}
	if v < 0 {
		return
	}
	key := strings.Join(values, "\xff")
	c.mu.Lock()
	s, ok := c.series[key]
	if !ok {
		s = &counterSeries{values: append([]string(nil), values...)}
		c.series[key] = s
	}
	s.n += v
	c.mu.Unlock()
}

// Value returns the current value of a series (0 if it does not exist yet).
func (c *CounterVec) Value(values ...string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.series[strings.Join(values, "\xff")]; ok {
		return s.n
	}
	return 0
}

func (c *CounterVec) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, escapeHelp(c.help), c.name)
	c.mu.Lock()
	keys := sortedKeys(c.series)
	for _, k := range keys {
		s := c.series[k]
		fmt.Fprintf(w, "%s%s %s\n", c.name, labelString(c.labels, s.values), formatFloat(s.n))
	}
	c.mu.Unlock()
}

// --- gauges ---

// GaugeFunc is a gauge whose value is read when the metrics are scraped.
type GaugeFunc struct {
	name, help string
	fn         func() float64
}

// NewGaugeFunc registers a gauge computed by fn on every scrape.
func (r *Registry) NewGaugeFunc(name, help string, fn func() float64) {
	r.add(name, &GaugeFunc{name: name, help: help, fn: fn})
}

func (g *GaugeFunc) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", g.name, escapeHelp(g.help), g.name, g.name, formatFloat(g.fn()))
}

// Gauge is a settable gauge without labels.
type Gauge struct {
	name, help string
	mu         sync.Mutex
	v          float64
}

// NewGauge registers a gauge.
func (r *Registry) NewGauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	r.add(name, g)
	return g
}

// Add changes the gauge by delta (negative to decrease).
func (g *Gauge) Add(delta float64) {
	g.mu.Lock()
	g.v += delta
	g.mu.Unlock()
}

// Value returns the current value.
func (g *Gauge) Value() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.v
}

func (g *Gauge) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", g.name, escapeHelp(g.help), g.name, g.name, formatFloat(g.Value()))
}

// --- histograms ---

// DefBuckets are latency buckets in seconds, from 5 ms to 30 s.
var DefBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}

// HistogramVec is a histogram with labels.
type HistogramVec struct {
	name, help string
	labels     []string
	buckets    []float64
	mu         sync.Mutex
	series     map[string]*histSeries
}

type histSeries struct {
	values []string
	counts []uint64 // per bucket, not cumulative; the last slot is +Inf
	sum    float64
	count  uint64
}

// NewHistogramVec registers a histogram. buckets must be sorted ascending.
func (r *Registry) NewHistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	h := &HistogramVec{name: name, help: help, labels: labels, buckets: buckets, series: map[string]*histSeries{}}
	r.add(name, h)
	return h
}

// Observe records one value.
func (h *HistogramVec) Observe(v float64, values ...string) {
	if len(values) != len(h.labels) {
		panic(fmt.Sprintf("obs: %s expects %d label values, got %d", h.name, len(h.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	h.mu.Lock()
	s, ok := h.series[key]
	if !ok {
		s = &histSeries{values: append([]string(nil), values...), counts: make([]uint64, len(h.buckets)+1)}
		h.series[key] = s
	}
	i := sort.SearchFloat64s(h.buckets, v) // first bucket with bound >= v
	s.counts[i]++
	s.sum += v
	s.count++
	h.mu.Unlock()
}

// Count returns the number of observations of a series.
func (h *HistogramVec) Count(values ...string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.series[strings.Join(values, "\xff")]; ok {
		return s.count
	}
	return 0
}

func (h *HistogramVec) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, escapeHelp(h.help), h.name)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range sortedKeys(h.series) {
		s := h.series[k]
		var cum uint64
		for i, le := range h.buckets {
			cum += s.counts[i]
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelString(append(append([]string(nil), h.labels...), "le"), append(append([]string(nil), s.values...), formatFloat(le))), cum)
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelString(append(append([]string(nil), h.labels...), "le"), append(append([]string(nil), s.values...), "+Inf")), s.count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.name, labelString(h.labels, s.values), formatFloat(s.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, labelString(h.labels, s.values), s.count)
	}
}

// --- formatting ---

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func labelString(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%s="%s"`, n, escapeLabel(values[i]))
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func formatFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	case math.IsNaN(f):
		return "NaN"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
