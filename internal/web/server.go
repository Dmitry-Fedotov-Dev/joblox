// Package web serves the storefront, the checkout, the customer's order page and, on a separate
// local-only listener, the operator's dashboard. Pages are rendered on the server; the little JavaScript
// there is only refreshes the order status and keeps the online counter, and every form works without it.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/kitten"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/metrics"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Config struct {
	Trusted       []netip.Prefix // proxies whose X-Forwarded-For is believed
	SecureCookies bool           // the site is served over HTTPS
	Dev           bool           // local run: shows the outbox at /dev/outbox instead of sending mail
	Version       string
	Owner         *Owner // the owner's area on the public site (`joblox up`); nil on servers run by hand
	// CallURL is a voice service that pairs two people by a room in the link (Контакт, https://openline.one/):
	// an open order gets a "call" button for the customer and the performer. Empty: no button.
	CallURL string
	// Shop and Photos are the catalog with photos of a pricing.mode shop profile; nil otherwise.
	Shop   *shop.Catalog
	Photos *shop.Photos
	Demo   bool // a showcase site: a banner on every page says orders will not be fulfilled
	// Prefix is the path the site lives under ("/demo-shop"), taken from the base address; empty at the root.
	Prefix string
	// Cover is the file of the profile's cover picture, served at /cover; empty: no cover.
	Cover string
	// OGImage is the file of the link preview picture, served at /og; empty: the cover or nothing.
	OGImage string
	// MarketURL is the network's market where anyone can start the same kind of site; empty: no button.
	MarketURL string
	// Bot connects the operator bot from the cabinet; nil when the server's environment sets the bot.
	Bot BotPanel
}

type Server struct {
	svc     *order.Service
	rec     *metrics.Recorder
	outbox  *mail.Outbox // only in Dev
	cfg     Config
	secret  []byte
	tpl     map[string]*template.Template
	log     *slog.Logger
	general *Limiter // every request
	orders  *Limiter // checkout submits
	codes   *Limiter // email code tries
}

func New(svc *order.Service, rec *metrics.Recorder, outbox *mail.Outbox, secret []byte, cfg Config, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{svc: svc, rec: rec, outbox: outbox, cfg: cfg, secret: secret, log: log,
		general: NewLimiter(20, 60), orders: NewLimiter(0.1, 5), codes: NewLimiter(0.2, 8)}
	funcs := template.FuncMap{
		"money": func(a money.Amount) string { return a.String() },
		// price is a shop price: whole sums without the kopecks, "4 500" rather than "4 500.00"
		"price": func(a money.Amount) string { return strings.TrimSuffix(a.String(), ".00") },
		"cur":   currencySign,
		"join":  strings.Join,
		"inc":   func(i int) int { return i + 1 },
		"sign":  func() string { return currencySign(svc.P.Business.Currency) },
		"mascot": func(id int64) template.HTML {
			pic := mascot(id)
			if s.svc.P.Business.Placeholder == "knit" {
				pic = knit(id)
			}
			return template.HTML(strings.Replace(string(pic), `src="/`, `src="`+s.cfg.Prefix+`/`, 1))
		},
		"yarn": func(id int64) template.HTML {
			if s.svc.P.Business.Placeholder != "knit" {
				return ""
			}
			return template.HTML(strings.ReplaceAll(string(yarn(id)), `src="/`, `src="`+s.cfg.Prefix+`/`))
		},
		// yarnPage frames the whole storefront of a knitting shop: the needles in its top right corner, the thread
		// round the page to one ball of yarn for the kitten; the cards then only knit
		"yarnPage": func() template.HTML {
			if s.svc.P.Business.Placeholder != "knit" {
				return ""
			}
			h := strings.Replace(string(yarn(7)), `class="yarn `, `class="yarn page-yarn `, 1)
			h = strings.Replace(h, `width="84" height="90"`, `width="112" height="120"`, 1)
			return template.HTML(strings.ReplaceAll(h, `src="/`, `src="`+s.cfg.Prefix+`/`))
		},
		"spin": func(id int64) template.HTML {
			return template.HTML(strings.ReplaceAll(string(spin(id)), `src="/`, `src="`+s.cfg.Prefix+`/`))
		},
		"asset": func(name string) string { return s.cfg.Prefix + asset(name) },
		"p":     func() string { return s.cfg.Prefix },
		"whole": whole,
		"initial": func(s string) string {
			for _, r := range s {
				return string(unicode.ToUpper(r))
			}
			return ""
		},
		"plural": func(n int, one, few, many string) string {
			n %= 100
			switch {
			case n%10 == 1 && n != 11:
				return one
			case n%10 >= 2 && n%10 <= 4 && (n < 12 || n > 14):
				return few
			}
			return many
		},
	}
	s.tpl = map[string]*template.Template{}
	for _, page := range []string{"shop", "order", "message", "admin", "owner", "terms", "store", "product", "cart", "catalog", "item"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.tpl[page] = t
	}
	return s, nil
}

