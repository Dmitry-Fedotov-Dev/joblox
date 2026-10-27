package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/hub"
)

// fakeOwner records what the owner's area asks of the launcher; handlers run on the server's goroutines.
type fakeOwner struct {
	mu     sync.Mutex
	setups []SetupInput
	shown  bool
}

func (f *fakeOwner) Setups() []SetupInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SetupInput(nil), f.setups...)
}

func (f *fakeOwner) Shown() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shown
}

// ownerSite is newSite with the owner's area on, as `joblox up` runs it.
func ownerSite(t *testing.T) (*site, *fakeOwner) {
	t.Helper()
	s := newSite(t, "bakery")
	f := &fakeOwner{}
	s.srv.cfg.Owner = &Owner{Key: "masterkey0123456789", MarketURL: "https://joblox.example", DataDir: "/data",
		Configured: func() bool { return len(f.Setups()) > 0 },
		Setup: func(in SetupInput) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.setups = append(f.setups, in)
			return nil
		},
		Shown: f.Shown,
		Portal: func(context.Context) []hub.Card {
			return []hub.Card{{Kind: "call", Title: "Трубка: Контакт", URL: "https://openline.one/", Badge: "OpenLine"}}
		},
		SetShown: func(v bool) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.shown = v
			return nil
		}}
	s.http.Config.Handler = s.srv.Handler() // routes depend on the owner config
	return s, f
}

func TestOwnerNeedsTheMasterLink(t *testing.T) {
	s, _ := ownerSite(t)
	for _, path := range []string{"/owner", "/admin", "/admin/live.json", "/owner/enter?k=wrong"} {
		if code, _ := s.get(t, path); code != http.StatusNotFound {
			t.Errorf("%s without the master link: %d", path, code)
		}
	}
	if resp, _ := s.post(t, "/owner/o/1/paid", url.Values{}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("owner action without the master link: %d", resp.StatusCode)
	}
	resp, err := s.client.Get(s.http.URL + "/owner/enter?k=masterkey0123456789")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/owner" {
		t.Fatalf("enter: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, page := s.get(t, "/owner"); code != 200 || !strings.Contains(page, "Кем вы будете?") || !strings.Contains(page, "Трубка: Контакт") {
		t.Fatalf("setup page: %d", code)
	}
}

func TestOwnerSetupAndOrders(t *testing.T) {
	s, f := ownerSite(t)
	s.get(t, "/owner/enter?k=masterkey0123456789")
	_, page := s.get(t, "/owner")
	csrf := match(t, csrfRe, page)

	form := url.Values{"csrf": {csrf}, "kind": {"taxi"}, "name": {"Такси Азамат"}, "contact": {"+996 700 000 000"},
		"place": {"Чолпон-Ата"}, "country": {"kg"}, "pay": {"Mbank +996 700 000 000"}, "show": {"1"}}
	if resp, body := s.post(t, "/owner/setup", form); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "соглашение") {
		t.Fatalf("setup without the terms: %d", resp.StatusCode)
	}
	form.Set("terms", "1")
	resp, _ := s.post(t, "/owner/setup", form)
	if resp.StatusCode != http.StatusOK || len(f.Setups()) != 1 {
		t.Fatalf("setup: %d, %d calls", resp.StatusCode, len(f.Setups()))
	}
	if in := f.Setups()[0]; in.Kind != "taxi" || in.Name != "Такси Азамат" || !in.Show || in.Country != "kg" {
		t.Fatalf("setup input: %+v", in)
	}

	// an order comes in; the owner works it from the browser
	if resp, _ := s.post(t, "/order", s.bakeryForm(t)); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("order: %d", resp.StatusCode)
	}
	_, page = s.get(t, "/owner")
	for _, want := range []string{"№1", "Анна", "Оплата получена", "Письма клиентам"} {
		if !strings.Contains(page, want) {
			t.Errorf("owner page lacks %q", want)
		}
	}
	if resp, _ := s.post(t, "/owner/o/1/paid", url.Values{"csrf": {"forged"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("owner action with a forged csrf: %d", resp.StatusCode)
	}
	if resp, _ := s.post(t, "/owner/o/1/paid", url.Values{"csrf": {csrf}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("paid: %d", resp.StatusCode)
	}
	if resp, _ := s.post(t, "/owner/o/1/t-cancel", url.Values{"csrf": {csrf}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("cancel: %d", resp.StatusCode)
	}
	if resp, _ := s.post(t, "/owner/o/1/t-nonsense", url.Values{"csrf": {csrf}}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("unknown transition: %d", resp.StatusCode)
	}
	o, _ := s.svc.Store.Get(t.Context(), 1)
	if !o.Paid || o.State != "cancelled" {
		t.Fatalf("order after the owner's actions: paid %v, %s", o.Paid, o.State)
	}
	if resp, _ := s.post(t, "/owner/market", url.Values{"csrf": {csrf}, "show": {"1"}}); resp.StatusCode != http.StatusSeeOther || !f.Shown() {
		t.Fatalf("show on the market: %d %v", resp.StatusCode, f.Shown())
	}
	if code, _ := s.get(t, "/admin"); code != 200 {
		t.Fatalf("dashboard for the owner: %d", code)
	}
}

func TestTermsPage(t *testing.T) {
	s, _ := ownerSite(t)
	code, page := s.get(t, "/terms")
	if code != 200 || !strings.Contains(page, "Пекарня «Колос»") || !strings.Contains(page, "https://joblox.example/terms") {
		t.Fatalf("terms: %d", code)
	}
}
