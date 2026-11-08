package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
)

// A new shop without products opens on the wizard: the name, the requisites, the first product, the link.
func TestSetupWizard(t *testing.T) {
	s, _ := shopSite(t)
	s.svc.SetReq(nil)
	resp, err := s.client.Get(s.http.URL + "/owner/enter?k=" + testOwnerKey)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/owner/welcome" {
		t.Fatalf("a new shop must open on the wizard, went to %q", loc)
	}

	_, page := s.get(t, "/owner/welcome")
	if !strings.Contains(page, "Как называется ваш магазин") {
		t.Fatal("step 1 is not the name")
	}
	csrf := match(t, csrfRe, page)
	if r, _ := s.post(t, "/owner/welcome/name", url.Values{"csrf": {csrf}, "name": {"Ы"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a one-letter name must be refused: %d", r.StatusCode)
	}
	r, _ := s.post(t, "/owner/welcome/name", url.Values{"csrf": {csrf}, "name": {"Плюшевая лавка"},
		"about": {"Мягкие игрушки"}, "contact": {"@plush"}})
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/owner/welcome?step=2" {
		t.Fatalf("name: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if b := s.svc.P.Business; b.Name != "Плюшевая лавка" || b.Description != "Мягкие игрушки" || b.Contact != "@plush" {
		t.Fatalf("the business was not renamed: %+v", b)
	}
	if v := s.svc.Setting(t.Context(), "biz_name"); v != "Плюшевая лавка" {
		t.Fatalf("the name is not kept for the next start: %q", v)
	}
	if _, home := s.get(t, "/"); !strings.Contains(home, "Плюшевая лавка") {
		t.Fatal("the storefront does not show the new name")
	}

	_, page = s.get(t, "/owner/welcome?step=2")
	if !strings.Contains(page, "Куда покупатели переводят деньги") || !strings.Contains(page, `name="back" value="/owner/welcome?step=3"`) {
		t.Fatal("step 2 is not the requisites")
	}
	r, _ = s.post(t, "/owner/requisites", url.Values{"csrf": {csrf}, "recipient": {"Анна П."}, "label1": {"По номеру"},
		"value1": {"+996 700 000 001"}, "back": {"/owner/welcome?step=3"}})
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/owner/welcome?step=3" || s.svc.Req() == nil {
		t.Fatalf("requisites from the wizard: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// the way back is the wizard only: another address falls back to the settings
	r, _ = s.post(t, "/owner/requisites", url.Values{"csrf": {csrf}, "recipient": {"Анна П."}, "label1": {"По номеру"},
		"value1": {"+996 700 000 001"}, "back": {"https://evil.example/"}})
	if loc := r.Header.Get("Location"); loc != "/owner#pay" {
		t.Fatalf("a foreign back address was followed: %q", loc)
	}

	_, page = s.get(t, "/owner/welcome?step=3")
	if !strings.Contains(page, "Добавьте первый товар") || !strings.Contains(page, "/owner/catalog/new?back=%2Fowner%2Fwelcome%3Fstep%3D3") {
		t.Fatal("step 3 does not lead to a new product and back")
	}
	_, form := s.get(t, "/owner/catalog/new?back=%2Fowner%2Fwelcome%3Fstep%3D3")
	if !strings.Contains(form, `name="back" value="/owner/welcome?step=3"`) {
		t.Fatal("the product form loses the way back to the wizard")
	}

	_, page = s.get(t, "/owner/welcome?step=4")
	if !strings.Contains(page, "Магазин работает") || !strings.Contains(page, "реквизиты указаны") {
		t.Fatal("the last step is not the ready shop")
	}
	if r, _ = s.post(t, "/owner/welcome/done", url.Values{"csrf": {csrf}, "to": {"admin"}}); r.Header.Get("Location") != "/admin" {
		t.Fatalf("done: %q", r.Header.Get("Location"))
	}
	resp, err = s.client.Get(s.http.URL + "/owner/enter?k=" + testOwnerKey)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/admin" {
		t.Fatalf("after the wizard the owner must land in the back office, went to %q", loc)
	}
}

// A shop that already sells (requisites and products) never sees the wizard.
func TestWizardSkipsWorkingShop(t *testing.T) {
	s, cat := shopSite(t)
	if _, err := cat.Save(t.Context(), 0, shop.Input{Title: "Мишка", Price: 199000, Variants: []shop.VariantInput{{Stock: 1}}}); err != nil {
		t.Fatal(err)
	}
	resp, err := s.client.Get(s.http.URL + "/owner/enter?k=" + testOwnerKey)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/admin" {
		t.Fatalf("a working shop went to %q", loc)
	}
}
