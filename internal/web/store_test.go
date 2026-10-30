package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/metrics"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/store"
)

const testOwnerKey = "owner-key-0123456789abcdef0123456789"

var price450 = strings.TrimSuffix(money.Amount(450000).String(), ".00") // "4 500" with the thin space

// shopSite is the shop profile with the photo catalog and the owner's area, as `joblox serve` runs it with
// JOBLOX_OWNER_KEY.
func shopSite(t *testing.T) (*site, *shop.Catalog) { return catalogSite(t, "shop") }

// catalogSite runs a preset whose products or services live in the database: shop.yaml or master.yaml.
func catalogSite(t *testing.T, preset string) (*site, *shop.Catalog) {
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
	cat, err := shop.Open(context.Background(), st.DB())
	if err != nil {
		t.Fatal(err)
	}
	box := &mail.Outbox{}
	req := &order.Requisites{Recipient: "ИП Тестова", Lines: []order.RequisiteRow{{Label: "Карта", Value: "4000 0000 0000 0002"}}}
	svc := order.New(p, st, box, req, []byte("secret"), "http://shop.test")
	if p.ServiceCatalog != nil {
		cat.Services = true
		if err := shop.SeedServices(context.Background(), cat, p.Services); err != nil {
			t.Fatal(err)
		}
	} else {
		svc.Stock = cat
	}
	rec := metrics.New()
	svc.Observe = rec
	owner := &Owner{Key: testOwnerKey, Configured: func() bool { return true }, Shown: func() bool { return false },
		SetShown: func(bool) error { return nil }}
	srv, err := New(svc, rec, box, []byte("secret"), Config{Dev: true, Version: "test", Owner: owner, Shop: cat,
		Photos: &shop.Photos{Dir: t.TempDir()}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &site{srv: srv, http: httptest.NewServer(srv.Handler()), admin: httptest.NewServer(srv.AdminHandler()), box: box, svc: svc}
	jar, _ := cookiejar.New(nil)
	s.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() { s.http.Close(); s.admin.Close(); _ = st.Close() })
	return s, cat
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 300, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 300; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y / 2), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The owner adds a product with a photo from the browser; it shows up on the storefront with its own page.
func TestOwnerAddsProductWithPhoto(t *testing.T) {
	s, cat := shopSite(t)
	if code, _ := s.get(t, "/owner/catalog"); code != http.StatusNotFound {
		t.Fatalf("the catalog without the master link: %d", code)
	}
	s.get(t, "/owner/enter?k="+testOwnerKey)
	_, page := s.get(t, "/owner/catalog/new")
	csrf := match(t, csrfRe, page)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range map[string]string{"csrf": csrf, "title": "Платье «Лён»", "price": "4 500", "description": "Лён",
		"v_name_0": "44", "v_stock_0": "1", "v_name_1": "46", "v_stock_1": "2", "v_name_2": "", "v_stock_2": ""} {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("photos", "IMG_0001.jpg")
	_, _ = fw.Write(testJPEG(t))
	bad, _ := mw.CreateFormFile("photos", "notes.txt")
	_, _ = bad.Write([]byte("not a photo"))
	_ = mw.Close()
	resp, err := s.client.Post(s.http.URL+uploadPath, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/owner/catalog/1?saved=1") || !strings.Contains(loc, "notes.txt") {
		t.Fatalf("save: %d %s", resp.StatusCode, loc)
	}
	p, err := cat.BySlug(context.Background(), "plate-len")
	if err != nil || len(p.Images) != 1 || len(p.Variants) != 2 || p.Price != 450000 {
		t.Fatalf("saved product: %+v %v", p, err)
	}

	code, home := s.get(t, "/")
	if code != http.StatusOK || !strings.Contains(home, `href="/p/plate-len"`) || !strings.Contains(home, price450) ||
		!strings.Contains(home, "/img/"+p.Images[0].File+"_t.jpg") {
		t.Fatalf("storefront: %d", code)
	}
	if code, _ := s.get(t, "/img/"+p.Images[0].File+".jpg"); code != http.StatusOK {
		t.Fatalf("photo: %d", code)
	}
	if code, _ := s.get(t, "/img/..%2Fjoblox.db"); code != http.StatusNotFound {
		t.Fatalf("a path outside the photos: %d", code)
	}
	if code, page := s.get(t, "/p/plate-len"); code != http.StatusOK || !strings.Contains(page, `value="v`) {
		t.Fatalf("product page: %d", code)
	}

	// hidden products leave the storefront and their page
	_, list := s.get(t, "/owner/catalog")
	s.post(t, "/owner/catalog/1/hide", url.Values{"csrf": {match(t, csrfRe, list)}})
	if _, home := s.get(t, "/"); strings.Contains(home, "plate-len") {
		t.Fatal("a hidden product is on the storefront")
	}
	if code, _ := s.get(t, "/p/plate-len"); code != http.StatusNotFound {
		t.Fatalf("hidden product page: %d", code)
	}
}

var itemRe = regexp.MustCompile(`name="item" value="(v\d+)"`)

// A customer puts the last dress into the cart and orders it: the stock is reserved, the cart is emptied, and
// a second customer with the same dress in the cart is told it is gone.
func TestCartToOrder(t *testing.T) {
	s, cat := shopSite(t)
	ctx := context.Background()
	if _, err := cat.Save(ctx, 0, shop.Input{Title: "Платье", Price: 450000, Variants: []shop.VariantInput{{Name: "44", Stock: 1}}}); err != nil {
		t.Fatal(err)
	}
	other := &site{http: s.http, svc: s.svc}
	jar, _ := cookiejar.New(nil)
	other.client = &http.Client{Jar: jar, CheckRedirect: s.client.CheckRedirect}

	for _, c := range []*site{s, other} {
		_, page := c.get(t, "/p/plate")
		r, _ := c.post(t, "/cart", url.Values{"csrf": {match(t, csrfRe, page)}, "add": {"1"}, "qty": {"1"}, "item": {match(t, itemRe, page)}})
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/p/plate?added=1" {
			t.Fatalf("add to cart: %d %s", r.StatusCode, r.Header.Get("Location"))
		}
	}
	// a second one of the same size is more than there is
	_, page := s.get(t, "/p/plate")
	r, _ := s.post(t, "/cart", url.Values{"csrf": {match(t, csrfRe, page)}, "add": {"1"}, "qty": {"1"}, "item": {match(t, itemRe, page)}})
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("over the stock: %d", r.StatusCode)
	}

	checkout := func(c *site, email string) reply {
		_, cart := c.get(t, "/cart")
		if !strings.Contains(cart, "Размер 44") || !strings.Contains(cart, price450) {
			t.Fatal("cart page without the line")
		}
		r, _ := c.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, cart)}, "stamp": {match(t, stampRe, cart)},
			"f_name": {"Анна"}, "f_phone": {"+996 700 000 000"}, "f_email": {email}, "f_delivery": {"Самовывоз со склада"},
			"method": {profile.MethodTransfer}})
		return r
	}
	s.srv.svc.Now = func() time.Time { return time.Now().Add(time.Minute) } // the form was filled long enough ago
	if r := checkout(s, "anna@example.com"); r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/o/") {
		t.Fatalf("order: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if _, cart := s.get(t, "/cart"); !strings.Contains(cart, "Пока пусто") {
		t.Fatal("the cart is not emptied after the order")
	}
	if _, home := s.get(t, "/"); !strings.Contains(home, "Нет в наличии") {
		t.Fatal("the sold dress is not marked")
	}
	if r := checkout(other, "boris@example.com"); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second buyer: %d", r.StatusCode)
	}
	if _, cart := other.get(t, "/cart"); !strings.Contains(cart, "этого уже нет в наличии") {
		t.Fatal("the second buyer is not told the dress is gone")
	}
}

