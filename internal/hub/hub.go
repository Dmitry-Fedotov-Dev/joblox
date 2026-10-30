// Package hub is Joblox Network: the market of performers and a public counter of Joblox installations.
// An installation sends a heartbeat with a random id and a few numbers; the hub publishes only the totals.
// When the owner also pressed "Показать на бирже", the heartbeat carries their card (name, place, a short
// text, a contact and the shop's address), and the market shows it while the shop is online.
//
// What the hub never receives or keeps: goods, prices, customers, orders, payments, IP addresses.
package hub

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/kitten"
)

//go:embed *.html
var pageFS embed.FS

const (
	// ActiveWindow: an installation counts while its last heartbeat is this fresh.
	ActiveWindow = 15 * time.Minute
	// BeatEvery is how often installations report; a faster id is refused.
	BeatEvery = time.Minute
	keepFor   = 7 * 24 * time.Hour

	maxOnline = 100_000    // one installation cannot add more than this to the online total
	maxOrders = 10_000_000 // nor to the orders total
)

// Beat is what an installation sends.
type Beat struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Version     string   `json:"version"`
	Online      int      `json:"online"`
	OrdersToday int      `json:"orders_today"`
	Listing     *Listing `json:"listing,omitempty"` // only when the owner shows the shop on the market
}

// Stats is the public total.
type Stats struct {
	Installations int            `json:"installations"`
	Online        int            `json:"online"`
	OrdersToday   int            `json:"orders_today"`
	Kinds         map[string]int `json:"kinds"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

var (
	idRe      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	kindRe    = regexp.MustCompile(`^[a-z0-9_-]{1,24}$`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,32}$`)
)

// Validate checks a heartbeat; unknown kinds become "other" so the public page shows no free text.
func (b *Beat) Validate() error {
	if !idRe.MatchString(b.ID) {
		return errors.New("id: 32 lowercase hex characters")
	}
	if b.Online < 0 || b.OrdersToday < 0 {
		return errors.New("negative numbers")
	}
	if !kindRe.MatchString(b.Kind) {
		b.Kind = "other"
	}
	if !versionRe.MatchString(b.Version) {
		b.Version = "unknown"
	}
	b.Online = min(b.Online, maxOnline)
	b.OrdersToday = min(b.OrdersToday, maxOrders)
	if b.Listing != nil && b.Listing.Clean() != nil {
		b.Listing = nil // a broken card is not shown, the counters still count
	}
	return nil
}

type Hub struct {
	// AdminToken lets the network owner hide a card (POST /api/v1/hide); empty turns moderation off.
	AdminToken string
	// Contact is where people report a card, shown on the market.
	Contact string
	// Sales is the Telegram chat for hosting and turnkey requests (JOBLOX_HUB_SALES); empty: DefaultSales.
	Sales string

	db   *sql.DB
	log  *slog.Logger
	now  func() time.Time
	page *template.Template

	mu     sync.Mutex
	seen   map[string]time.Time // id → last accepted beat, for the per-id rate
	newIDs map[string][]time.Time
}

// Open opens the hub database (":memory:" for tests).
func Open(path string, log *slog.Logger) (*Hub, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		dsn = "file::memory:"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS installs (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, version TEXT NOT NULL,
		online INTEGER NOT NULL, orders_today INTEGER NOT NULL, seen_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS hidden (id TEXT PRIMARY KEY)`,
	} {
		if _, err := db.Exec(q); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	// the card column came with the market; databases of the counter-only hub get it here
	if _, err := db.Exec(`ALTER TABLE installs ADD COLUMN listing TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		_ = db.Close()
		return nil, err
	}
	page, err := template.New("").Funcs(template.FuncMap{"kindTitle": KindTitle, "kindIcon": KindIcon}).ParseFS(pageFS, "*.html")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Hub{db: db, log: log, now: time.Now, page: page, seen: map[string]time.Time{}, newIDs: map[string][]time.Time{}}, nil
}

func (h *Hub) Close() error { return h.db.Close() }

// allow applies the rates: one beat a minute per id, and at most 20 new ids an hour from one address.
// The address lives only in this map, in memory, and is forgotten within the hour.
func (h *Hub) allow(id, addr string) bool {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if last, ok := h.seen[id]; ok {
		if now.Sub(last) < BeatEvery-5*time.Second {
			return false
		}
		h.seen[id] = now
		return true
	}
	recent := h.newIDs[addr][:0]
	for _, t := range h.newIDs[addr] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 20 {
		h.newIDs[addr] = recent
		return false
	}
	h.newIDs[addr] = append(recent, now)
	h.seen[id] = now
	if len(h.seen) > 200_000 {
		for k, t := range h.seen {
			if now.Sub(t) > ActiveWindow {
				delete(h.seen, k)
			}
		}
	}
	return true
}

