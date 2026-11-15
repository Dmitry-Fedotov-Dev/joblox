package web

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
)

// What search engines and AI agents read off a site: Schema.org JSON-LD (the store, its products and offers),
// the plain-words facts next to them (stock, city, delivery, payment), sitemap.xml and robots.txt, and an
// IndexNow ping when the owner adds or changes a product, so a new card is found in hours, not weeks.
//
// Everything is built from the same data the page shows, so the markup never says what the page does not.

const indexNowDefault = "https://api.indexnow.org/indexnow"

func (s *Server) seoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /sitemap.xml", s.sitemap)
	mux.HandleFunc("GET /indexnow.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(s.indexNowKey()))
	})
}

// ---------------------------------------------------------------- the seller's facts

// countryOf is the seller's ISO country: the profile's, else guessed from the currency.
func countryOf(b profile.Business) string {
	if c := strings.ToUpper(strings.TrimSpace(b.Country)); len(c) == 2 {
		return c
	}
	return map[string]string{"KGS": "KG", "KZT": "KZ", "RUB": "RU", "UZS": "UZ", "BYN": "BY", "AMD": "AM", "TJS": "TJ"}[strings.ToUpper(b.Currency)]
}

func countryName(code string) string {
	return map[string]string{"KG": "Кыргызстан", "KZ": "Казахстан", "RU": "Россия", "UZ": "Узбекистан", "BY": "Беларусь",
		"AM": "Армения", "TJ": "Таджикистан"}[code]
}

// sellerFacts is the block of plain words a buyer and a crawler both read: where the seller is, how the goods
// come and how they are paid for.
type sellerFacts struct {
	Place    string // «Чолпон-Ата, Кыргызстан»
	Delivery string
	Payment  string // «переводом продавцу или при получении»
}

func (s *Server) sellerFacts() sellerFacts {
	b := s.svc.P.Business
	var place []string
	if c := strings.TrimSpace(b.City); c != "" {
		place = append(place, c)
	}
	if n := countryName(countryOf(b)); n != "" {
		place = append(place, n)
	}
	return sellerFacts{Place: strings.Join(place, ", "), Delivery: strings.TrimSpace(b.Delivery), Payment: s.paymentWords()}
}

// paymentWords says how the buyer pays, from the methods the site really offers now.
func (s *Server) paymentWords() string {
	var transfer, cash bool
	for _, m := range s.svc.Methods() {
		switch m {
		case profile.MethodTransfer:
			transfer = true
		case profile.MethodCash:
			cash = true
		}
	}
	switch {
	case transfer && cash:
		return "переводом напрямую продавцу или при получении"
	case transfer:
		return "переводом напрямую продавцу"
	case cash:
		return "при получении"
	}
	return ""
}

// stockState is a product's availability said once, the same way on the page and in the markup.
type stockState struct {
	Text   string // «В наличии: 3 шт.», «Под заказ», «Нет в наличии»
	Note   string // what it means for the buyer
	Schema string // https://schema.org/ItemAvailability
	Count  int    // pieces ready to ship; 0 for a made-to-order product
	Ready  bool   // can be bought now and shipped from stock
}

func availability(p *shop.Product, food bool) stockState {
	n := p.Stock()
	switch {
	case p.Preorder && food:
		return stockState{Text: "Готовим на заказ", Note: "Приготовим к нужному времени после оформления", Schema: "https://schema.org/MadeToOrder"}
	case p.Preorder && n > 0:
		return stockState{Text: "В наличии: " + strconv.Itoa(n) + " шт.", Note: "Когда закончится, можно заказать: мастер сделает новый",
			Schema: "https://schema.org/InStock", Count: n, Ready: true}
	case p.Preorder:
		return stockState{Text: "Под заказ", Note: "Готового нет: мастер сделает после оформления заказа", Schema: "https://schema.org/MadeToOrder"}
	case n == 1:
		return stockState{Text: "В наличии: 1 шт.", Note: "Последний экземпляр, можно купить сейчас", Schema: "https://schema.org/InStock", Count: 1, Ready: true}
	case n > 0:
		return stockState{Text: "В наличии: " + strconv.Itoa(n) + " шт.", Note: "Можно купить сейчас", Schema: "https://schema.org/InStock", Count: n, Ready: true}
	}
	return stockState{Text: "Нет в наличии", Schema: "https://schema.org/OutOfStock"}
}