// The cart cookie is signed: a forged one is ignored.
func TestCartCookieSigned(t *testing.T) {
	s, _ := shopSite(t)
	u, _ := url.Parse(s.http.URL)
	s.client.Jar.SetCookies(u, []*http.Cookie{{Name: cartCookie, Value: "v1:5~forged"}})
	if _, cart := s.get(t, "/cart"); !strings.Contains(cart, "Пока пусто") {
		t.Fatal("a forged cart was accepted")
	}
}

// A site under a path (https://joblox.openline.one/demo-shop) keeps every link, redirect and cookie there.
func TestUnderPrefix(t *testing.T) {
	s, cat := shopSite(t)
	s.srv.cfg.Prefix = "/demo-shop"
	s.http.Config.Handler = s.srv.Handler()
	if _, err := cat.Save(context.Background(), 0, shop.Input{Title: "Платье", Price: 450000, Variants: []shop.VariantInput{{Stock: 2}}}); err != nil {
		t.Fatal(err)
	}
	if code, _ := s.get(t, "/demo-shop"); code != http.StatusMovedPermanently {
		t.Fatalf("bare prefix: %d", code)
	}
	code, home := s.get(t, "/demo-shop/")
	if code != http.StatusOK || !strings.Contains(home, `href="/demo-shop/p/plate"`) || !strings.Contains(home, `href="/demo-shop/static/style.css?v=`) ||
		!strings.Contains(home, `src="/demo-shop/static/kittens/`) || strings.Contains(home, `href="/p/`) {
		t.Fatalf("links under the prefix: %d", code)
	}
	_, page := s.get(t, "/demo-shop/p/plate")
	r, _ := s.post(t, "/demo-shop/cart", url.Values{"csrf": {match(t, csrfRe, page)}, "add": {"1"}, "qty": {"1"}, "item": {match(t, itemRe, page)}})
	if r.Header.Get("Location") != "/demo-shop/p/plate?added=1" {
		t.Fatalf("redirect: %s", r.Header.Get("Location"))
	}
	if _, cart := s.get(t, "/demo-shop/cart"); !strings.Contains(cart, "Платье") {
		t.Fatal("the cart cookie does not live under the prefix")
	}
	if code, _ := s.get(t, "/healthz"); code != http.StatusOK {
		t.Fatalf("health at the root: %d", code)
	}
	if code, _ := s.get(t, "/cart"); code != http.StatusNotFound {
		t.Fatalf("the site answers outside its prefix: %d", code)
	}
}

