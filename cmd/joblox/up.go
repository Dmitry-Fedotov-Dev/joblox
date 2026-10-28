package main

// `joblox up`: the owner's quick start on their own computer or Android phone. Everything lives in one
// folder (JOBLOX_HOME, joblox-data next to the program by default): the profile, the keys, the database, the settings and
// cloudflared. The shop goes online through a Cloudflare quick tunnel, and the owner signs in with the
// master link the program prints and opens in the browser.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/hub"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/tunnel"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/web"
	"github.com/Dmitry-Fedotov-Dev/joblox/profiles"
)

// DefaultNetwork is the market a new installation reports to; JOBLOX_NETWORK_URL=off turns it off.
const DefaultNetwork = "https://joblox.openline.one"

// DefaultCall is Контакт: an open order gets a voice call by link; JOBLOX_CALL_URL=off turns it off.
const DefaultCall = "https://openline.one/"

// settings are the owner's answers in the setup.
type settings struct {
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	Contact       string    `json:"contact"`
	Place         string    `json:"place"`
	About         string    `json:"about"`
	Country       string    `json:"country"`
	Show          bool      `json:"show"`
	TermsAccepted time.Time `json:"terms_accepted"`
}

type launcher struct {
	local   string // http://127.0.0.1:port, the cabinet on this computer
	dir     string
	key     string
	log     *slog.Logger
	mu      sync.Mutex
	set     *settings // nil until the owner has gone through the setup
	base    atomic.Pointer[string]
	svc     atomic.Pointer[order.Service]
	restart chan struct{}
	wake    chan struct{}
	opened  atomic.Bool
}