// ---------------------------------------------------------------- JSON-LD

type ldNode = map[string]any

// jsonLD renders a graph as a whole <script type="application/ld+json"> element. json.Marshal escapes <, > and
// &, so no text of the owner's can close the script. The element is built here rather than in layout.html: the
// templates then hold no inline script that is not JavaScript, and CI's node --check of inline scripts stays
// meaningful.
func jsonLD(nodes ...ldNode) template.HTML {
	doc := ldNode{"@context": "https://schema.org"}
	if len(nodes) == 1 {
		for k, v := range nodes[0] {
			doc[k] = v
		}
	} else {
		doc["@graph"] = nodes
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	return template.HTML(`<script type="application/ld+json">` + string(b) + `</script>`) //nolint:gosec // HTML-escaped JSON, see above
}

func ldPrice(a money.Amount) string { return strings.TrimSuffix(a.Plain(), ".00") }

// home is the site's absolute front page; the store's @id hangs off it.
func (s *Server) home() string { return s.svc.BaseURL() + "/" }

// storeNode is the seller: name, place, contact, payment and delivery terms.
func (s *Server) storeNode() ldNode {
	b := s.svc.P.Business
	kind := "OnlineStore"
	if s.isServices() {
		kind = "Organization"
	}
	n := ldNode{"@type": kind, "@id": s.home() + "#store", "name": b.Name, "url": s.home()}
	if b.Description != "" {
		n["description"] = b.Description
	}
	if img := s.sitePreview(); img != "" {
		n["image"] = img
	}
	country := countryOf(b)
	if b.City != "" || country != "" {
		addr := ldNode{"@type": "PostalAddress"}
		if b.City != "" {
			addr["addressLocality"] = b.City
		}
		if country != "" {
			addr["addressCountry"] = country
		}
		n["address"] = addr
	}
	if country != "" {
		n["areaServed"] = ldNode{"@type": "Country", "name": country}
	}
	contactLD(n, b.Contact)
	if b.Currency != "" {
		n["currenciesAccepted"] = strings.ToUpper(b.Currency)
	}
	if pay := s.paymentWords(); pay != "" {
		n["paymentAccepted"] = pay
	}
	var terms []string
	if b.Delivery != "" {
		terms = append(terms, "Доставка: "+b.Delivery)
	}
	if pay := s.paymentWords(); pay != "" {
		terms = append(terms, "Оплата: "+pay)
	}
	if len(terms) > 0 {
		n["disambiguatingDescription"] = strings.Join(terms, ". ")
	}
	return n
}

// contactLD puts the owner's free-text contact where it belongs: a phone, an email, a Telegram or a link.
func contactLD(n ldNode, contact string) {
	c := strings.TrimSpace(contact)
	switch {
	case c == "":
	case strings.Contains(c, "@") && strings.Contains(c, ".") && !strings.HasPrefix(c, "@"):
		n["email"] = c
	case strings.HasPrefix(c, "@") && len(c) > 1:
		n["sameAs"] = "https://t.me/" + strings.TrimPrefix(c, "@")
	case strings.HasPrefix(c, "https://"):
		n["sameAs"] = c
	case strings.ContainsAny(c, "0123456789") && strings.Trim(c, "+0123456789 ()-") == "":
		n["telephone"] = c
	}
}

func (s *Server) productURL(p *shop.Product) string { return s.svc.BaseURL() + "/p/" + p.Slug }

func (s *Server) productImages(p *shop.Product) []string {
	var out []string
	for _, im := range append(append([]shop.Image{}, p.Images...), p.Real...) {
		out = append(out, s.svc.BaseURL()+"/img/"+im.File+".jpg")
	}
	return out
}

// offerNode is the price and the availability of a product, sold by the store.
func (s *Server) offerNode(p *shop.Product) ldNode {
	st := availability(p, s.isFood())
	o := ldNode{"@type": "Offer", "url": s.productURL(p), "priceCurrency": strings.ToUpper(s.svc.P.Business.Currency),
		"price": ldPrice(p.Price), "availability": st.Schema, "itemCondition": "https://schema.org/NewCondition",
		"seller": ldNode{"@id": s.home() + "#store"}}
	if st.Count > 0 {
		o["inventoryLevel"] = ldNode{"@type": "QuantitativeValue", "value": st.Count}
	}
	if c := countryOf(s.svc.P.Business); c != "" {
		o["areaServed"] = ldNode{"@type": "Country", "name": c}
	}
	return o
}

// productNode is a product with its offer; a service is a Service with a price «от», or none by agreement.
func (s *Server) productNode(p *shop.Product) ldNode {
	n := ldNode{"@id": s.productURL(p) + "#item", "url": s.productURL(p), "name": p.Title}
	if d := strings.TrimSpace(p.Description); d != "" {
		n["description"] = d
	}
	if imgs := s.productImages(p); len(imgs) > 0 {
		n["image"] = imgs
	}
	if s.isServices() {
		n["@type"] = "Service"
		n["provider"] = ldNode{"@id": s.home() + "#store"}
		if c := countryOf(s.svc.P.Business); c != "" {
			n["areaServed"] = ldNode{"@type": "Country", "name": c}
		}
		if p.Price > 0 {
			n["offers"] = ldNode{"@type": "Offer", "url": s.productURL(p), "priceCurrency": strings.ToUpper(s.svc.P.Business.Currency),
				"priceSpecification": ldNode{"@type": "PriceSpecification", "minPrice": ldPrice(p.Price),
					"priceCurrency": strings.ToUpper(s.svc.P.Business.Currency)}}
		}
		return n
	}
	n["@type"] = "Product"
	n["sku"] = p.Slug
	n["productID"] = strconv.FormatInt(p.ID, 10)
	n["brand"] = ldNode{"@type": "Brand", "name": s.svc.P.Business.Name}
	n["offers"] = s.offerNode(p)
	var vs []string
	for _, v := range p.Variants {
		if v.Name != "" {
			vs = append(vs, v.Name)
		}
	}
	if len(vs) > 0 {
		n["additionalProperty"] = ldNode{"@type": "PropertyValue", "name": "Варианты", "value": strings.Join(vs, ", ")}
	}
	return n
}

// storeLD is the front page's markup: the store and the list of what it sells.
func (s *Server) storeLD(list []*shop.Product) template.HTML {
	store := s.storeNode()
	var items []ldNode
	for i, p := range list {
		items = append(items, ldNode{"@type": "ListItem", "position": i + 1, "url": s.productURL(p), "item": s.productNode(p)})
	}
	if len(items) == 0 {
		return jsonLD(store)
	}
	name := "Товары"
	if s.isServices() {
		name = "Услуги"
	} else if s.isFood() {
		name = "Меню"
	}
	return jsonLD(store, ldNode{"@type": "ItemList", "@id": s.home() + "#catalog", "name": name, "numberOfItems": len(items),
		"itemListElement": items})
}

// productLD is a product page's markup: the product, its offer and the store that sells it.
func (s *Server) productLD(p *shop.Product) template.HTML {
	return jsonLD(s.productNode(p), s.storeNode())
}

// ---------------------------------------------------------------- robots.txt, sitemap.xml

// indexable: a real site at its own address. A demo's goods are not for sale, so it keeps out of search.
func (s *Server) indexable() bool { return !s.cfg.Demo }

func (s *Server) robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if !s.indexable() {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
		return
	}
	p := s.cfg.Prefix
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: " + p + "/\n")
	for _, d := range []string{"/owner", "/admin", "/o/", "/cart", "/quick", "/dev/"} {
		b.WriteString("Disallow: " + p + d + "\n")
	}
	b.WriteString("\nSitemap: " + s.svc.BaseURL() + "/sitemap.xml\n")
	_, _ = w.Write([]byte(b.String()))
}

