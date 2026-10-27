// Package web serves the storefront, the checkout, the customer's order page and, on a separate
// local-only listener, the operator's dashboard. Pages are rendered on the server; the little JavaScript
// there is only refreshes the order status and keeps the online counter, and every form works without it.
package web

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/metrics"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
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
		"cur":   currencySign,
		"join":  strings.Join,
		"inc":   func(i int) int { return i + 1 },
	}
	s.tpl = map[string]*template.Template{}
	for _, page := range []string{"shop", "order", "message", "admin", "owner", "terms"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
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
	mux.HandleFunc("GET /{$}", s.shop)
	mux.HandleFunc("POST /order", s.createOrder)
	mux.HandleFunc("GET /o/{token}", s.orderPage)
	mux.HandleFunc("GET /o/{token}/state.json", s.orderState)
	mux.HandleFunc("POST /o/{token}/code", s.verifyCode)
	mux.HandleFunc("POST /o/{token}/resend", s.resendCode)
	mux.HandleFunc("POST /o/{token}/t/{transition}", s.customerTransition)
	mux.HandleFunc("POST /beat", s.beat)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
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
	return s.middleware(mux)
}

// AdminHandler is the operator's dashboard and /metrics. Serve it on 127.0.0.1 only and reach it through
// an SSH tunnel: it shows order numbers and load figures that are nobody else's business.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.Handle("GET /metrics", s.rec.Handler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin", http.StatusFound) })
	mux.HandleFunc("GET /admin", s.adminPage)
	mux.HandleFunc("GET /admin/live.json", s.adminLive)
	return securityHeaders(mux, false)
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
		r.Body = http.MaxBytesReader(sw, r.Body, 64<<10)
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