// A cover picture next to the profile is served at /cover and shown at the top of the site.
func TestCover(t *testing.T) {
	s, _ := shopSite(t)
	file := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(file, testJPEG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	s.srv.cfg.Cover = file
	s.http.Config.Handler = s.srv.Handler()
	if _, home := s.get(t, "/"); !strings.Contains(home, `class="hero-cover" src="/cover?v=`) {
		t.Fatal("no cover on the storefront")
	}
	if code, _ := s.get(t, "/cover"); code != http.StatusOK {
		t.Fatalf("cover: %d", code)
	}
}

// Real photos stay off the storefront's gallery and open with the round button on the product page.
func TestRealPhotos(t *testing.T) {
	s, cat := shopSite(t)
	ctx := context.Background()
	id, err := cat.Save(ctx, 0, shop.Input{Title: "Грибок", Price: 100000, Variants: []shop.VariantInput{{Stock: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddImage(ctx, id, shop.Image{File: "aaaaaaaaaaaaaaaaaaaaaaaa", W: 10, H: 10}); err != nil {
		t.Fatal(err)
	}
	if code, page := s.get(t, "/p/gribok"); code != http.StatusOK || strings.Contains(page, `class="real-btn"`) {
		t.Fatal("a button without real photos")
	}
	if err := cat.AddImage(ctx, id, shop.Image{File: "bbbbbbbbbbbbbbbbbbbbbbbb", W: 10, H: 10, Real: true}); err != nil {
		t.Fatal(err)
	}
	_, page := s.get(t, "/p/gribok")
	if !strings.Contains(page, `href="#real"`) || !strings.Contains(page, "Посмотреть настоящее изображение") ||
		!strings.Contains(page, "/img/bbbbbbbbbbbbbbbbbbbbbbbb.jpg") {
		t.Fatal("no real photos on the product page")
	}
	p, _ := cat.Get(ctx, id)
	if len(p.Images) != 1 || len(p.Real) != 1 || p.Cover().File != "aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("galleries: %+v %+v", p.Images, p.Real)
	}
}

// Link previews: the site's preview picture and a product's own cover and text.
func TestLinkPreviews(t *testing.T) {
	s, cat := shopSite(t)
	og := filepath.Join(t.TempDir(), "og.jpg")
	if err := os.WriteFile(og, testJPEG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	s.srv.cfg.OGImage = og
	s.http.Config.Handler = s.srv.Handler()
	_, home := s.get(t, "/")
	if !strings.Contains(home, `<meta property="og:image" content="http://shop.test/og?v=`) || !strings.Contains(home, `og:title`) {
		t.Fatal("no site preview")
	}
	if code, _ := s.get(t, "/og"); code != http.StatusOK {
		t.Fatalf("og picture: %d", code)
	}
	ctx := context.Background()
	id, _ := cat.Save(ctx, 0, shop.Input{Title: "Грибок", Description: "Вязаная подвеска", Price: 100000, Variants: []shop.VariantInput{{Stock: 1}}})
	_ = cat.AddImage(ctx, id, shop.Image{File: "cccccccccccccccccccccccc", W: 10, H: 10})
	_, page := s.get(t, "/p/gribok")
	if !strings.Contains(page, `content="http://shop.test/img/cccccccccccccccccccccccc.jpg"`) || !strings.Contains(page, `og:description" content="Вязаная подвеска"`) {
		t.Fatal("no product preview")
	}
	resp, err := s.admin.Client().Get(s.admin.URL + "/admin")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the dashboard: %v %v", resp, err)
	}
	resp.Body.Close()
}

// A preorder sells without stock: the button says «Предзаказ», the order goes through, nothing is reserved.
func TestPreorder(t *testing.T) {
	s, cat := shopSite(t)
	ctx := context.Background()
	id, err := cat.Save(ctx, 0, shop.Input{Title: "Зайчик", Price: 250000, Preorder: true, Variants: []shop.VariantInput{{Stock: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	_, home := s.get(t, "/")
	if !strings.Contains(home, ">Предзаказ<") || strings.Contains(home, "Нет в наличии") || !strings.Contains(home, "/static/spin/spin-") {
		t.Fatal("the preorder is not marked on the storefront")
	}
	_, page := s.get(t, "/p/zaychik")
	if !strings.Contains(page, ">Предзаказ</button>") {
		t.Fatal("no «Предзаказ» button")
	}
	r, _ := s.post(t, "/cart", url.Values{"csrf": {match(t, csrfRe, page)}, "add": {"1"}, "qty": {"1"}, "item": {match(t, itemRe, page)}})
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("add a preorder: %d", r.StatusCode)
	}
	s.srv.svc.Now = func() time.Time { return time.Now().Add(time.Minute) }
	_, cart := s.get(t, "/cart")
	r, _ = s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, cart)}, "stamp": {match(t, stampRe, cart)},
		"f_name": {"Анна"}, "f_phone": {"+996 700 000 000"}, "f_email": {"anna@example.com"}, "f_delivery": {"Самовывоз со склада"},
		"method": {profile.MethodTransfer}})
	if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/o/") {
		t.Fatalf("order a preorder: %d", r.StatusCode)
	}
	if p, _ := cat.Get(ctx, id); p.Variants[0].Stock != 0 {
		t.Fatalf("a preorder touched the stock: %d", p.Variants[0].Stock)
	}
}

// «Я оплатил»: the customer says the money went; the order shows it is being checked, nothing is confirmed.
func TestReportPaid(t *testing.T) {
	s, cat := shopSite(t)
	ctx := context.Background()
	if _, err := cat.Save(ctx, 0, shop.Input{Title: "Платье", Price: 450000, Variants: []shop.VariantInput{{Stock: 2}}}); err != nil {
		t.Fatal(err)
	}
	_, page := s.get(t, "/p/plate")
	s.post(t, "/cart", url.Values{"csrf": {match(t, csrfRe, page)}, "add": {"1"}, "qty": {"1"}, "item": {match(t, itemRe, page)}})
	s.srv.svc.Now = func() time.Time { return time.Now().Add(time.Minute) }
	_, cart := s.get(t, "/cart")
	r, _ := s.post(t, "/order", url.Values{"csrf": {match(t, csrfRe, cart)}, "stamp": {match(t, stampRe, cart)},
		"f_name": {"Анна"}, "f_phone": {"+996 700 000 000"}, "f_email": {"anna@example.com"}, "f_delivery": {"Самовывоз со склада"},
		"method": {profile.MethodTransfer}})
	link := r.Header.Get("Location")
	// the email code confirmed and the anti-fraud check passed, as the customer and the owner would
	o, err := s.svc.Store.ByToken(ctx, strings.TrimPrefix(link, "/o/"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	o.VerifiedAt, o.Approved = &now, true
	if err := s.svc.Store.Save(ctx, o, order.Event{}); err != nil {
		t.Fatal(err)
	}
	_, page = s.get(t, link)
	if !strings.Contains(page, ">Я оплатил</button>") || !strings.Contains(page, `class="track"`) {
		t.Fatal("no «Я оплатил» or no track on the order page")
	}
	s.post(t, link+"/paid", url.Values{"csrf": {match(t, csrfRe, page)}})
	_, page = s.get(t, link)
	if !strings.Contains(page, "Проверяем оплату") || strings.Contains(page, ">Я оплатил</button>") {
		t.Fatal("the report is not shown")
	}
	if o, _ = s.svc.Store.ByToken(ctx, strings.TrimPrefix(link, "/o/")); o.Paid || o.ReportedAt == nil {
		t.Fatalf("report: paid=%v reported=%v", o.Paid, o.ReportedAt)
	}
}
