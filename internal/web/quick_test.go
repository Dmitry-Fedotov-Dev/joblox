package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tgData is what Telegram's sign-in sends back, signed with the bot's token.
func tgData(token string, fields map[string]any) string {
	var lines []string
	for k, v := range fields {
		lines = append(lines, k+"="+toString(v))
	}
	sort.Strings(lines)
	key := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(strings.Join(lines, "\n")))
	fields["hash"] = hex.EncodeToString(mac.Sum(nil))
	raw, _ := json.Marshal(fields)
	return base64.StdEncoding.EncodeToString(raw)
}

func toString(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case string:
		return x
	}
	panic("type")
}

// One tap on a tariff places the order: the page with the status and the sum, no form; the months are chosen
// there; after «Я оплатил» the contact is asked, typed or by Telegram.
func TestQuickOrder(t *testing.T) {
	s := newSite(t, "openline")
	s.srv.cfg.TelegramLogin = "123456:TESTTOKEN"
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
	if !strings.Contains(page, "Куда написать") || !strings.Contains(page, "oauth.telegram.org/auth?bot_id=123456") || strings.Contains(page, ">3 мес</button>") {
		t.Fatal("after «Я оплатил»: no contact card, no Telegram, or the months still change")
	}
	if r, _ := s.post(t, link+"/contact", url.Values{"csrf": {csrf}, "f_phone": {""}}); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an empty contact was taken: %d", r.StatusCode)
	}
	// Telegram vouches for the account
	data := tgData("123456:TESTTOKEN", map[string]any{"id": int64(42), "first_name": "Айгуль", "username": "aigul_cakes", "auth_date": time.Now().Unix()})
	if r, _ := s.post(t, link+"/tg", url.Values{"csrf": {csrf}, "data": {"forged" + data}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a forged sign-in: %d", r.StatusCode)
	}
	s.post(t, link+"/tg", url.Values{"csrf": {csrf}, "data": {data}})
	_, page = s.get(t, link)
	if strings.Contains(page, "Куда написать") || !strings.Contains(page, "@aigul_cakes") || !strings.Contains(page, "Айгуль") {
		t.Fatal("the Telegram account is not the order's contact")
	}
	o, _ := s.svc.Store.ByToken(t.Context(), strings.TrimPrefix(link, "/o/"))
	if o.Fields["telegram_id"] != "42" || o.Total != 2100*100 {
		t.Fatalf("order: %+v", o.Fields)
	}
}

// A contact typed by hand: a Telegram @name is a contact too.
func TestQuickOrderTypedContact(t *testing.T) {
	s := newSite(t, "openline")
	_, page := s.get(t, "/quick?add=turnkey")
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, page)}, "stamp": {match(t, stampRe, page)}, "quick": {"1"}, "item_turnkey": {"1"}})
	link := r.Header.Get("Location")
	_, page = s.get(t, link)
	csrf := match(t, csrfRe, page)
	if strings.Contains(page, "мес</button>") || strings.Contains(page, "oauth.telegram.org") {
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

func TestTelegramTooOld(t *testing.T) {
	data := tgData("1:T", map[string]any{"id": int64(1), "auth_date": time.Now().Add(-48 * time.Hour).Unix()})
	if _, err := verifyTelegram("1:T", data, time.Now()); err == nil {
		t.Fatal("a two-day-old sign-in was taken")
	}
	if _, err := verifyTelegram("1:OTHER", tgData("1:T", map[string]any{"id": int64(1), "auth_date": time.Now().Unix()}), time.Now()); err == nil {
		t.Fatal("signed by another bot")
	}
}
