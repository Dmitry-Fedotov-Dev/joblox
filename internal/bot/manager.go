package bot

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/antifraud"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
)

// Settings keeps the bot's token, its name, its operators and the joining code in the site's own database,
// so the owner connects a bot from the cabinet, without the server's environment.
type Settings interface {
	Meta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// Manager runs the operator bot the owner connected in the cabinet: it starts it with the token, lets a new
// operator join with a one-time link and passes the order news on to whichever bot is running now.
type Manager struct {
	svc     *order.Service
	st      Settings
	log     *slog.Logger
	version string
	newAPI  func(token string) *API // tests replace it

	mu     sync.Mutex
	bot    *Bot
	stop   context.CancelFunc
	name   string // the bot's @username
	parent context.Context
}

const (
	keyToken = "bot_token"
	keyName  = "bot_name"
	keyOps   = "bot_operators"
	keyLink  = "bot_link"
)

func NewManager(svc *order.Service, st Settings, version string, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{svc: svc, st: st, log: log, version: version, newAPI: NewAPI}
}

// Start runs the bot saved earlier, if there is one, until ctx ends.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.parent = ctx
	m.mu.Unlock()
	token, err := m.st.Meta(ctx, keyToken)
	if err != nil || token == "" {
		return err
	}
	name, _ := m.st.Meta(ctx, keyName)
	return m.run(ctx, token, name)
}

func (m *Manager) run(ctx context.Context, token, name string) error {
	ops, err := m.operators(ctx)
	if err != nil {
		return err
	}
	b := New(m.newAPI(token), m.svc, ops, m.log)
	b.Join = m.join
	m.mu.Lock()
	if m.stop != nil {
		m.stop()
	}
	parent := m.parent
	if parent == nil {
		parent = context.Background()
	}
	bctx, stop := context.WithCancel(parent)
	m.bot, m.stop, m.name = b, stop, name
	m.mu.Unlock()
	go b.Run(bctx)
	if len(ops) > 0 {
		b.Started(bctx, m.version)
	}
	return nil
}

func (m *Manager) operators(ctx context.Context) ([]int64, error) {
	raw, err := m.st.Meta(ctx, keyOps)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, p := range strings.Split(raw, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ErrBadToken: Telegram does not know the token.
var ErrBadToken = errors.New("токен не подошёл: скопируйте его из @BotFather целиком")

// Connect checks the token with Telegram, saves it and starts the bot; the old bot, if any, stops.
func (m *Manager) Connect(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if !validToken(token) {
		return ErrBadToken
	}
	me, err := m.newAPI(token).Me(ctx)
	if err != nil || me.Username == "" {
		return ErrBadToken
	}
	for k, v := range map[string]string{keyToken: token, keyName: me.Username} {
		if err := m.st.SetMeta(ctx, k, v); err != nil {
			return err
		}
	}
	if _, err := m.newLink(ctx); err != nil {
		return err
	}
	return m.run(ctx, token, me.Username)
}

// validToken: 123456:ABC-def… — digits, a colon, then letters, digits, - and _.
func validToken(t string) bool {
	id, secret, ok := strings.Cut(t, ":")
	if !ok || len(id) < 5 || len(secret) < 30 || len(t) > 100 {
		return false
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return false
	}
	for _, r := range secret {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// Disconnect stops the bot and forgets its token and operators.
func (m *Manager) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	if m.stop != nil {
		m.stop()
	}
	m.bot, m.stop, m.name = nil, nil, ""
	m.mu.Unlock()
	for _, k := range []string{keyToken, keyName, keyOps, keyLink} {
		if err := m.st.SetMeta(ctx, k, ""); err != nil {
			return err
		}
	}
	return nil
}

// Status is what the cabinet shows: the bot's name, how many people get the orders, and the link that
// makes one more (a fresh code each time it is used).
type Status struct {
	Name      string
	Operators int
	Link      string
}

// BotStatus is Status for the cabinet (the web package does not import this one).
func (m *Manager) BotStatus(ctx context.Context) (name string, operators int, link string) {
	s := m.Status(ctx)
	return s.Name, s.Operators, s.Link
}

func (m *Manager) Status(ctx context.Context) Status {
	m.mu.Lock()
	name, running := m.name, m.bot != nil
	m.mu.Unlock()
	if !running {
		return Status{}
	}
	ops, _ := m.operators(ctx)
	code, _ := m.st.Meta(ctx, keyLink)
	if code == "" {
		code, _ = m.newLink(ctx)
	}
	return Status{Name: name, Operators: len(ops), Link: "https://t.me/" + name + "?start=" + code}
}

func (m *Manager) newLink(ctx context.Context) (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := hex.EncodeToString(b)
	return code, m.st.SetMeta(ctx, keyLink, code)
}

// join: someone opened the cabinet's link and pressed Start. The code works once.
func (m *Manager) join(ctx context.Context, id int64, code string) bool {
	want, err := m.st.Meta(ctx, keyLink)
	if err != nil || want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(code)) != 1 {
		return false
	}
	ops, err := m.operators(ctx)
	if err != nil {
		return false
	}
	parts := make([]string, 0, len(ops)+1)
	for _, o := range ops {
		if o == id {
			return true
		}
		parts = append(parts, strconv.FormatInt(o, 10))
	}
	parts = append(parts, strconv.FormatInt(id, 10))
	if err := m.st.SetMeta(ctx, keyOps, strings.Join(parts, ",")); err != nil {
		return false
	}
	if _, err := m.newLink(ctx); err != nil {
		m.log.Warn("bot link", "err", err)
	}
	return true
}

func (m *Manager) current() *Bot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bot
}

// The order news goes to the bot running now, if any.
func (m *Manager) OrderCreated(ctx context.Context, o *order.Order, d antifraud.Decision) {
	if b := m.current(); b != nil {
		b.OrderCreated(ctx, o, d)
	}
}

func (m *Manager) OrderChanged(ctx context.Context, o *order.Order, ev order.Event) {
	if b := m.current(); b != nil {
		b.OrderChanged(ctx, o, ev)
	}
}

func (m *Manager) Refused(ctx context.Context, why string, d antifraud.Decision) {
	if b := m.current(); b != nil {
		b.Refused(ctx, why, d)
	}
}
