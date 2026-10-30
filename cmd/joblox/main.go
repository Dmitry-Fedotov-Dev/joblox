// Command joblox runs a shop, a bakery, a taxi dispatch or any other order service described by a
// business profile. Everything is configured with environment variables (see docs/CONFIGURATION.md):
//
//	joblox up                    # everything on this computer or phone, published through Cloudflare (the default)
//	joblox serve                 # the site, the operator bot, the dashboard
//	joblox check profile.yaml    # validate a profile (and JOBLOX_REQUISITES) and print its workflow
//	joblox health                # exit 0 when the local server answers /healthz (Docker HEALTHCHECK)
//	joblox hub                   # Joblox Network: the public counter of installations (JOBLOX_HUB_ADDR, JOBLOX_HUB_DB)
//	joblox version
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/bot"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/hub"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/metrics"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/shop"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/store"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/web"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// started without arguments, by a double click or from the installer: the owner's quick start
	implicit := len(os.Args) == 1 && os.Getenv("JOBLOX_PROFILE") == ""
	cmd := "serve"
	if implicit {
		cmd = "up"
	}
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "up":
		err = up(log)
	case "serve":
		err = serve(log)
	case "check":
		err = check(os.Args[2:])
	case "health":
		err = health()
	case "hub":
		err = runHub(log)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q: up, serve, check, health, hub, version", cmd)
	}
	if err != nil {
		log.Error(err.Error())
		if implicit { // keep the window open, otherwise a double-clicked program vanishes with its error
			fmt.Fprintln(os.Stderr, "Нажмите Enter, чтобы закрыть окно.")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Config is read once at start.
type Config struct {
	Env        string
	Profile    string
	Requisites string
	DB         string
	Addr       string
	AdminAddr  string
	BaseURL    string
	Secret     []byte
	Trusted    string
	SMTP       *mail.SMTP
	BotToken   string
	Operators  []int64
	OwnerKey   string // opens the owner's area on a server too: BaseURL/owner/enter?k=OwnerKey
	// Demo is a showcase site: no mail and no bot needed, a banner on every page, payment requisites that say
	// not to pay, and a shop filled with sample products on the first start.
	Demo bool
}

// loadConfig reads the environment. quick is `joblox up`: no mail, no bot and a base address that is not
// known yet are fine there, because the owner works in the browser and the tunnel brings the address.
func loadConfig(quick bool) (Config, error) {
	c := Config{
		Env: env("JOBLOX_ENV", "prod"), Profile: env("JOBLOX_PROFILE", ""), Requisites: env("JOBLOX_REQUISITES", ""),
		DB: env("JOBLOX_DB", "data/joblox.db"), Addr: env("JOBLOX_ADDR", ":8080"),
		AdminAddr: env("JOBLOX_ADMIN_ADDR", "127.0.0.1:9180"), BaseURL: env("JOBLOX_BASE_URL", ""),
		Trusted: env("JOBLOX_TRUSTED_PROXIES", "127.0.0.1,::1"), BotToken: env("TELEGRAM_TOKEN", ""),
		OwnerKey: os.Getenv("JOBLOX_OWNER_KEY"), Demo: os.Getenv("JOBLOX_DEMO") == "1",
	}
	relaxed := quick || c.Demo // a site without mail and bot is fine
	var errs []error
	if c.OwnerKey != "" && len(c.OwnerKey) < 32 {
		errs = append(errs, errors.New("JOBLOX_OWNER_KEY: at least 32 random characters (openssl rand -hex 24)"))
	}
	if c.Env != "prod" && c.Env != "dev" {
		errs = append(errs, fmt.Errorf("JOBLOX_ENV must be prod or dev, not %q", c.Env))
	}
	if c.Profile == "" {
		errs = append(errs, errors.New("JOBLOX_PROFILE: path to the business profile, e.g. profiles/bakery.yaml"))
	}
	secret := os.Getenv("JOBLOX_SECRET")
	switch {
	case len(secret) >= 32:
		c.Secret = []byte(secret)
	case c.Env == "dev":
		c.Secret = make([]byte, 32)
		_, _ = rand.Read(c.Secret)
	default:
		errs = append(errs, errors.New("JOBLOX_SECRET: at least 32 random characters (openssl rand -hex 32)"))
	}
	if quick && os.Getenv("JOBLOX_ADMIN_ADDR") == "" {
		c.AdminAddr = "" // the owner sees the dashboard in the browser, no second port
	}
	if c.BaseURL == "" && quick {
		c.BaseURL = "http://127.0.0.1" // replaced by the tunnel address as soon as it is up
	} else if c.BaseURL == "" {
		if c.Env == "dev" {
			c.BaseURL = "http://localhost" + c.Addr
		} else {
			errs = append(errs, errors.New("JOBLOX_BASE_URL: the public address, e.g. https://shop.example.com"))
		}
	} else if c.Env == "prod" && !strings.HasPrefix(c.BaseURL, "https://") {
		errs = append(errs, errors.New("JOBLOX_BASE_URL must be https:// in production: order links carry secrets"))
	}
	if host := env("SMTP_HOST", ""); host != "" {
		port, err := strconv.Atoi(env("SMTP_PORT", "587"))
		if err != nil {
			errs = append(errs, errors.New("SMTP_PORT: a number"))
		}
		c.SMTP = &mail.SMTP{Host: host, Port: port, User: env("SMTP_USER", ""), Password: os.Getenv("SMTP_PASSWORD"),
			From: env("SMTP_FROM", ""), Insecure: env("SMTP_INSECURE", "") == "1"}
		if c.SMTP.From == "" {
			errs = append(errs, errors.New("SMTP_FROM: the sender, e.g. Пекарня <shop@example.com>"))
		}
	} else if c.Env == "prod" && !relaxed {
		errs = append(errs, errors.New("SMTP_HOST: production needs a mail relay for codes and statuses"))
	}
	for _, part := range strings.FieldsFunc(env("JOBLOX_OPERATORS", ""), func(r rune) bool { return r == ',' || r == ' ' }) {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("JOBLOX_OPERATORS: %q is not a Telegram id", part))
			continue
		}
		c.Operators = append(c.Operators, id)
	}
	if c.BotToken != "" && len(c.Operators) == 0 {
		errs = append(errs, errors.New("JOBLOX_OPERATORS: the bot needs at least one operator id"))
	}
	if c.Env == "prod" && c.BotToken == "" && !relaxed {
		errs = append(errs, errors.New("TELEGRAM_TOKEN: production needs the operator bot, otherwise nobody sees orders"))
	}
	return c, errors.Join(errs...)
}

