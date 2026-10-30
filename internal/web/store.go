package web

import (
	"crypto/hmac"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
)

// The storefront of a shop with photos (pricing.mode shop): a grid of products, a page per product, a cart
// kept in a signed cookie and the checkout with the profile's fields. Everything works without JavaScript;
// the script only adds the gallery swipe and the kitten.

func (s *Server) isShop() bool { return s.cfg.Shop != nil && !s.cfg.Shop.Services }

// isServices: the catalog holds a performer's services (cards with photos of finished work), the orders are
// requests with the price named by the performer.
func (s *Server) isServices() bool { return s.cfg.Shop != nil && s.cfg.Shop.Services }

// hasCatalog: products or services live in the database and the owner edits them.
func (s *Server) hasCatalog() bool { return s.cfg.Shop != nil }

func (s *Server) storeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /p/{slug}", s.productPage)
	mux.HandleFunc("GET /img/{file}", s.photo)
	if s.isShop() {
		mux.HandleFunc("GET /cart", s.cartPage)
		mux.HandleFunc("POST /cart", s.cartUpdate)
	}
}

// ---------------------------------------------------------------- cart cookie

const cartCookie = "jx_cart"

// cart is item id → quantity. The cookie holds "v12:1.v15:2" and a signature, so nobody slips in an item
// the shop never had; prices and stock are always taken from the catalog, never from the cookie.
type cart map[string]int

func (s *Server) readCart(r *http.Request) cart {
	c := cart{}
	ck, err := r.Cookie(cartCookie)
	if err != nil {
		return c
	}
	body, sig, ok := strings.Cut(ck.Value, "~")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign("cart", body))) {
		return c
	}
	for _, part := range strings.Split(body, ".") {
		id, n, ok := strings.Cut(part, ":")
		q, err := strconv.Atoi(n)
		if _, valid := shop.ParseItemID(id); ok && valid && err == nil && q > 0 && q <= 99 && len(c) < 50 {
			c[id] = q
		}
	}
	return c
}

