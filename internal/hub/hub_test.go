package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

type page struct {
	code   int
	header http.Header
	body   string
}

func get(t *testing.T, url string) page {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return page{resp.StatusCode, resp.Header, string(body)}
}

func TestPublicPages(t *testing.T) {
	_, srv, _ := newHub(t)
	post(t, srv.URL, `{"id":"`+idA+`","kind":"bakery","version":"1","online":3,"orders_today":2}`)
	home := get(t, srv.URL+"/")
	for _, want := range []string{"OpenLine Радио", "Agent Pay", "Трубка: Контакт", "Мамина мастерская", `name="q"`, "Стать исполнителем"} {
		if !strings.Contains(home.body, want) {
			t.Errorf("market page has no %q", want)
		}
	}
	if !strings.Contains(home.header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("market page without CSP")
	}
	radio := get(t, srv.URL+"/?kind=radio").body
	if !strings.Contains(radio, "OpenLine Радио") || strings.Contains(radio, "Agent Pay") {
		t.Error("kind does not filter the tiles")
	}
	found := get(t, srv.URL+"/?q="+url.QueryEscape("радио станц")).body
	if !strings.Contains(found, "OpenLine Радио") || strings.Contains(found, "Agent Pay") || !strings.Contains(found, `value="радио станц"`) {
		t.Error("the search does not filter the tiles")
	}
	if none := get(t, srv.URL+"/?q=zzzz").body; !strings.Contains(none, "Ничего не нашлось") {
		t.Error("an empty search result is not explained")
	}
	for path, want := range map[string]string{"/start": "Termux", "/terms": "Пользовательское соглашение", "/api": "/api/v1/market",
		"/about": "AGPL-3.0", "/t/master": "Мастер на выезд", "/t/shop": "Магазин с фото"} {
		if p := get(t, srv.URL+path); p.code != http.StatusOK || !strings.Contains(p.body, want) {
			t.Errorf("%s: %d, no %q", path, p.code, want)
		}
	}
}

const card = `"listing":{"title":"Такси <b>Азамат</b>","place":"Чолпон-Ата","about":"Camry","contact":"+996 700","url":"https://abc-def.trycloudflare.com/o/secret"}`

func TestListingOnTheMarket(t *testing.T) {
	h, srv, clock := newHub(t)
	h.AdminToken = "admintoken"
	if code := post(t, srv.URL, `{"id":"`+idA+`","kind":"taxi","version":"1","online":2,"orders_today":0,`+card+`}`); code != http.StatusNoContent {
		t.Fatalf("beat with a card: %d", code)
	}
	// a card with a non-https address is dropped, the beat still counts
	if code := post(t, srv.URL, `{"id":"`+idB+`","kind":"taxi","version":"1","online":1,"orders_today":0,"listing":{"title":"x","url":"javascript:alert(1)"}}`); code != http.StatusNoContent {
		t.Fatalf("beat with a bad card: %d", code)
	}
	cards, err := h.Market(context.Background(), "taxi")
	if err != nil || len(cards) != 1 {
		t.Fatalf("taxi cards: %+v %v", cards, err)
	}
	if c := cards[0]; c.Title != "Такси <b>Азамат</b>" || c.URL != "https://abc-def.trycloudflare.com/" || c.Online != 2 {
		t.Fatalf("card: %+v", c)
	}
	body := get(t, srv.URL+"/").body
	if !strings.Contains(body, "Такси &lt;b&gt;Азамат&lt;/b&gt;") || strings.Contains(body, "<b>Азамат") {
		t.Fatal("the card title is not escaped on the page")
	}
	js := get(t, srv.URL+"/api/v1/market?kind=taxi").body
	if !strings.Contains(js, "abc-def.trycloudflare.com") {
		t.Fatalf("market api: %s", js)
	}

	hide := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/hide", strings.NewReader(`{"id":"`+idA+`","hidden":true}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := hide("wrong"); code != http.StatusForbidden {
		t.Fatalf("hide with a wrong token: %d", code)
	}
	if code := hide("admintoken"); code != http.StatusNoContent {
		t.Fatalf("hide: %d", code)
	}
	if cards, _ := h.Market(context.Background(), "taxi"); len(cards) != 0 {
		t.Fatalf("hidden card still shown: %+v", cards)
	}
	if err := h.Hide(context.Background(), idA, false); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(ActiveWindow + time.Minute)
	if cards, _ := h.Market(context.Background(), "taxi"); len(cards) != 0 {
		t.Fatalf("offline card still shown: %+v", cards)
	}
}

func TestReporter(t *testing.T) {
	h, srv, _ := newHub(t)
	r := &Reporter{URL: srv.URL + "/", ID: NewID(), Kind: "Bakery", Version: "1.2.3",
		Online: func() int { return 4 }, OrdersToday: func(context.Context) int { return 9 },
		Listing: func() *Listing { return &Listing{Title: "Пекарня у дома", URL: "https://bake.example.com"} }}
	if err := r.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := h.Stats(context.Background())
	if err != nil || st.Installations != 1 || st.Online != 4 || st.OrdersToday != 9 || st.Kinds["bakery"] != 1 {
		t.Fatalf("reported: %+v %v", st, err)
	}
	if cards, _ := h.Market(context.Background(), "bakery"); len(cards) != 1 || cards[0].Title != "Пекарня у дома" {
		t.Fatalf("reported card: %+v", cards)
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
