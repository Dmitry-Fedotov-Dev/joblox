// Command joblox runs a shop, a bakery, a taxi dispatch or any other order service described by a
// business profile. Everything is configured with environment variables (see docs/CONFIGURATION.md):
//
//	joblox serve                 # the site, the operator bot, the dashboard
//	joblox check profile.yaml    # validate a profile (and JOBLOX_REQUISITES) and print its workflow
//	joblox health                # exit 0 when the local server answers /healthz (Docker HEALTHCHECK)
//	joblox version
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/bot"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/metrics"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/store"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/web"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(log)
	case "check":
		err = check(os.Args[2:])
	case "health":
		err = health()
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q: serve, check, health, version", cmd)
	}
	if err != nil {
		log.Error(err.Error())
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
}

func loadConfig() (Config, error) {
	c := Config{
		Env: env("JOBLOX_ENV", "prod"), Profile: env("JOBLOX_PROFILE", ""), Requisites: env("JOBLOX_REQUISITES", ""),
		DB: env("JOBLOX_DB", "data/joblox.db"), Addr: env("JOBLOX_ADDR", ":8080"),
		AdminAddr: env("JOBLOX_ADMIN_ADDR", "127.0.0.1:9180"), BaseURL: env("JOBLOX_BASE_URL", ""),
		Trusted: env("JOBLOX_TRUSTED_PROXIES", "127.0.0.1,::1"), BotToken: env("TELEGRAM_TOKEN", ""),
	}
	var errs []error
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
	if c.BaseURL == "" {
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
	} else if c.Env == "prod" {
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
	if c.Env == "prod" && c.BotToken == "" {
		errs = append(errs, errors.New("TELEGRAM_TOKEN: production needs the operator bot, otherwise nobody sees orders"))
	}
	return c, errors.Join(errs...)
}

func serve(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	p, err := profile.Load(cfg.Profile)
	if err != nil {
		return err
	}
	req, err := order.LoadRequisites(cfg.Requisites)
	if err != nil {
		return err
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
		log.Warn("no SMTP_HOST: letters go to /dev/outbox (dev only)")
	}
	svc := order.New(p, st, mailer, req, cfg.Secret, cfg.BaseURL)
	svc.Log = log
	rec := metrics.New()
	svc.Observe = rec
	trusted, err := web.ParsePrefixes(cfg.Trusted)
	if err != nil {
		return fmt.Errorf("JOBLOX_TRUSTED_PROXIES: %w", err)
	}
	site, err := web.New(svc, rec, outbox, cfg.Secret, web.Config{Trusted: trusted, Dev: cfg.Env == "dev",
		SecureCookies: strings.HasPrefix(cfg.BaseURL, "https://"), Version: version}, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.BotToken != "" {
		b := bot.New(bot.NewAPI(cfg.BotToken), svc, cfg.Operators, log)
		svc.Notify = b
		go b.Run(ctx)
		b.Started(ctx, version)
	}
	go sweep(ctx, svc, log)

	public := &http.Server{Addr: cfg.Addr, Handler: site.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	admin := &http.Server{Addr: cfg.AdminAddr, Handler: site.AdminHandler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	errc := make(chan error, 2)
	for _, s := range []*http.Server{public, admin} {
		go func(s *http.Server) {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}(s)
	}
	log.Info("joblox started", "version", version, "business", p.Business.Name, "addr", cfg.Addr, "admin", cfg.AdminAddr,
		"env", cfg.Env, "requisites", req.Fingerprint())
	select {
	case <-ctx.Done():
	case err := <-errc:
		stop()
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = public.Shutdown(shutdown)
	_ = admin.Shutdown(shutdown)
	log.Info("joblox stopped")
	return nil
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