func (s *Server) writeCart(w http.ResponseWriter, r *http.Request, c cart) {
	if len(c) == 0 {
		http.SetCookie(w, &http.Cookie{Name: cartCookie, Value: "", Path: s.cookiePath(), MaxAge: -1, HttpOnly: true,
			Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
		return
	}
	ids := make([]string, 0, len(c))
	for id := range c {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id + ":" + strconv.Itoa(c[id])
	}
	body := strings.Join(parts, ".")
	http.SetCookie(w, &http.Cookie{Name: cartCookie, Value: body + "~" + s.sign("cart", body), Path: s.cookiePath(),
		MaxAge: 30 * 24 * 3600, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
}

func (c cart) count() int {
	n := 0
	for _, q := range c {
		n += q
	}
	return n
}

// ---------------------------------------------------------------- views

type productCard struct {
	*shop.Product
	Label    string // «Последний», «Скидка», «Нет в наличии» …
	LabelHot bool
}

func label(p *shop.Product) (string, bool) {
	if p.Preorder {
		return "Предзаказ", true
	}
	switch n := p.Stock(); {
	case n == 0:
		return "Нет в наличии", false
	case p.OldPrice > 0:
		return "Скидка", true
	case n == 1:
		return "Последний", true
	case n <= 3:
		return "Осталось " + strconv.Itoa(n), false
	case time.Since(p.CreatedAt) < 14*24*time.Hour:
		return "Новинка", false
	}
	return "", false
}

func cards(list []*shop.Product) []productCard {
	out := make([]productCard, len(list))
	for i, p := range list {
		l, hot := label(p)
		out[i] = productCard{Product: p, Label: l, LabelHot: hot}
	}
	return out
}

type storeView struct {
	base
	Products []productCard
	Intro    string
}

func (s *Server) storeFront(w http.ResponseWriter, r *http.Request) {
	list, err := s.cfg.Shop.List(r.Context(), false)
	if err != nil {
		s.log.Error("store list", "err", err)
		s.message(w, r, http.StatusInternalServerError, "Ошибка", "Витрина не загрузилась, обновите страницу.")
		return
	}
	// what can be bought comes first; sold-out pieces stay at the end as a portfolio
	sort.SliceStable(list, func(i, j int) bool { return list[i].InStock() && !list[j].InStock() })
	s.render(w, "store", http.StatusOK, storeView{base: s.base(w, r, s.svc.P.Business.Name), Products: cards(list),
		Intro: s.svc.P.Texts.Intro})
}

type productView struct {
	base
	Product productCard
	Added   bool
	Error   string
	More    []productCard
}

func (s *Server) productPage(w http.ResponseWriter, r *http.Request) {
	if !s.hasCatalog() {
		http.NotFound(w, r)
		return
	}
	p, err := s.cfg.Shop.BySlug(r.Context(), r.PathValue("slug"))
	if err != nil || p.Hidden {
		s.message(w, r, http.StatusNotFound, "Товар не найден", "Возможно, его уже убрали с витрины.")
		return
	}
	s.renderProduct(w, r, p, http.StatusOK, "")
}

func (s *Server) renderProduct(w http.ResponseWriter, r *http.Request, p *shop.Product, code int, msg string) {
	l, hot := label(p)
	if s.isServices() {
		l, hot = "", false // a service has no stock to speak of
	}
	b := s.base(w, r, p.Title)
	if c := p.Cover(); c != nil {
		b.Preview = s.svc.BaseURL() + "/img/" + c.File + ".jpg"
	}
	if d := strings.TrimSpace(p.Description); d != "" {
		b.Summary = clipText(d, 200)
	}
	v := productView{base: b, Product: productCard{Product: p, Label: l, LabelHot: hot},
		Added: r.URL.Query().Get("added") == "1", Error: msg}
	if list, err := s.cfg.Shop.List(r.Context(), false); err == nil {
		for _, o := range list {
			if o.ID != p.ID && (o.InStock() || s.isServices()) && len(v.More) < 4 {
				v.More = append(v.More, productCard{Product: o})
			}
		}
	}
	s.render(w, "product", code, v)
}

// cartUpdate adds a variant from the product page or changes a quantity in the cart.
func (s *Server) cartUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.isShop() {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil || !s.csrfOK(r) {
		s.message(w, r, http.StatusForbidden, "Страница устарела", "Обновите страницу и попробуйте ещё раз.")
		return
	}
	c := s.readCart(r)
	item := r.PostFormValue("item")
	if _, ok := shop.ParseItemID(item); !ok {
		s.redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}
	q, err := strconv.Atoi(r.PostFormValue("qty"))
	if err != nil || q < 0 {
		q = 0
	}
	if r.PostFormValue("add") == "1" {
		p, v, found := s.findVariant(r, item)
		switch {
		case !found:
			s.message(w, r, http.StatusNotFound, "Товар не найден", "Возможно, его уже убрали с витрины.")
		case !p.Preorder && v.Stock < c[item]+max(q, 1):
			msg := "Этого размера больше нет."
			if v.Stock > 0 {
				msg = "В наличии только " + strconv.Itoa(v.Stock) + " шт., и они уже в корзине."
			}
			s.renderProduct(w, r, p, http.StatusConflict, msg)
		default:
			c[item] += max(q, 1)
			s.writeCart(w, r, c)
			s.redirect(w, r, "/p/"+p.Slug+"?added=1", http.StatusSeeOther)
		}
		return
	}
	if q == 0 {
		delete(c, item)
	} else {
		c[item] = min(q, 99)
	}
	s.writeCart(w, r, c)
	s.redirect(w, r, "/cart", http.StatusSeeOther)
}

func (s *Server) findVariant(r *http.Request, item string) (*shop.Product, shop.Variant, bool) {
	id, _ := shop.ParseItemID(item)
	list, err := s.cfg.Shop.List(r.Context(), false)
	if err != nil {
		return nil, shop.Variant{}, false
	}
	for _, p := range list {
		if v, ok := p.Variant(id); ok {
			return p, v, true
		}
	}
	return nil, shop.Variant{}, false
}

type cartLine struct {
	Item    string
	Product *shop.Product
	Variant shop.Variant
	Qty     int
	Sum     money.Amount
	Error   string
}

type cartView struct {
	shopView
	Lines []cartLine
	Sum   money.Amount
}

func (s *Server) cartPage(w http.ResponseWriter, r *http.Request) {
	if !s.isShop() {
		http.NotFound(w, r)
		return
	}
	s.render(w, "cart", http.StatusOK, s.cartView(w, r, s.readCart(r), nil, "", nil))
}

// cartView joins the cart with the catalog: lines whose product is gone are dropped, quantities above the
// stock are flagged next to the line.
func (s *Server) cartView(w http.ResponseWriter, r *http.Request, c cart, form map[string]string, method string, errs order.FieldErrors) cartView {
	v := cartView{shopView: s.shopView(w, r, form, nil, method, errs)}
	v.Title = "Корзина"
	byField := map[string]string{}
	for _, e := range errs {
		byField[e.Field] = e.Msg
	}
	list, err := s.cfg.Shop.List(r.Context(), false)
	if err != nil {
		s.log.Error("cart", "err", err)
	}
	for _, p := range list {
		for _, vr := range p.Variants {
			item := shop.ItemID(vr.ID)
			q := c[item]
			if q == 0 {
				continue
			}
			l := cartLine{Item: item, Product: p, Variant: vr, Qty: q, Sum: p.Price.MulQty(q), Error: byField["item_"+item]}
			if l.Error == "" && q > vr.Stock && !p.Preorder {
				l.Error = "в наличии " + strconv.Itoa(vr.Stock)
				if vr.Stock == 0 {
					l.Error = "этого уже нет в наличии"
				}
			}
			v.Lines = append(v.Lines, l)
			v.Sum += l.Sum
		}
	}
	return v
}

// photo serves a processed photo; the names are random and the files never change, so they are cached for good.
func (s *Server) photo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !s.hasCatalog() || !shop.ValidFile(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(s.cfg.Photos.Dir, name))
}

// shopCreateFailed re-renders the cart with the checkout's mistakes.
func (s *Server) shopCreateFailed(w http.ResponseWriter, r *http.Request, fields map[string]string, method string, fe order.FieldErrors) {
	s.render(w, "cart", http.StatusUnprocessableEntity, s.cartView(w, r, s.readCart(r), fields, method, fe))
}

// clipText shortens a text to n letters on a word boundary, for previews.
func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}
