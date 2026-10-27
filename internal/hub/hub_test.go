package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newHub(t *testing.T) (*Hub, *httptest.Server, *time.Time) {
	t.Helper()
	h, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_800_000_000, 0)
	h.now = func() time.Time { return clock }
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(func() { srv.Close(); _ = h.Close() })
	return h, srv, &clock
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url+"/api/v1/beat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func stats(t *testing.T, url string) Stats {
	t.Helper()
	resp, err := http.Get(url + "/api/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("stats without CORS")
	}
	var st Stats
	_ = json.NewDecoder(resp.Body).Decode(&st)
	return st
}

const idA = "0123456789abcdef0123456789abcdef"
const idB = "fedcba9876543210fedcba9876543210"

func TestBeatsAddUp(t *testing.T) {
	_, srv, clock := newHub(t)
	if code := post(t, srv.URL, `{"id":"`+idA+`","kind":"bakery","version":"1.0","online":5,"orders_today":3}`); code != http.StatusNoContent {
		t.Fatalf("beat A: %d", code)
	}
	if code := post(t, srv.URL, `{"id":"`+idB+`","kind":"Такси <b>","version":"1.0","online":2,"orders_today":1}`); code != http.StatusNoContent {
		t.Fatalf("beat B: %d", code)
	}
	st := stats(t, srv.URL)
	if st.Installations != 2 || st.Online != 7 || st.OrdersToday != 4 || st.Kinds["bakery"] != 1 || st.Kinds["other"] != 1 {
		t.Fatalf("stats: %+v", st)
	}
	*clock = clock.Add(ActiveWindow + time.Minute)
	if st := stats(t, srv.URL); st.Installations != 0 {
		t.Fatalf("silent installations still counted: %+v", st)
	}
}

func TestBeatValidationAndRate(t *testing.T) {
	_, srv, clock := newHub(t)
	for _, bad := range []string{`{"id":"short"}`, `{"id":"` + idA + `","online":-1}`, `{"id":"` + idA + `","name":"Пекарня"}`, `not json`} {
		if code := post(t, srv.URL, bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	beat := `{"id":"` + idA + `","kind":"taxi","version":"1","online":999999999,"orders_today":1}`
	if code := post(t, srv.URL, beat); code != http.StatusNoContent {
		t.Fatalf("first: %d", code)
	}
	if code := post(t, srv.URL, beat); code != http.StatusTooManyRequests {
		t.Fatalf("second within a minute: %d", code)
	}
	*clock = clock.Add(time.Minute)
	if code := post(t, srv.URL, beat); code != http.StatusNoContent {
		t.Fatalf("after a minute: %d", code)
	}
	if st := stats(t, srv.URL); st.Online != maxOnline {
		t.Fatalf("online not capped: %d", st.Online)
	}
}

func TestNewIDsPerAddressAreLimited(t *testing.T) {
	_, srv, _ := newHub(t)
	ok := 0
	for i := 0; i < 25; i++ {
		id := strings.Repeat("0", 30) + string("0123456789abcdef"[i%16]) + string("0123456789abcdef"[i/16])
		if post(t, srv.URL, `{"id":"`+id+`","kind":"shop","version":"1","online":1,"orders_today":0}`) == http.StatusNoContent {
			ok++
		}
	}
	if ok != 20 {
		t.Fatalf("new ids accepted from one address: %d", ok)
	}
}

func TestPublicPage(t *testing.T) {
	_, srv, _ := newHub(t)
	post(t, srv.URL, `{"id":"`+idA+`","kind":"bakery","version":"1","online":3,"orders_today":2}`)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "Joblox Network") || !strings.Contains(string(body), "bakery") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("public page")
	}
}

func TestReporter(t *testing.T) {
	h, srv, _ := newHub(t)
	r := &Reporter{URL: srv.URL + "/", ID: NewID(), Kind: "Bakery", Version: "1.2.3",
		Online: func() int { return 4 }, OrdersToday: func(context.Context) int { return 9 }}
	if err := r.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := h.Stats(context.Background())
	if err != nil || st.Installations != 1 || st.Online != 4 || st.OrdersToday != 9 || st.Kinds["bakery"] != 1 {
		t.Fatalf("reported: %+v %v", st, err)
	}
	if err := r.Send(context.Background()); err == nil {
		t.Fatal("second beat within a minute was accepted")
	}
}

func TestPrune(t *testing.T) {
	h, srv, clock := newHub(t)
	post(t, srv.URL, `{"id":"`+idA+`","kind":"shop","version":"1","online":1,"orders_today":0}`)
	*clock = clock.Add(8 * 24 * time.Hour)
	if err := h.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM installs`).Scan(&n)
	if n != 0 {
		t.Fatalf("rows after prune: %d", n)
	}
}
