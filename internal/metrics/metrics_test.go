package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveCountsAndOnline(t *testing.T) {
	r := New()
	clock := time.Unix(1_800_000_000, 0)
	r.now = func() time.Time { return clock }
	for i := 0; i < 50; i++ {
		r.Observe("/", 200, 10*time.Millisecond)
	}
	r.Observe("/order", 500, 200*time.Millisecond)
	r.Seen("a")
	r.Seen("b")
	r.Seen("a")
	clock = clock.Add(time.Second)
	live := r.Live()
	if live.Online != 2 || live.RPS10s != 5.1 || live.Errors5m != 1 || live.P95ms < 9 || len(live.Series) != 120 {
		t.Fatalf("live: %+v", live)
	}
	clock = clock.Add(3 * time.Minute)
	if live = r.Live(); live.Online != 0 || live.RPS10s != 0 {
		t.Fatalf("after three minutes: %+v", live)
	}
}

func TestPrometheusEndpoint(t *testing.T) {
	r := New()
	r.Observe("/", 200, time.Millisecond)
	r.Decision("allow")
	r.Transition("done")
	r.Live()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{`joblox_http_requests_total{code="2xx",route="/"} 1`, `joblox_antifraud_decisions_total{action="allow"} 1`,
		`joblox_order_transitions_total{to="done"} 1`, "joblox_online_visitors", "go_goroutines"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestSystemReadsSomething(t *testing.T) {
	s := ReadSystem()
	if s.Cores == 0 || s.Goroutines == 0 || s.ProcessMB == 0 {
		t.Fatalf("system: %+v", s)
	}
}