// Record stores a heartbeat.
func (h *Hub) Record(ctx context.Context, b Beat) error {
	listing := ""
	if b.Listing != nil {
		raw, err := json.Marshal(b.Listing)
		if err != nil {
			return err
		}
		listing = string(raw)
	}
	_, err := h.db.ExecContext(ctx, `INSERT INTO installs (id, kind, version, online, orders_today, seen_at, listing)
		VALUES (?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, version=excluded.version,
		online=excluded.online, orders_today=excluded.orders_today, seen_at=excluded.seen_at, listing=excluded.listing`,
		b.ID, b.Kind, b.Version, b.Online, b.OrdersToday, h.now().Unix(), listing)
	return err
}

// Stats sums the installations heard within the active window.
func (h *Hub) Stats(ctx context.Context) (Stats, error) {
	st := Stats{Kinds: map[string]int{}, UpdatedAt: h.now().UTC()}
	rows, err := h.db.QueryContext(ctx, `SELECT kind, COUNT(*), SUM(online), SUM(orders_today) FROM installs
		WHERE seen_at >= ? GROUP BY kind`, h.now().Add(-ActiveWindow).Unix())
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n, online, orders int
		if err := rows.Scan(&kind, &n, &online, &orders); err != nil {
			return st, err
		}
		st.Kinds[kind] = n
		st.Installations += n
		st.Online += online
		st.OrdersToday += orders
	}
	return st, rows.Err()
}

// Prune forgets installations silent for a week.
func (h *Hub) Prune(ctx context.Context) error {
	_, err := h.db.ExecContext(ctx, `DELETE FROM installs WHERE seen_at < ?`, h.now().Add(-keepFor).Unix())
	return err
}

// Handler serves the API and the public page.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/beat", h.beat)
	mux.HandleFunc("GET /api/v1/stats", h.stats)
	mux.HandleFunc("OPTIONS /api/v1/stats", h.stats)
	mux.HandleFunc("GET /api/v1/market", h.market)
	mux.HandleFunc("POST /api/v1/hide", h.hide)
	mux.HandleFunc("GET /{$}", h.home)
	mux.HandleFunc("GET /start", h.start)
	mux.HandleFunc("GET /terms", h.static("terms.html"))
	mux.HandleFunc("GET /api", h.static("api.html"))
	mux.HandleFunc("GET /about", h.static("about.html"))
	mux.HandleFunc("GET /t/{id}", h.showcase)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("GET /kitten/", http.StripPrefix("/kitten", kitten.Handler()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the only scripts are the kitten's, from this host; the page refreshes itself with a meta tag
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'self'; img-src 'self' data:; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		mux.ServeHTTP(w, r)
	})
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	// behind Caddy on the same host every request comes from loopback: take the proxy's header then
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			return fwd
		}
	}
	return host
}

func (h *Hub) beat(w http.ResponseWriter, r *http.Request) {
	var b Beat
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // the hub accepts exactly these numbers and nothing else
	if err := dec.Decode(&b); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := b.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.allow(b.ID, clientAddr(r)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too often", http.StatusTooManyRequests)
		return
	}
	if err := h.Record(r.Context(), b); err != nil {
		h.log.Error("record beat", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) stats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*") // an open API: any site may show the counters
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	st, err := h.Stats(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=30")
	_ = json.NewEncoder(w).Encode(st)
}

// home is the market as a customer sees it, like the home screen of a super app: a big «Что нужно?» search
// and the tiles of everything available right now (performers on line first, then the network's own
// services and templates). ?q= searches the tiles, ?kind= keeps one kind.
func (h *Hub) home(w http.ResponseWriter, r *http.Request) {
	st, err := h.Stats(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	all, err := h.Market(r.Context(), "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	kind := r.URL.Query().Get("kind")
	q := clip(r.URL.Query().Get("q"), 60)
	var tiles []Card
	for _, c := range all {
		if (kind == "" || c.Kind == kind) && matches(c, q) {
			tiles = append(tiles, c)
		}
	}
	h.render(w, "market.html", struct {
		Stats
		Tiles   []Card
		Query   string
		Contact string
	}{st, tiles, q, h.Contact})
}

// matches is the search of the home page: every word of the query is somewhere in the card.
func matches(c Card, q string) bool {
	text := strings.ToLower(c.Title + " " + c.About + " " + c.Place + " " + KindTitle(c.Kind))
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

func (h *Hub) static(page string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		h.render(w, page, struct {
			Roles   []Card
			Contact string
		}{Roles, h.Contact})
	}
}

func (h *Hub) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.page.ExecuteTemplate(w, page, data); err != nil {
		h.log.Error("page", "page", page, "err", err)
	}
}

// Run serves until ctx ends and prunes old rows once an hour.
func (h *Hub) Run(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := h.Prune(ctx); err != nil {
					h.log.Warn("prune", "err", err)
				}
			}
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		return err
	}
}
