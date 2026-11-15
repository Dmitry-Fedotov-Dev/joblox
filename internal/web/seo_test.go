package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
)

var ldRe = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// ld is the page's JSON-LD graph, node by @type.
func ld(t *testing.T, page string) map[string]map[string]any {
	t.Helper()
	m := ldRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no JSON-LD on the page")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
		t.Fatalf("JSON-LD does not parse: %v\n%s", err, m[1])
	}
	nodes := []any{doc}
	if g, ok := doc["@graph"].([]any); ok {
		nodes = g
	}
	out := map[string]map[string]any{}
	for _, n := range nodes {
		node := n.(map[string]any)
		out[node["@type"].(string)] = node
	}
	return out
}

func seoShop(t *testing.T) (*site, *shop.Catalog) {
	s, cat := shopSite(t)
	b := &s.svc.P.Business
	b.City, b.Delivery = "Чолпон-Ата", "по Иссык-Кулю и почтой по стране"
	ctx := context.Background()
	for _, in := range []shop.Input{
		{Title: "Грибок", Description: "Вязаный, 8 × 3,5 см. </script><b>не тег</b>", Price: 100000, Variants: []shop.VariantInput{{Stock: 1}}},
		{Title: "Зайчик", Price: 145000, Preorder: true, Variants: []shop.VariantInput{{Stock: 0}}},
		{Title: "Секрет", Price: 1000, Hidden: true, Variants: []shop.VariantInput{{Stock: 3}}},
	} {
		if _, err := cat.Save(ctx, 0, in); err != nil {
			t.Fatal(err)
		}
	}
	return s, cat
}

func TestProductStructuredData(t *testing.T) {
	s, _ := seoShop(t)
	_, page := s.get(t, "/p/gribok")
	g := ld(t, page)
	p, store := g["Product"], g["OnlineStore"]
	if p == nil || store == nil {
		t.Fatalf("want a Product and the store, got %v", g)
	}
	offer := p["offers"].(map[string]any)
	if offer["price"] != "1000" || offer["priceCurrency"] != "RUB" || offer["availability"] != "https://schema.org/InStock" {
		t.Fatalf("offer: %v", offer)
	}
	if offer["seller"].(map[string]any)["@id"] != store["@id"] {
		t.Fatal("the offer's seller is not the store")
	}
	addr := store["address"].(map[string]any)
	if addr["addressLocality"] != "Чолпон-Ата" || addr["addressCountry"] != "RU" {
		t.Fatalf("address: %v", addr)
	}
	if !strings.Contains(store["disambiguatingDescription"].(string), "Иссык-Кулю") || store["paymentAccepted"] == "" {
		t.Fatalf("delivery and payment are not in the markup: %v", store)
	}
	if !strings.Contains(p["description"].(string), "</script>") || strings.Count(page, "</script>") != strings.Count(page, "<script") {
		t.Fatal("the owner's text broke out of the script")
	}
	for _, want := range []string{"В наличии: 1 шт.", "Последний экземпляр", "<dt>Где</dt><dd>Чолпон-Ата, Россия</dd>",
		"<dt>Доставка</dt>", "<dt>Оплата</dt>", `rel="canonical" href="http://shop.test/p/gribok"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the product page has no %q", want)
		}
	}

	_, page = s.get(t, "/p/zaychik")
	if a := ld(t, page)["Product"]["offers"].(map[string]any)["availability"]; a != "https://schema.org/MadeToOrder" {
		t.Fatalf("a preorder is %v", a)
	}
	if !strings.Contains(page, "<b>Под заказ</b>") {
		t.Fatal("the preorder's page does not say it is made to order")
	}
}

func TestStorefrontSaysStockApart(t *testing.T) {
	s, _ := seoShop(t)
	_, home := s.get(t, "/")
	if !strings.Contains(home, "<b>1</b> товар в наличии") || !strings.Contains(home, "<b>1</b> под заказ") {
		t.Fatal("the storefront counts made-to-order goods as in stock")
	}
	if strings.Contains(home, `alt="Готовится под заказ"`) {
		t.Fatal("the decorative picture still speaks")
	}
	// the label follows the name in the text, so «Последний» is not read as the next card's
	if i, j := strings.Index(home, ">Грибок<"), strings.Index(home, ">Последний<"); i < 0 || j < i {
		t.Fatal("the label comes before the card's name")
	}
	list := ld(t, home)["ItemList"]
	if list == nil || list["numberOfItems"].(float64) != 2 {
		t.Fatalf("item list: %v", list)
	}
	if strings.Contains(home, "Секрет") {
		t.Fatal("a hidden product leaked")
	}
}

func TestRobotsAndSitemap(t *testing.T) {
	s, _ := seoShop(t)
	_, robots := s.get(t, "/robots.txt")
	for _, want := range []string{"Disallow: /owner", "Disallow: /cart", "Sitemap: http://shop.test/sitemap.xml"} {
		if !strings.Contains(robots, want) {
			t.Fatalf("robots.txt has no %q:\n%s", want, robots)
		}
	}
	code, sm := s.get(t, "/sitemap.xml")
	if code != http.StatusOK || !strings.Contains(sm, "<loc>http://shop.test/p/gribok</loc>") ||
		!strings.Contains(sm, "<loc>http://shop.test/p/zaychik</loc>") || strings.Contains(sm, "sekret") {
		t.Fatalf("sitemap:\n%s", sm)
	}
	_, cart := s.get(t, "/cart")
	if !strings.Contains(cart, `<meta name="robots" content="noindex">`) {
		t.Fatal("the cart is open to search")
	}

	s.srv.cfg.Demo = true
	if _, robots = s.get(t, "/robots.txt"); !strings.Contains(robots, "Disallow: /\n") {
		t.Fatal("a demo is open to search")
	}
	if code, _ = s.get(t, "/sitemap.xml"); code != http.StatusNotFound {
		t.Fatalf("a demo's sitemap: %d", code)
	}
}

func TestIndexNow(t *testing.T) {
	s, cat := seoShop(t)
	var mu sync.Mutex
	var got map[string]any
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer engine.Close()

	p, err := cat.BySlug(context.Background(), "gribok")
	if err != nil {
		t.Fatal(err)
	}
	s.srv.cfg.IndexNow = engine.URL
	s.srv.pingProduct(p) // a local run (Dev) at http://: never pings
	s.srv.pings.Wait()
	if got != nil {
		t.Fatal("a local site pinged")
	}

	s.srv.cfg.Dev = false
	s.svc.SetBaseURL("https://valya.free-sellers.com")
	s.srv.pingProduct(p)
	s.srv.pings.Wait()
	mu.Lock()
	defer mu.Unlock()
	if got["host"] != "valya.free-sellers.com" || got["key"] != s.srv.indexNowKey() ||
		got["keyLocation"] != "https://valya.free-sellers.com/indexnow.txt" {
		t.Fatalf("ping: %v", got)
	}
	if urls := got["urlList"].([]any); len(urls) != 2 || urls[0] != "https://valya.free-sellers.com/p/gribok" {
		t.Fatalf("urls: %v", urls)
	}
	if _, key := s.get(t, "/indexnow.txt"); key != s.srv.indexNowKey() {
		t.Fatal("the key file does not hold the key")
	}

	for _, base := range []string{"https://abc-def.trycloudflare.com", "http://valya.free-sellers.com"} {
		s.svc.SetBaseURL(base)
		if s.srv.indexNowEndpoint() != "" {
			t.Fatalf("%s would ping", base)
		}
	}
}
