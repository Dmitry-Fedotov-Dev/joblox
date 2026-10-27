package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/metrics"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/order"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/store"
)

type site struct {
	srv    *Server
	http   *httptest.Server
	admin  *httptest.Server
	client *http.Client
	box    *mail.Outbox
	svc    *order.Service
}

func newSite(t *testing.T, preset string) *site {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := profile.Load(filepath.Join(filepath.Dir(file), "..", "..", "profiles", preset+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	box := &mail.Outbox{}
	req := &order.Requisites{Recipient: "ИП Тестов Т. Т.", Lines: []order.RequisiteRow{{Label: "Расчётный счёт", Value: "40802810000000000042"}},
		Purpose: "Оплата заказа {ref}, без НДС"}
	svc := order.New(p, st, box, req, []byte("secret"), "http://shop.test")
	rec := metrics.New()
	svc.Observe = rec
	srv, err := New(svc, rec, box, []byte("secret"), Config{Dev: true, Version: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &site{srv: srv, http: httptest.NewServer(srv.Handler()), admin: httptest.NewServer(srv.AdminHandler()), box: box, svc: svc}
	jar, _ := cookiejar.New(nil)
	s.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() { s.http.Close(); s.admin.Close(); _ = st.Close() })
	return s
}

var (
	csrfRe  = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	stampRe = regexp.MustCompile(`name="stamp" value="([^"]+)"`)
	codeRe  = regexp.MustCompile(`Код подтверждения: (\d{6})`)
)

func (s *site) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := s.client.Get(s.http.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type reply struct {
	StatusCode int
	Header     http.Header
}

func (s *site) post(t *testing.T, path string, form url.Values) (reply, string) {
	t.Helper()
	resp, err := s.client.PostForm(s.http.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, string(b)
}

func match(t *testing.T, re *regexp.Regexp, body string) string {
	t.Helper()
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s not found in page", re)
	}
	return m[1]
}

func (s *site) bakeryForm(t *testing.T) url.Values {
	t.Helper()
	code, page := s.get(t, "/")
	if code != 200 {
		t.Fatalf("shop: %d", code)
	}
	return url.Values{
		"csrf": {match(t, csrfRe, page)}, "stamp": {s.srv.formStamp(time.Now().Add(-30 * time.Second))},
		"item_baton": {"2"}, "item_croissant": {"1"}, "f_name": {"Анна"}, "f_phone": {"+7 900 123-45-67"},
		"f_email": {"anna@example.ru"}, "f_pickup_at": {"2026-10-27T14:00"}, "method": {"transfer"},
	}
}

func TestShopPage(t *testing.T) {
	s := newSite(t, "bakery")
	resp, err := s.client.Get(s.http.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{"Пекарня «Колос»", "Батон нарезной", `name="f_pickup_at"`, `type="datetime-local"`, `name="website"`, "Переводом по реквизитам"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("shop page lacks %q", want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP: %q", csp)
	}
	if strings.Contains(string(body), "40802810000000000042") {
		t.Fatal("requisites on the public page")
	}
	if !stampRe.MatchString(string(body)) {
		t.Fatal("no form stamp")
	}
}

func TestCheckoutToRequisites(t *testing.T) {
	s := newSite(t, "bakery")
	resp, _ := s.post(t, "/order", s.bakeryForm(t))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	link := resp.Header.Get("Location")
	_, page := s.get(t, link)
	if !strings.Contains(page, "Подтвердите email") || strings.Contains(page, "40802810000000000042") {
		t.Fatal("before the code: no code form or requisites already shown")
	}
	code := match(t, codeRe, s.box.Messages()[0].Subject+s.box.Messages()[0].Body)
	resp, _ = s.post(t, link+"/code", url.Values{"csrf": {match(t, csrfRe, page)}, "code": {"000000"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}
	resp, _ = s.post(t, link+"/code", url.Values{"csrf": {match(t, csrfRe, page)}, "code": {code}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("right code: %d", resp.StatusCode)
	}
	_, page = s.get(t, link)
	for _, want := range []string{"40802810000000000042", "Оплата заказа KL-1, без НДС", "298.00"} {
		if !strings.Contains(page, want) {
			t.Errorf("order page lacks %q", want)
		}
	}
	resp, _ = s.post(t, link+"/t/cancel", url.Values{"csrf": {match(t, csrfRe, page)}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("cancel: %d", resp.StatusCode)
	}
	_, page = s.get(t, link)
	if !strings.Contains(page, "Заказ отменён") || strings.Contains(page, "40802810000000000042") {
		t.Fatal("cancelled order still shows requisites")
	}
	code2, js := s.get(t, link+"/state.json")
	if code2 != 200 || !strings.Contains(js, `"state":"cancelled"`) {
		t.Fatalf("state.json: %d %s", code2, js)
	}
}

func TestCSRFAndHoneypot(t *testing.T) {
	s := newSite(t, "bakery")
	form := s.bakeryForm(t)
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("csrf", "forged")
	if resp, _ := s.post(t, "/order", bad); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged csrf: %d", resp.StatusCode)
	}
	form.Set("website", "http://spam.example")
	resp, body := s.post(t, "/order", form)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "не можем принять") || strings.Contains(body, "скрытое поле") {
		t.Fatalf("honeypot: %d", resp.StatusCode)
	}
	if list, _ := s.svc.Store.List(context.Background(), order.Filter{}, nil); len(list) != 0 {
		t.Fatal("refused order was stored")
	}
}

func TestValidationKeepsInput(t *testing.T) {
	s := newSite(t, "bakery")
	form := s.bakeryForm(t)
	form.Set("f_email", "anna")
	resp, body := s.post(t, "/order", form)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "проверьте адрес почты") || !strings.Contains(body, `value="Анна"`) {
		t.Fatalf("validation page: %d", resp.StatusCode)
	}
}

func TestFastFillGoesToReview(t *testing.T) {
	s := newSite(t, "bakery")
	form := s.bakeryForm(t)
	form.Set("stamp", s.srv.formStamp(time.Now()))
	resp, _ := s.post(t, "/order", form)
	_, page := s.get(t, resp.Header.Get("Location"))
	if !strings.Contains(page, "на проверке у оператора") {
		t.Fatal("instant submit was not held for review")
	}
}

func TestAdminListener(t *testing.T) {
	s := newSite(t, "bakery")
	s.post(t, "/order", s.bakeryForm(t))
	resp, err := http.Get(s.admin.URL + "/admin/live.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Live   metrics.Live `json:"live"`
		Orders order.Stats  `json:"orders"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	resp.Body.Close()
	if data.Orders.Today != 1 || data.Live.Online < 1 || len(data.Live.Series) != 120 {
		t.Fatalf("live: %+v", data)
	}
	resp, _ = http.Get(s.admin.URL + "/admin")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Дашборд") || !strings.Contains(string(body), s.svc.Req.Fingerprint()) {
		t.Fatal("admin page")
	}
	if code, _ := s.get(t, "/admin"); code != http.StatusNotFound {
		t.Fatalf("admin on the public listener: %d", code)
	}
	resp, _ = http.Get(s.admin.URL + "/metrics")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `openline_antifraud_decisions_total{action="allow"} 1`) {
		t.Fatal("metrics")
	}
}

func TestClientIP(t *testing.T) {
	trusted, err := ParsePrefixes("127.0.0.1, 10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.1.2.3")
	if got := ClientIP(r, trusted); got != "198.51.100.7" {
		t.Fatalf("behind proxies: %s", got)
	}
	r.RemoteAddr = "203.0.113.1:5000"
	if got := ClientIP(r, trusted); got != "203.0.113.1" {
		t.Fatalf("untrusted sender forged XFF: %s", got)
	}
	if _, err := ParsePrefixes("nonsense"); err == nil {
		t.Fatal("bad prefix accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(1, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d refused", i)
		}
	}
	if l.Allow("a") || !l.Allow("b") {
		t.Fatal("bucket not per client")
	}
}

func TestDevOutboxOnlyInDev(t *testing.T) {
	s := newSite(t, "bakery")
	if code, _ := s.get(t, "/dev/outbox"); code != 200 {
		t.Fatalf("dev outbox: %d", code)
	}
	s.srv.cfg.Dev = false
	h := httptest.NewServer(s.srv.Handler())
	defer h.Close()
	resp, _ := http.Get(h.URL + "/dev/outbox")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("outbox in production: %d", resp.StatusCode)
	}
}

func TestQuoteProfilePage(t *testing.T) {
	s := newSite(t, "freight")
	_, page := s.get(t, "/")
	if !strings.Contains(page, "Отправить заявку") || strings.Contains(page, "Что заказываете") {
		t.Fatal("quote storefront")
	}
}