func up(log *slog.Logger) error {
	useFallbackDNS()
	useAndroidCerts()
	dir, err := dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// the window is for the owner: short Russian lines; the technical log goes to joblox.log
	if f, err := os.OpenFile(filepath.Join(dir, "joblox.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		defer f.Close()
		log = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	if err := loadEnvFile(filepath.Join(dir, "joblox.env")); err != nil {
		return err
	}
	secret, err := keyFile(filepath.Join(dir, "secret.key"))
	if err != nil {
		return err
	}
	l := &launcher{dir: dir, log: log, restart: make(chan struct{}, 1), wake: make(chan struct{}, 1)}
	if l.key, err = keyFile(filepath.Join(dir, "owner.key")); err != nil {
		return err
	}
	if err := l.loadSettings(); err != nil {
		return err
	}
	setDefault := func(k, v string) {
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	setDefault("JOBLOX_SECRET", secret)
	setDefault("JOBLOX_PROFILE", l.path("profile.yaml"))
	setDefault("JOBLOX_DB", l.path("joblox.db"))
	setDefault("JOBLOX_NETWORK_URL", DefaultNetwork)
	setDefault("JOBLOX_CALL_URL", DefaultCall)
	if _, err := os.Stat(l.path("requisites.yaml")); err == nil {
		setDefault("JOBLOX_REQUISITES", l.path("requisites.yaml"))
	}
	if _, err := os.Stat(os.Getenv("JOBLOX_PROFILE")); errors.Is(err, os.ErrNotExist) {
		raw, err := profiles.FS.ReadFile(profiles.Default)
		if err != nil {
			return err
		}
		if err := os.WriteFile(os.Getenv("JOBLOX_PROFILE"), raw, 0o600); err != nil {
			return err
		}
	}

	token := os.Getenv("CLOUDFLARE_TUNNEL_TOKEN")
	named := os.Getenv("JOBLOX_BASE_URL")
	if token != "" && !strings.HasPrefix(named, "https://") {
		return errors.New("CLOUDFLARE_TUNNEL_TOKEN needs JOBLOX_BASE_URL=https://your.domain in joblox.env")
	}
	// a named tunnel points at a fixed port; a quick tunnel takes any free one
	ln, err := net.Listen("tcp", env("JOBLOX_ADDR", "127.0.0.1:8080"))
	if err != nil && token == "" {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	local := "http://" + ln.Addr().String()
	start := local
	if token != "" {
		start = named
	}
	l.base.Store(&start)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	l.local = local
	fmt.Printf("\nJoblox работает. Кабинет на этом компьютере:\n  %s/owner/enter?k=%s\nПапка с данными: %s\n\n", local, l.key, dir)
	// the site and the browser come up at once; the public address follows when the tunnel is ready
	go func() {
		bin, err := tunnel.Ensure(ctx, l.path("bin"), log, downloadProgress())
		if err != nil {
			log.Error("cloudflared", "err", err)
			fmt.Printf("\nНе получилось скачать cloudflared: %v\n"+
				"Сайт пока работает только на этом компьютере. Перезапустите программу, когда интернет будет стабильнее.\n\n", err)
			return
		}
		fmt.Println("Получаем публичную ссылку от Cloudflare…")
		(&tunnel.Tunnel{Bin: bin, Local: local, Token: token, Log: log, Ready: l.ready}).Run(ctx)
	}()

	market := strings.TrimRight(env("JOBLOX_NETWORK_URL", ""), "/")
	if market == "off" {
		market = ""
	}
	if market != "" {
		go checkClock(ctx, market)
	}
	owner := &web.Owner{Key: l.key, MarketURL: market, DataDir: dir, Configured: l.configured, Setup: l.setup,
		Shown: l.shown, SetShown: l.setShown}
	if market != "" {
		owner.Portal = (&portal{url: market}).cards
	}
	for {
		cfg, err := loadConfig(true)
		if err != nil {
			return err
		}
		cfg.BaseURL = *l.base.Load()
		err = run(ctx, log, cfg, runOptions{Listener: ln, Owner: owner, SecureCookies: true, Listing: l.listing,
			Wake: l.wake, Restart: l.restart, Started: func(svc *order.Service) {
				svc.SetBaseURL(*l.base.Load())
				l.svc.Store(svc)
				if l.opened.CompareAndSwap(false, true) { // the first start: the portal opens right away
					go openBrowser(local + "/owner/enter?k=" + l.key)
				}
			}})
		if !errors.Is(err, errRestart) {
			return err
		}
		// the server closed the listener: take the same port again, the tunnel keeps pointing at it
		if ln, err = net.Listen("tcp", ln.Addr().String()); err != nil {
			return err
		}
	}
}

func (l *launcher) path(name string) string { return filepath.Join(l.dir, name) }

// dataDir is JOBLOX_HOME or joblox-data next to the program, so `joblox up` finds the same shop from any
// folder (a double click, a shortcut, the joblox command on a phone).
func dataDir() (string, error) {
	if d := env("JOBLOX_HOME", ""); d != "" {
		return filepath.Abs(d)
	}
	exe, err := os.Executable()
	if err != nil {
		return filepath.Abs("joblox-data")
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Join(filepath.Dir(exe), "joblox-data"), nil
}

// ready is called by the tunnel each time it is up; url is empty for a named tunnel.
func (l *launcher) ready(url string) {
	if url != "" {
		l.base.Store(&url)
		if svc := l.svc.Load(); svc != nil {
			svc.SetBaseURL(url)
		}
	}
	base := *l.base.Load()
	master := base + "/owner/enter?k=" + l.key
	fmt.Printf("\nСайт в интернете: %s\n\nКабинет с телефона (мастер-ссылка, никому не пересылайте):\n  %s\n\n"+
		"Окно не закрывайте: пока программа работает, сайт открыт. Остановить: Ctrl+C.\n\n", base, master)
	select {
	case l.wake <- struct{}{}: // the market learns the new address
	default:
	}
}

// downloadProgress prints the cloudflared download in the window every 5 MB: on a slow line it takes
// minutes, and a silent window looks like a hang.
func downloadProgress() func(done, total int64) {
	const step = 5 << 20
	var next int64
	return func(done, total int64) {
		if done < next && done != total {
			return
		}
		next = done - done%step + step
		if total > 0 {
			fmt.Printf("Скачиваем cloudflared, один раз: %d из %d МБ\n", done>>20, total>>20)
		} else {
			fmt.Printf("Скачиваем cloudflared, один раз: %d МБ\n", done>>20)
		}
	}
}

// ---------------------------------------------------------------- portal

// portal fetches the market's cards for the owner's area: the program opens on a portal where somebody
// is already working. A minute of cache; an unreachable market just hides the block.
type portal struct {
	url  string
	mu   sync.Mutex
	at   time.Time
	last []hub.Card
}

func (p *portal) cards(ctx context.Context) []hub.Card {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.at) < time.Minute {
		return p.last
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url+"/api/v1/market", nil)
	if err != nil {
		return p.last
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return p.last
	}
	defer resp.Body.Close()
	var out struct {
		Cards []hub.Card `json:"cards"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil {
		return p.last
	}
	var safe []hub.Card
	for _, c := range out.Cards { // links from the network open in the owner's browser: https only
		if strings.HasPrefix(c.URL, "https://") {
			safe = append(safe, c)
		}
	}
	p.last, p.at = safe, time.Now()
	return p.last
}

// ---------------------------------------------------------------- settings

func (l *launcher) loadSettings() error {
	raw, err := os.ReadFile(l.path("settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var s settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("settings.json: %w", err)
	}
	l.set = &s
	return nil
}

func (l *launcher) saveSettings(s settings) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(l.path("settings.json"), raw); err != nil {
		return err
	}
	l.mu.Lock()
	l.set = &s
	l.mu.Unlock()
	return nil
}

func (l *launcher) current() *settings {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.set
}

func (l *launcher) configured() bool { return l.current() != nil }

func (l *launcher) shown() bool {
	s := l.current()
	return s != nil && s.Show
}

func (l *launcher) setShown(show bool) error {
	s := l.current()
	if s == nil {
		return errors.New("the setup is not done")
	}
	c := *s
	c.Show = show
	if err := l.saveSettings(c); err != nil {
		return err
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return nil
}

// listing is the market card sent with the heartbeat: only when the owner shows it and the site has a
// public https address.
func (l *launcher) listing() *hub.Listing {
	s := l.current()
	base := *l.base.Load()
	if s == nil || !s.Show || !strings.HasPrefix(base, "https://") {
		return nil
	}
	return &hub.Listing{Title: s.Name, Place: s.Place, About: s.About, Contact: s.Contact, URL: base}
}

// setup turns the owner's answers into profile.yaml and requisites.yaml and restarts the shop.
func (l *launcher) setup(in web.SetupInput) error {
	country := web.Countries[0]
	for _, c := range web.Countries {
		if c.ID == in.Country {
			country = c
		}
	}
	raw, err := profiles.FS.ReadFile(in.Kind + ".yaml")
	if err != nil {
		return web.SetupError("неизвестная роль")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	root := doc.Content[0]
	biz := mapValue(root, "business")
	setScalar(biz, "name", in.Name)
	setScalar(biz, "contact", in.Contact)
	if in.About != "" {
		setScalar(biz, "description", in.About)
	}
	setScalar(biz, "currency", country.Currency)
	setScalar(biz, "timezone", country.Timezone)
	if methods := mapValue(mapValue(root, "payment"), "methods"); methods != nil && in.Pay == "" {
		hasCash := false
		for _, m := range methods.Content {
			hasCash = hasCash || m.Value == profile.MethodCash
		}
		if !hasCash { // no requisites: without cash such a shop could take no order at all
			methods.Content = append(methods.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: profile.MethodCash})
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	tmp := l.path("profile.yaml.new")
	if err := writeFile(tmp, out); err != nil {
		return err
	}
	if _, err := profile.Load(tmp); err != nil {
		_ = os.Remove(tmp)
		return web.SetupError("профиль не собрался: " + err.Error())
	}
	if err := os.Rename(tmp, os.Getenv("JOBLOX_PROFILE")); err != nil {
		return err
	}

	reqPath := l.path("requisites.yaml")
	if in.Pay != "" {
		req, err := yaml.Marshal(order.Requisites{Title: "Куда перевести", Recipient: in.Name,
			Lines:   []order.RequisiteRow{{Label: "Перевод", Value: in.Pay}},
			Purpose: "Заказ {ref}", Note: "Переводите только по этим реквизитам и укажите номер заказа."})
		if err != nil {
			return err
		}
		if err := writeFile(reqPath, req); err != nil {
			return err
		}
		_ = os.Setenv("JOBLOX_REQUISITES", reqPath)
	} else {
		_ = os.Remove(reqPath)
		_ = os.Unsetenv("JOBLOX_REQUISITES")
	}
	if err := l.saveSettings(settings{Kind: in.Kind, Name: in.Name, Contact: in.Contact, Place: in.Place, About: in.About,
		Country: country.ID, Show: in.Show, TermsAccepted: time.Now().UTC()}); err != nil {
		return err
	}
	go func() { // after the "restarting" page has gone out
		time.Sleep(300 * time.Millisecond)
		select {
		case l.restart <- struct{}{}:
		default:
		}
	}()
	return nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setScalar(m *yaml.Node, key, value string) {
	if m == nil {
		return
	}
	if v := mapValue(m, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, "!!str", value, yaml.DoubleQuotedStyle
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle})
}

// ---------------------------------------------------------------- files

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// keyFile returns the hex key kept in path, making one on the first start.
func keyFile(path string) (string, error) {
	if raw, err := os.ReadFile(path); err == nil { // #nosec G304 -- inside the owner's data folder
		if k := strings.TrimSpace(string(raw)); len(k) >= 32 {
			return k, nil
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	k := hex.EncodeToString(b)
	return k, writeFile(path, []byte(k+"\n"))
}

const envTemplate = `# Настройки Joblox. Строка вида КЛЮЧ=значение, после правки перезапустите программу.
# Решётка в начале строки выключает её. Всё описано в кабинете, раздел «Инструкции».

# Свой домен через Cloudflare Tunnel (вместо временной ссылки trycloudflare.com)
#CLOUDFLARE_TUNNEL_TOKEN=
#JOBLOX_BASE_URL=https://taxi.example.com

# Почта: письма клиентам и коды подтверждения
#SMTP_HOST=smtp.gmail.com
#SMTP_PORT=587
#SMTP_USER=
#SMTP_PASSWORD=
#SMTP_FROM=Название <you@gmail.com>

# Заказы в Telegram
#TELEGRAM_TOKEN=
#JOBLOX_OPERATORS=

# Счётчик и биржа Joblox Network; off выключает
#JOBLOX_NETWORK_URL=off
`

// loadEnvFile applies KEY=VALUE lines from the owner's joblox.env (creating a commented one on the first
// start); the real environment wins.
func loadEnvFile(path string) error {
	f, err := os.Open(path) // #nosec G304 -- inside the owner's data folder
	if errors.Is(err, os.ErrNotExist) {
		return writeFile(path, []byte(envTemplate))
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

// ---------------------------------------------------------------- the device

// checkClock compares the device clock with the market's: a clock hours off makes every https
// certificate look expired or not yet valid, and the card never reaches the market (seen on Windows with
// a stopped time service). The check reads the Date header without trusting the certificate's dates.
func checkClock(ctx context.Context, market string) {
	tr := &http.Transport{TLSClientConfig: &tls.Config{
		// only the time comparison: the request carries nothing and its answer is not used otherwise
		InsecureSkipVerify: true, // #nosec G402 -- a wrong clock is exactly what breaks verification here
	}}
	c := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, market+"/healthz", nil)
	if err != nil {
		return
	}
	resp, err := c.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
	at, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return
	}
	if skew := time.Since(at); skew > 5*time.Minute || skew < -5*time.Minute {
		fmt.Printf("\nВНИМАНИЕ: часы на этом устройстве расходятся с точным временем на %s.\n"+
			"Из-за этого не работают защищённые соединения: карточка не попадёт на биржу, письма не уйдут.\n"+
			"Включите синхронизацию времени (Windows: Параметры, Время и язык, Синхронизировать) и перезапустите программу.\n\n",
			skew.Abs().Round(time.Minute))
	}
}

func openBrowser(url string) {
	if os.Getenv("JOBLOX_NO_BROWSER") != "" { // a server, or a test run
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if p, err := exec.LookPath("termux-open-url"); err == nil { // Android, Termux
			cmd = exec.Command(p, url)
		} else {
			cmd = exec.Command("xdg-open", url)
		}
	}
	_ = cmd.Start() // no browser (a server, a bare phone shell): the link is printed anyway
}

// useFallbackDNS: a Linux binary on Android (Termux) finds no /etc/resolv.conf and could not resolve
// anything; ask public resolvers then.
func useFallbackDNS() {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return
	}
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, "1.1.1.1:53")
	}}
}

// useAndroidCerts: the same Linux binary on Android finds no /etc/ssl either, and every https request
// fails with "certificate signed by unknown authority". Point Go at Termux's bundle or Android's store.
// It must run before the first https request: Go reads the roots once.
func useAndroidCerts() {
	if runtime.GOOS != "linux" || os.Getenv("SSL_CERT_FILE") != "" || os.Getenv("SSL_CERT_DIR") != "" {
		return
	}
	for _, dir := range []string{"/etc/ssl/certs", "/etc/pki/tls/certs"} {
		if _, err := os.Stat(dir); err == nil {
			return
		}
	}
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		if f := filepath.Join(prefix, "etc", "tls", "cert.pem"); fileExists(f) {
			_ = os.Setenv("SSL_CERT_FILE", f)
			return
		}
	}
	if fileExists("/system/etc/security/cacerts") {
		_ = os.Setenv("SSL_CERT_DIR", "/system/etc/security/cacerts")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