type sitemapURL struct {
	Loc    string         `xml:"loc"`
	Images []sitemapImage `xml:"image:image,omitempty"`
}

type sitemapImage struct {
	Loc string `xml:"image:loc"`
}

func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	if !s.indexable() {
		http.NotFound(w, r)
		return
	}
	urls := []sitemapURL{{Loc: s.home()}}
	if s.hasCatalog() {
		list, err := s.cfg.Shop.List(r.Context(), false)
		if err != nil {
			s.log.Error("sitemap", "err", err)
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		for _, p := range list {
			if p.Hidden {
				continue
			}
			u := sitemapURL{Loc: s.productURL(p)}
			for _, img := range s.productImages(p) {
				u.Images = append(u.Images, sitemapImage{Loc: img})
			}
			urls = append(urls, u)
		}
	}
	urls = append(urls, sitemapURL{Loc: s.svc.BaseURL() + "/terms"})
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:image="http://www.google.com/schemas/sitemap-image/1.1">` + "\n")
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	for _, u := range urls {
		if err := enc.EncodeElement(u, xml.StartElement{Name: xml.Name{Local: "url"}}); err != nil {
			s.log.Error("sitemap", "err", err)
			return
		}
	}
	_ = enc.Flush()
	buf.WriteString("\n</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write(buf.Bytes())
}

// ---------------------------------------------------------------- IndexNow

// indexNowKey is the site's IndexNow key, served at /indexnow.txt: stable for the installation, derived from
// its secret, so nothing new is kept.
func (s *Server) indexNowKey() string { return s.sign("indexnow") }

// indexNowEndpoint is where the pings go; empty: pings are off for this site (a demo, a temporary tunnel
// address, a local run or JOBLOX_INDEXNOW=off).
func (s *Server) indexNowEndpoint() string {
	if !s.indexable() || s.cfg.IndexNow == "off" || s.cfg.Dev {
		return ""
	}
	u, err := url.Parse(s.svc.BaseURL())
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.HasSuffix(u.Hostname(), ".trycloudflare.com") ||
		u.Hostname() == "localhost" {
		return ""
	}
	if s.cfg.IndexNow != "" {
		return s.cfg.IndexNow
	}
	return indexNowDefault
}

// pingIndexNow tells Bing, Yandex and the other IndexNow engines that these pages changed. It runs in the
// background and never holds the owner's page; a failure is only logged.
func (s *Server) pingIndexNow(urls ...string) {
	endpoint := s.indexNowEndpoint()
	if endpoint == "" || len(urls) == 0 {
		return
	}
	u, _ := url.Parse(s.svc.BaseURL())
	body, err := json.Marshal(map[string]any{"host": u.Host, "key": s.indexNowKey(), "keyLocation": s.svc.BaseURL() + "/indexnow.txt",
		"urlList": urls})
	if err != nil {
		return
	}
	s.pings.Add(1)
	go func() {
		defer s.pings.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			s.log.Info("indexnow", "err", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			s.log.Info("indexnow", "status", resp.StatusCode)
		}
	}()
}

// pingProduct announces a product page and the front page that lists it.
func (s *Server) pingProduct(p *shop.Product) {
	if p == nil {
		return
	}
	s.pingIndexNow(s.productURL(p), s.home())
}
