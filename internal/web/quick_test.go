package web

import (
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// One tap on a tariff places the order: the page with the status and the sum, no form; the months are chosen
// there; after «Я оплатил» the contact is asked, typed or by Telegram.
func TestQuickOrder(t *testing.T) {
	s := newSite(t, "openline")
	s.srv.cfg.CustomerBot = func() string { return "freesellersdotcombot" }
	code, page := s.get(t, "/quick?add=hosting")
	if code != http.StatusOK || !strings.Contains(page, "data-auto") || !strings.Contains(page, `name="item_hosting" value="1"`) {
		t.Fatalf("the one-tap page: %d", code)
	}
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, page)}, "stamp": {match(t, stampRe, page)}, "quick": {"1"}, "item_hosting": {"1"}})
	link := r.Header.Get("Location")
	if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(link, "/o/") {
		t.Fatalf("one tap placed no order: %d %s", r.StatusCode, link)
	}
	_, page = s.get(t, link)
	for _, want := range []string{"Заказ принят и уже в обработке", "40802810000000000042", `data-copy="700"`, ">3 мес</button>", ">Я оплатил</button>"} {
		if !strings.Contains(page, want) {
			t.Errorf("the order page lacks %q", want)
		}
	}
	if strings.Contains(page, "Куда написать") {
		t.Error("the contact is asked before «Я оплатил»")
	}
	csrf := match(t, csrfRe, page)
	s.post(t, link+"/qty", url.Values{"csrf": {csrf}, "qty": {"3"}})
	s.post(t, link+"/qty", url.Values{"csrf": {csrf}, "qty": {"5"}}) // not one of the choices
	if _, page = s.get(t, link); !strings.Contains(page, `data-copy="2100"`) {
		t.Fatal("three months are not 2100")
	}
	s.post(t, link+"/paid", url.Values{"csrf": {csrf}})
	_, page = s.get(t, link)
	// the address card comes first while the transfer is checked; the news card is the compact one under it
	if !strings.Contains(page, "Осталось настроить") || !strings.Contains(page, "https://t.me/freesellersdotcombot?start=o"+strings.TrimPrefix(link, "/o/")) ||
		!strings.Contains(page, "Связь только через бота") || strings.Contains(page, ">3 мес</button>") {
		t.Fatal("after «Я оплатил»: no chat, no Telegram link, no «только через бота», or the months still change")
	}
	if r, _ := s.post(t, link+"/contact", url.Values{"csrf": {csrf}, "f_phone": {""}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an empty contact was taken: %d", r.StatusCode)
	}
	s.post(t, link+"/msg", url.Values{"csrf": {csrf}, "text": {"Здравствуйте, когда будет сайт?"}})
	s.post(t, link+"/botonly", url.Values{"csrf": {csrf}, "on": {"1"}})
	_, page = s.get(t, link)
	if !strings.Contains(page, `<li class="mine">Здравствуйте, когда будет сайт?</li>`) || !strings.Contains(page, `value="1" checked`) {
		t.Fatal("the message or the tick is not on the page")
	}
	o, _ := s.svc.Store.ByToken(t.Context(), strings.TrimPrefix(link, "/o/"))
	if !o.BotOnly() || o.Total != 2100*100 {
		t.Fatalf("order: %+v", o.Fields)
	}
	if err := s.svc.OperatorSays(t.Context(), o, "1", "Завтра к обеду"); err != nil {
		t.Fatal(err)
	}
	if _, page = s.get(t, link); !strings.Contains(page, "<li class=\"\">Завтра к обеду</li>") {
		t.Fatal("the seller's answer is not on the page")
	}
}

func TestQuickOrderTypedContact(t *testing.T) {
	s := newSite(t, "openline")
	_, page := s.get(t, "/quick?add=turnkey")
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, page)}, "stamp": {match(t, stampRe, page)}, "quick": {"1"}, "item_turnkey": {"1"}})
	link := r.Header.Get("Location")
	_, page = s.get(t, link)
	csrf := match(t, csrfRe, page)
	if strings.Contains(page, "мес</button>") || strings.Contains(page, "t.me/") {
		t.Fatal("turnkey has no months; no bot, no Telegram button")
	}
	s.post(t, link+"/paid", url.Values{"csrf": {csrf}})
	if r, _ := s.post(t, link+"/contact", url.Values{"csrf": {csrf}, "f_phone": {"@valya_shop"}, "f_site": {"valya"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("contact: %d", r.StatusCode)
	}
	if _, page = s.get(t, link); strings.Contains(page, "Куда написать") || !strings.Contains(page, "@valya_shop") {
		t.Fatal("the typed contact is not kept")
	}
}

// A shop without checkout.after keeps its form: /quick places nothing.
func TestNoQuickOrderWithoutAfter(t *testing.T) {
	s := newSite(t, "bakery")
	if code, _ := status(t, s, "/quick?add=baton"); code != http.StatusSeeOther {
		t.Fatalf("quick on a profile without it: %d", code)
	}
	_, page := s.get(t, "/")
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, page)}, "stamp": {match(t, stampRe, page)}, "quick": {"1"}, "item_baton": {"1"}, "method": {"transfer"}})
	if r.StatusCode == http.StatusSeeOther {
		t.Fatal("«quick» skipped the bakery's required fields")
	}
}