func currencySign(code string) string {
	switch code {
	case "RUB":
		return "₽"
	case "KGS":
		return "сом"
	case "KZT":
		return "₸"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	}
	return code
}

// Handler is the public site.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	mux.Handle("GET /kitten/", http.StripPrefix("/kitten", kitten.Handler()))
	mux.HandleFunc("GET /{$}", s.shop)
	mux.HandleFunc("POST /order", s.createOrder)
	mux.HandleFunc("GET /o/{token}", s.orderPage)
	mux.HandleFunc("GET /o/{token}/state.json", s.orderState)
	mux.HandleFunc("POST /o/{token}/code", s.verifyCode)
	mux.HandleFunc("POST /o/{token}/resend", s.resendCode)
	mux.HandleFunc("POST /o/{token}/t/{transition}", s.customerTransition)
	mux.HandleFunc("POST /o/{token}/paid", s.reportPaid)
	mux.HandleFunc("POST /beat", s.beat)
	for path, file := range map[string]string{"GET /cover": s.cfg.Cover, "GET /og": s.cfg.OGImage} {
		if file == "" {
			continue
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			http.ServeFile(w, r, file)
		})
	}
	if s.hasCatalog() {
		s.storeRoutes(mux)
	}
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		s.redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /terms", s.terms)
	if s.cfg.Owner != nil {
		s.ownerRoutes(mux)
	}
	if s.cfg.Dev && s.outbox != nil {
		mux.HandleFunc("GET /dev/outbox", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s.outbox.Messages())
		})
	}
	return s.mount(s.middleware(mux))
}

