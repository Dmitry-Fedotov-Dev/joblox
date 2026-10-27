// Package metrics counts what the server does: Prometheus metrics on /metrics for Grafana, and a live
// summary for the built-in dashboard (online now, requests per second, latency, server load) that works
// without any monitoring stack.
package metrics

import (
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	window     = 300 // seconds of per-second request counts kept for the dashboard
	samples    = 2048
	onlineSpan = 2 * time.Minute
)

// Recorder implements order.Observer and the HTTP middleware.
type Recorder struct {
	reg       *prometheus.Registry
	requests  *prometheus.CounterVec
	durations *prometheus.HistogramVec
	decisions *prometheus.CounterVec
	moves     *prometheus.CounterVec

	mu       sync.Mutex
	started  time.Time
	buckets  [window]int64 // requests per second, ring indexed by unix second
	errors   [window]int64
	stamps   [window]int64 // which second each slot holds
	lat      [samples]float64
	latN     int
	visitors map[string]time.Time
	now      func() time.Time
}

func New() *Recorder {
	r := &Recorder{
		reg: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openline_http_requests_total",
			Help: "HTTP requests by route and status class."}, []string{"route", "code"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "openline_http_request_duration_seconds",
			Help: "HTTP request duration by route.", Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}}, []string{"route"}),
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openline_antifraud_decisions_total",
			Help: "Anti-fraud decisions on checkout: allow, review, block."}, []string{"action"}),
		moves: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "openline_order_transitions_total",
			Help: "Orders entering each workflow state."}, []string{"to"}),
		started:  time.Now(),
		visitors: map[string]time.Time{},
		now:      time.Now,
	}
	online := prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "openline_online_visitors",
		Help: "Visitors seen in the last two minutes."}, func() float64 { return float64(r.Online()) })
	r.reg.MustRegister(r.requests, r.durations, online, r.decisions, r.moves,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

// Handler serves /metrics.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

func (r *Recorder) Decision(action string) { r.decisions.WithLabelValues(action).Inc() }
func (r *Recorder) Transition(to string)   { r.moves.WithLabelValues(to).Inc() }

// Observe records one finished request.
func (r *Recorder) Observe(route string, code int, d time.Duration) {
	class := strconv.Itoa(code/100) + "xx"
	r.requests.WithLabelValues(route, class).Inc()
	r.durations.WithLabelValues(route).Observe(d.Seconds())
	sec := r.now().Unix()
	i := sec % window
	r.mu.Lock()
	if r.stamps[i] != sec {
		r.stamps[i], r.buckets[i], r.errors[i] = sec, 0, 0
	}
	r.buckets[i]++
	if code >= 500 {
		r.errors[i]++
	}
	r.lat[r.latN%samples] = d.Seconds()
	r.latN++
	r.mu.Unlock()
}

// Seen marks a visitor as present; id is a random cookie value, not a person.
func (r *Recorder) Seen(id string) {
	if id == "" {
		return
	}
	now := r.now()
	r.mu.Lock()
	r.visitors[id] = now
	if len(r.visitors) > 100_000 { // a flood of fresh cookies must not grow memory forever
		r.prune(now)
	}
	r.mu.Unlock()
}

func (r *Recorder) prune(now time.Time) {
	for k, t := range r.visitors {
		if now.Sub(t) > onlineSpan {
			delete(r.visitors, k)
		}
	}
}

// Online counts visitors seen in the last two minutes; Prometheus reads it on every scrape.
func (r *Recorder) Online() int {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(now)
	return len(r.visitors)
}

// Live is what the built-in dashboard shows.
type Live struct {
	Online    int       `json:"online"`
	RPS10s    float64   `json:"rps_10s"`
	RPS1m     float64   `json:"rps_1m"`
	RPS5m     float64   `json:"rps_5m"`
	Errors5m  int64     `json:"errors_5m"`
	P50ms     float64   `json:"p50_ms"`
	P95ms     float64   `json:"p95_ms"`
	Series    []int64   `json:"series"` // requests per second, oldest first, last 120 s
	UptimeSec int64     `json:"uptime_sec"`
	System    System    `json:"system"`
	At        time.Time `json:"at"`
}

func (r *Recorder) Live() Live {
	now := r.now()
	sec := now.Unix()
	r.mu.Lock()
	r.prune(now)
	online := len(r.visitors)
	sum := func(span int64) (n, e int64) {
		for s := sec - span + 1; s <= sec; s++ {
			i := s % window
			if r.stamps[i] == s {
				n += r.buckets[i]
				e += r.errors[i]
			}
		}
		return
	}
	n10, _ := sum(10)
	n60, _ := sum(60)
	n300, e300 := sum(300)
	series := make([]int64, 0, 120)
	for s := sec - 119; s <= sec; s++ {
		i := s % window
		if r.stamps[i] == s {
			series = append(series, r.buckets[i])
		} else {
			series = append(series, 0)
		}
	}
	n := r.latN
	if n > samples {
		n = samples
	}
	lat := append([]float64(nil), r.lat[:n]...)
	r.mu.Unlock()
	sort.Float64s(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(p*float64(len(lat)-1))] * 1000
	}
	return Live{Online: online, RPS10s: float64(n10) / 10, RPS1m: float64(n60) / 60, RPS5m: float64(n300) / 300,
		Errors5m: e300, P50ms: pct(.5), P95ms: pct(.95), Series: series, UptimeSec: int64(now.Sub(r.started).Seconds()),
		System: ReadSystem(), At: now.UTC()}
}