// A closed chat takes «Написать здесь» and the chat off the page; Telegram stays.
func TestChatClosedOnThePage(t *testing.T) {
	s := newSite(t, "openline")
	s.srv.cfg.CustomerBot = func() string { return "freesellersdotcombot" }
	_, page := s.get(t, "/quick?add=hosting")
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, page)}, "stamp": {match(t, stampRe, page)}, "quick": {"1"}, "item_hosting": {"1"}})
	link := r.Header.Get("Location")
	_, page = s.get(t, link)
	s.post(t, link+"/paid", url.Values{"csrf": {match(t, csrfRe, page)}})
	if err := s.svc.SetChatOff(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	_, page = s.get(t, link)
	if strings.Contains(page, "Написать здесь") || strings.Contains(page, `id="chat"`) || !strings.Contains(page, "t.me/freesellersdotcombot") {
		t.Fatal("a closed chat is still on the page, or Telegram is gone")
	}
}

// Once the payment is confirmed the customer fills in the site's details: the address first.
func TestSetupAfterPayment(t *testing.T) {
	s := newSite(t, "openline")
	_, page := s.get(t, "/quick?add=hosting")
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, page)}, "stamp": {match(t, stampRe, page)}, "quick": {"1"}, "item_hosting": {"1"}})
	link := r.Header.Get("Location")
	_, page = s.get(t, link)
	csrf := match(t, csrfRe, page)
	if strings.Contains(page, "Осталось настроить") {
		t.Fatal("the details are asked before the payment")
	}
	if r, _ := s.post(t, link+"/setup", url.Values{"csrf": {csrf}, "f_site": {"valya"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("setup before the payment: %d", r.StatusCode)
	}
	o, _ := s.svc.Store.ByToken(t.Context(), strings.TrimPrefix(link, "/o/"))
	if o.Fields["site"] != "" {
		t.Fatal("the details were taken before the payment")
	}
	// «Я оплатил»: the address may be chosen while the transfer is checked
	s.post(t, link+"/paid", url.Values{"csrf": {csrf}})
	if _, page = s.get(t, link); !strings.Contains(page, "Проверяем перевод") || !strings.Contains(page, ".free-sellers.com</span>") {
		t.Fatal("no address card while the transfer is checked")
	}
	o, _ = s.svc.Store.ByToken(t.Context(), strings.TrimPrefix(link, "/o/"))
	if s.svc.CanProvision(o) {
		t.Fatal("a shop can be made before the payment is confirmed")
	}
	if err := s.svc.ConfirmPayment(t.Context(), o, "1", ""); err != nil {
		t.Fatal(err)
	}
	if _, page = s.get(t, link); !strings.Contains(page, "Осталось настроить") || !strings.Contains(page, ".free-sellers.com</span>") {
		t.Fatal("no setup card after the payment")
	}
	if r, _ := s.post(t, link+"/setup", url.Values{"csrf": {csrf}, "f_site": {"Валя!"}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a bad address was taken: %d", r.StatusCode)
	}
	s.post(t, link+"/setup", url.Values{"csrf": {csrf}, "f_site": {"Valya-Shop"}, "f_shop": {"Магазин Вали"}})
	o, _ = s.svc.Store.ByToken(t.Context(), strings.TrimPrefix(link, "/o/"))
	if o.Fields["site"] != "valya-shop" || o.Fields["shop"] != "Магазин Вали" {
		t.Fatalf("details: %v", o.Fields)
	}
	if _, page = s.get(t, link); !strings.Contains(page, "Данные сохранены") {
		t.Fatal("the card does not say the details are saved")
	}
}

// The finished order links to the shop made of its address, and to the back office once an operator sent it.
func TestResultLinks(t *testing.T) {
	s := newSite(t, "openline")
	_, page := s.get(t, "/quick?add=hosting")
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, page)}, "stamp": {match(t, stampRe, page)}, "quick": {"1"}, "item_hosting": {"1"}})
	link := r.Header.Get("Location")
	_, page = s.get(t, link)
	csrf := match(t, csrfRe, page)
	get := func() *order.Order {
		o, _ := s.svc.Store.ByToken(t.Context(), strings.TrimPrefix(link, "/o/"))
		return o
	}
	_ = s.svc.ConfirmPayment(t.Context(), get(), "1", "")
	s.post(t, link+"/setup", url.Values{"csrf": {csrf}, "f_site": {"valya"}})
	_ = s.svc.Transition(t.Context(), get(), "take", profile.ActorOperator, "1")
	if _, page = s.get(t, link); strings.Contains(page, "Открыть сайт") {
		t.Fatal("the site is offered before it is done")
	}
	_ = s.svc.Transition(t.Context(), get(), "done", profile.ActorOperator, "1")
	if _, page = s.get(t, link); !strings.Contains(page, `href="https://valya.free-sellers.com/"`) || strings.Contains(page, "Настроить магазин") {
		t.Fatal("the finished order does not link to the shop, or offers a back office nobody sent")
	}
	if err := s.svc.SetAdminLink(t.Context(), get(), "1", "http://insecure"); err == nil {
		t.Fatal("a plain http link was taken")
	}
	if err := s.svc.SetAdminLink(t.Context(), get(), "1", "https://valya.free-sellers.com/owner/enter?k=secret"); err != nil {
		t.Fatal(err)
	}
	if _, page = s.get(t, link); !strings.Contains(page, "Настроить магазин") || !strings.Contains(page, "owner/enter?k=secret") {
		t.Fatal("the back office link is not on the page")
	}
}