// mount puts the site under its prefix: /demo-shop/cart reaches the /cart handler, the bare /demo-shop
// is sent to /demo-shop/.
func (s *Server) mount(h http.Handler) http.Handler {
	if s.cfg.Prefix == "" {
		return h
	}
	mux := http.NewServeMux()
	mux.Handle(s.cfg.Prefix+"/", http.StripPrefix(s.cfg.Prefix, h))
	mux.HandleFunc(s.cfg.Prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.cfg.Prefix+"/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

// redirect sends the browser to a path of this site, under its prefix.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, path string, code int) {
	if strings.HasPrefix(path, "/") {
		path = s.cfg.Prefix + path
	}
	http.Redirect(w, r, path, code)
}

// coverURL is the cover's address with a version from the file, so a new picture is not hidden by the cache.
func (s *Server) coverURL() string {
	if s.cfg.Cover == "" {
		return ""
	}
	v := ""
	if fi, err := os.Stat(s.cfg.Cover); err == nil {
		v = strconv.FormatInt(fi.ModTime().Unix()^fi.Size(), 36)
	}
	return s.cfg.Prefix + "/cover?v=" + v
}

// fileVersion is a short tag of a file's state for its address, so a new picture is not hidden by caches.
func fileVersion(file string) string {
	fi, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(fi.ModTime().Unix()^fi.Size(), 36)
}

// sitePreview is the absolute address of the site's link preview picture: the profile's og_image, else
// the cover, else none.
func (s *Server) sitePreview() string {
	switch {
	case s.cfg.OGImage != "":
		return s.svc.BaseURL() + "/og?v=" + fileVersion(s.cfg.OGImage)
	case s.cfg.Cover != "":
		return s.svc.BaseURL() + "/cover?v=" + fileVersion(s.cfg.Cover)
	}
	return ""
}

// cookiePath keeps the cookies of a site under a prefix to that prefix.
func (s *Server) cookiePath() string { return s.cfg.Prefix + "/" }

// AdminHandler is the operator's dashboard and /metrics. Serve it on 127.0.0.1 only and reach it through
// an SSH tunnel: it shows order numbers and load figures that are nobody else's business.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.Handle("GET /metrics", s.rec.Handler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.redirect(w, r, "/admin", http.StatusFound) })
	mux.HandleFunc("GET /admin", s.adminPage)
	mux.HandleFunc("GET /admin/live.json", s.adminLive)
	return s.mount(securityHeaders(mux, false))
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

func securityHeaders(h http.Handler, secure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
			"connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "same-origin") // the order link carries its secret: never send it to other sites
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if secure {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		h.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

// Unwrap lets http.ResponseController reach the connection (the upload stretches its deadlines).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) middleware(mux *http.ServeMux) http.Handler {
	inner := securityHeaders(mux, s.cfg.SecureCookies)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		_, route := mux.Handler(r)
		if route == "" {
			route = "other"
		}
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic", "err", p, "stack", string(debug.Stack()))
				sw.code = http.StatusInternalServerError
				http.Error(sw, "internal error", http.StatusInternalServerError)
			}
			s.rec.Observe(route, sw.code, time.Since(start))
		}()
		if !s.general.Allow(ClientIP(r, s.cfg.Trusted)) {
			sw.Header().Set("Retry-After", "5")
			sw.WriteHeader(http.StatusTooManyRequests)
			_, _ = sw.Write([]byte("слишком много запросов, попробуйте через несколько секунд"))
			return
		}
		limit := int64(64 << 10)
		if r.Method == http.MethodPost && r.URL.Path == uploadPath {
			limit = uploadLimit // photos from a phone, a dozen at once
		}
		r.Body = http.MaxBytesReader(sw, r.Body, limit)
		inner.ServeHTTP(sw, r)
	})
}

func (s *Server) render(w http.ResponseWriter, page string, code int, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := s.tpl[page].Execute(w, data); err != nil {
		s.log.Error("template", "page", page, "err", err)
	}
}

// asset is the address of a static file with a short hash of its content: browsers keep files for an hour,
// and a new version must not wait for that hour.
func asset(name string) string {
	assetOnce.Do(func() {
		assetHash = map[string]string{}
		entries, _ := fs.ReadDir(staticFS, "static")
		for _, e := range entries {
			if b, err := fs.ReadFile(staticFS, "static/"+e.Name()); err == nil {
				sum := sha256.Sum256(b)
				assetHash[e.Name()] = hex.EncodeToString(sum[:4])
			}
		}
	})
	return "/static/" + name + "?v=" + assetHash[name]
}

var (
	assetOnce sync.Once
	assetHash map[string]string
)

// whole keeps hyphenated words of a big heading in one piece: «Хитаги-сан» moves to the next line whole
// instead of breaking at its hyphen.
func whole(text string) template.HTML {
	words := strings.Fields(text)
	for i, w := range words {
		w = template.HTMLEscapeString(w)
		if strings.Contains(w, "-") {
			w = `<span class="whole">` + w + `</span>`
		}
		words[i] = w
	}
	return template.HTML(strings.Join(words, " "))
}