func serve(log *slog.Logger) error {
	cfg, err := loadConfig(false)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, log, cfg, runOptions{})
}

// runOptions are what `joblox up` adds to a plain `joblox serve`.
type runOptions struct {
	Listener      net.Listener // the public listener; nil listens on cfg.Addr
	Owner         *web.Owner
	SecureCookies bool                 // the site is reached over https even while BaseURL is still local
	Listing       func() *hub.Listing  // the market card for the heartbeat
	Wake          <-chan struct{}      // send the heartbeat now: the address or the card changed
	Started       func(*order.Service) // called once the service is built
	Restart       <-chan struct{}      // stop and return errRestart: the owner changed the profile
}

var errRestart = errors.New("restart")

func run(ctx context.Context, log *slog.Logger, cfg Config, opt runOptions) error {
	p, err := profile.Load(cfg.Profile)
	if err != nil {
		return err
	}
	req, err := order.LoadRequisites(cfg.Requisites)
	if err != nil {
		return err
	}
	if cfg.Demo && req == nil {
		req = &order.Requisites{Title: "Это демо", Recipient: "Демо-сайт Joblox",
			Lines: []order.RequisiteRow{{Label: "Перевод", Value: "не переводите деньги: это пример сайта"}}}
	}
	if p.AcceptsTransfer() && req == nil {
		log.Warn("the profile takes transfers but JOBLOX_REQUISITES is empty: transfer is hidden from the checkout")
	}
	if err := os.MkdirAll(dirOf(cfg.DB), 0o750); err != nil {
		return err
	}
	st, err := store.Open(cfg.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	var mailer order.Mailer
	var outbox *mail.Outbox
	if cfg.SMTP != nil {
		mailer = cfg.SMTP
	} else {
		outbox = &mail.Outbox{}
		mailer = outbox
		if opt.Owner != nil || cfg.Demo {
			// nobody could receive a code: the owner sees the letters in the browser instead
			p.Payment.RequireEmailCode = false
			log.Info("no SMTP_HOST: letters are kept for the owner's area, email codes are off")
		} else {
			log.Warn("no SMTP_HOST: letters go to /dev/outbox (dev only)")
		}
	}
	svc := order.New(p, st, mailer, req, cfg.Secret, cfg.BaseURL)
	svc.Log = log
	var catalog *shop.Catalog
	var photos *shop.Photos
	if p.Pricing.Mode == profile.PricingShop {
		if catalog, err = shop.Open(ctx, st.DB()); err != nil {
			return err
		}
		svc.Stock = catalog
		photos = &shop.Photos{Dir: filepath.Join(dirOf(cfg.DB), "photos")}
		if cfg.Demo {
			if err := shop.SeedDemo(ctx, catalog); err != nil {
				return err
			}
		}
	}
	if opt.Owner == nil && cfg.OwnerKey != "" {
		opt.Owner = serverOwner(cfg.OwnerKey)
	}
	rec := metrics.New()
	svc.Observe = rec
	trusted, err := web.ParsePrefixes(cfg.Trusted)
	if err != nil {
		return fmt.Errorf("JOBLOX_TRUSTED_PROXIES: %w", err)
	}
	site, err := web.New(svc, rec, outbox, cfg.Secret, web.Config{Trusted: trusted, Dev: cfg.Env == "dev",
		SecureCookies: opt.SecureCookies || strings.HasPrefix(cfg.BaseURL, "https://"), Version: version, Owner: opt.Owner,
		CallURL: callURL(), Shop: catalog, Photos: photos, Demo: cfg.Demo, Prefix: pathOf(cfg.BaseURL), Cover: coverOf(cfg.Profile, p), MarketURL: marketURL()}, log)
	if err != nil {
		return err
	}
	if opt.Started != nil {
		opt.Started(svc)
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	if cfg.BotToken != "" {
		b := bot.New(bot.NewAPI(cfg.BotToken), svc, cfg.Operators, log)
		svc.Notify = b
		go b.Run(ctx)
		b.Started(ctx, version)
	}
	go sweep(ctx, svc, log)
	if url := env("JOBLOX_NETWORK_URL", ""); url != "" && url != "off" {
		id, err := st.Meta(ctx, "network_id")
		if err != nil {
			return err
		}
		if id == "" {
			id = hub.NewID()
			if err := st.SetMeta(ctx, "network_id", id); err != nil {
				return err
			}
		}
		rep := &hub.Reporter{URL: url, ID: id, Kind: p.Business.Kind, Version: version, Online: rec.Online, Log: log,
			Listing: opt.Listing, Wake: opt.Wake,
			OrdersToday: func(ctx context.Context) int {
				s, err := st.Stats(ctx, time.Now().Add(-24*time.Hour), svc.Finals())
				if err != nil {
					return 0
				}
				return s.Today
			}}
		go rep.Run(ctx)
		log.Info("joblox network on: anonymous counters go to " + url)
	}

	public := &http.Server{Addr: cfg.Addr, Handler: site.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	admin := &http.Server{Addr: cfg.AdminAddr, Handler: site.AdminHandler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	errc := make(chan error, 2)
	listen := func(s *http.Server, ln net.Listener) {
		var err error
		if ln != nil {
			err = s.Serve(ln)
		} else {
			err = s.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}
	go listen(public, opt.Listener)
	if cfg.AdminAddr != "" {
		go listen(admin, nil)
	}
	log.Info("joblox started", "version", version, "business", p.Business.Name, "addr", cfg.Addr, "admin", cfg.AdminAddr,
		"env", cfg.Env, "requisites", req.Fingerprint())
	var result error
	select {
	case <-ctx.Done():
	case <-opt.Restart:
		result = errRestart
	case err := <-errc:
		stop()
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = public.Shutdown(shutdown)
	_ = admin.Shutdown(shutdown)
	log.Info("joblox stopped")
	return result
}

func runHub(log *slog.Logger) error {
	path := env("JOBLOX_HUB_DB", "data/hub.db")
	if err := os.MkdirAll(dirOf(path), 0o750); err != nil {
		return err
	}
	h, err := hub.Open(path, log)
	if err != nil {
		return err
	}
	defer h.Close()
	h.AdminToken, h.Contact = os.Getenv("JOBLOX_HUB_ADMIN_TOKEN"), env("JOBLOX_HUB_CONTACT", "")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	addr := env("JOBLOX_HUB_ADDR", "127.0.0.1:8099")
	log.Info("joblox network hub started", "addr", addr, "version", version)
	return h.Run(ctx, addr)
}

// callURL is the voice service for calls inside an order; empty or off: no call button.
func callURL() string {
	u := env("JOBLOX_CALL_URL", "")
	if u == "off" || !strings.HasPrefix(u, "https://") {
		return ""
	}
	return u
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i > 0 {
		return path[:i]
	}
	return "."
}

func sweep(ctx context.Context, svc *order.Service, log *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := svc.Sweep(ctx); err != nil {
				log.Warn("sweep", "err", err)
			} else if n > 0 {
				log.Info("expired unpaid orders", "count", n)
			}
		}
	}
}

func health() error {
	addr := env("JOBLOX_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz") // #nosec G107 -- our own listen address from the configuration
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

func check(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: joblox check profile.yaml [requisites.yaml]")
	}
	p, err := profile.Load(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s (%s): %d позиций, цена: %s, оплата: %s\n", p.Business.Name, p.Business.Kind, len(p.Catalog),
		p.Pricing.Mode, strings.Join(p.Payment.Methods, ", "))
	for _, s := range p.Workflow.States {
		mark := ""
		if s.ID == p.Workflow.Initial {
			mark = " (начало)"
		}
		if s.Final {
			mark = " (конец)"
		}
		fmt.Printf("  %s — %s%s\n", s.ID, s.Title, mark)
		for _, t := range p.Workflow.Transitions {
			if t.From.Has(s.ID) {
				req := ""
				if len(t.Requires) > 0 {
					req = ", нужно: " + strings.Join(t.Requires, ", ")
				}
				fmt.Printf("      → %s «%s», кто: %s%s\n", t.To, t.Title, strings.Join(t.By, ", "), req)
			}
		}
	}
	if len(args) > 1 {
		req, err := order.LoadRequisites(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("реквизиты: %s, отпечаток %s\n", req.Recipient, req.Fingerprint())
	}
	return nil
}

// serverOwner is the owner's area of a shop run by `joblox serve` on a server: no setup and no tunnel, only the
// orders, the catalog and the dashboard behind the master link.
func serverOwner(key string) *web.Owner {
	return &web.Owner{Key: key, Configured: func() bool { return true }, Shown: func() bool { return false },
		SetShown: func(bool) error { return errors.New("the market card is set in the profile on a server") }}
}

// pathOf is the path part of the base address: a site at https://example.com/demo-shop lives under /demo-shop.
func pathOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

// coverOf is the file of the profile's cover picture, which lies next to the profile.
func coverOf(profilePath string, p *profile.Profile) string {
	if p.Business.Cover == "" {
		return ""
	}
	file := filepath.Join(filepath.Dir(profilePath), p.Business.Cover)
	if _, err := os.Stat(file); err != nil {
		return "" // a profile copied without its picture (joblox up): no cover rather than a broken one
	}
	return file
}

// marketURL is where the «Сделать себе такой же» button leads: the network's market, the default one when
// the installation does not report to a network of its own. JOBLOX_MARKET_URL=off hides the button.
func marketURL() string {
	switch v := env("JOBLOX_MARKET_URL", ""); v {
	case "off":
		return ""
	case "":
		if n := env("JOBLOX_NETWORK_URL", ""); strings.HasPrefix(n, "https://") {
			return n
		}
		return "https://joblox.openline.one"
	default:
		return v
	}
}
