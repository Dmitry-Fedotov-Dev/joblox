package bot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The shared bot of the hosting tariff (the hub's side is internal/hub/relay.go): the shop's bot logic stays
// here, unchanged; only its Telegram is the hub, which sends with the shared token and hands back what the
// seller pressed. A shop on its own server keeps its own bot instead.

const (
	keyMode   = "bot_mode"     // "shared": the hub's bot; "" or "own": a token of the shop's own
	keyRelay  = "relay_secret" // the shop's key at the hub
	modeShare = "shared"
)

// NewRelayAPI is Telegram through the hub: the same calls, signed with the shop's address and key.
func NewRelayAPI(hub, site, secret string) *API {
	return &API{Base: strings.TrimRight(hub, "/") + "/api/v1/relay/tg", HTTP: &http.Client{Timeout: 20 * time.Second},
		Header: map[string]string{"X-Relay-Site": site, "X-Relay-Secret": secret}}
}

// Hub is the network's address (JOBLOX_NETWORK_URL); empty: no shared bot.
func (m *Manager) SetHub(hub string) { m.hub = strings.TrimRight(hub, "/") }

func (m *Manager) site() string { return strings.TrimRight(m.svc.BaseURL(), "/") }

// SharedAvailable: the network runs the shared bot and this shop lives on its hosting.
func (m *Manager) SharedAvailable(ctx context.Context) bool {
	if m.hub == "" || m.svc == nil {
		return false
	}
	u, err := url.Parse(m.site())
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".openline.one") {
		return false
	}
	m.mu.Lock()
	if time.Since(m.infoAt) < time.Minute {
		on := m.infoOn
		m.mu.Unlock()
		return on
	}
	m.mu.Unlock()
	var info struct {
		On  bool   `json:"on"`
		Bot string `json:"bot"`
	}
	on := m.hubJSON(ctx, http.MethodGet, "/api/v1/relay", nil, "", &info) == nil && info.On
	m.mu.Lock()
	m.infoAt, m.infoOn = time.Now(), on
	m.mu.Unlock()
	return on
}

func (m *Manager) hubJSON(ctx context.Context, method, path string, in any, secret string, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, m.hub+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Relay-Site", m.site())
		req.Header.Set("X-Relay-Secret", secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("сеть ответила %s", resp.Status)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ConnectShared registers the shop at the hub (once) and switches its bot to the shared one.
func (m *Manager) ConnectShared(ctx context.Context) error {
	if !m.SharedAvailable(ctx) {
		return errors.New("общий бот сейчас недоступен")
	}
	secret, _ := m.st.Meta(ctx, keyRelay)
	if secret == "" {
		nonce := randomCode()
		m.mu.Lock()
		m.nonce, m.nonceAt = nonce, time.Now()
		m.mu.Unlock()
		var out struct {
			Secret string `json:"secret"`
		}
		err := m.hubJSON(ctx, http.MethodPost, "/api/v1/relay/register",
			map[string]string{"url": m.site(), "name": m.svc.P.Business.Name, "nonce": nonce}, "", &out)
		m.mu.Lock()
		m.nonce = ""
		m.mu.Unlock()
		if err != nil {
			return err
		}
		if secret = out.Secret; secret == "" {
			return errors.New("сеть не выдала ключ")
		}
		if err := m.st.SetMeta(ctx, keyRelay, secret); err != nil {
			return err
		}
	}
	if err := m.st.SetMeta(ctx, keyMode, modeShare); err != nil {
		return err
	}
	m.runShared(secret)
	return nil
}

func (m *Manager) runShared(secret string) {
	b := New(NewRelayAPI(m.hub, m.site(), secret), m.svc, nil, m.log)
	b.Relay = true
	m.mu.Lock()
	if m.stop != nil {
		m.stop()
	}
	m.bot, m.stop, m.name, m.shared, m.link = b, nil, "", true, sharedLink{}
	m.mu.Unlock()
}

// DisconnectShared unlinks the shop's chats at the hub and stops using the shared bot.
func (m *Manager) DisconnectShared(ctx context.Context) error {
	if secret, _ := m.st.Meta(ctx, keyRelay); secret != "" && m.hub != "" {
		_ = m.hubJSON(ctx, http.MethodPost, "/api/v1/relay/off", map[string]string{}, secret, nil)
	}
	m.mu.Lock()
	if m.shared {
		m.bot, m.shared = nil, false
	}
	m.mu.Unlock()
	return m.st.SetMeta(ctx, keyMode, "")
}

// VerifyNonce answers the hub's call-back during the registration this shop started.
func (m *Manager) VerifyNonce(nonce string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nonce != "" && nonce == m.nonce && time.Since(m.nonceAt) < 2*time.Minute
}

// RelayUpdate takes what the seller pressed or typed in the shared bot, checks the hub's signature and
// handles it as the shop's own bot would.
func (m *Manager) RelayUpdate(ctx context.Context, body []byte, sign string) error {
	secret, _ := m.st.Meta(ctx, keyRelay)
	m.mu.Lock()
	b, shared := m.bot, m.shared
	m.mu.Unlock()
	if secret == "" || !shared || b == nil {
		return errors.New("shared bot off")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sign)) {
		return errors.New("bad signature")
	}
	var u Update
	if err := json.Unmarshal(body, &u); err != nil {
		return err
	}
	b.Handle(ctx, u)
	return nil
}

// sharedLink caches the hub's one-time link for a while: the back office asks for it on every visit.
type sharedLink struct {
	link, bot string
	chats     int
	at        time.Time
}

func (m *Manager) sharedStatus(ctx context.Context) Status {
	m.mu.Lock()
	l := m.link
	m.mu.Unlock()
	if time.Since(l.at) > 10*time.Minute {
		secret, _ := m.st.Meta(ctx, keyRelay)
		var out struct {
			Link  string `json:"link"`
			Bot   string `json:"bot"`
			Chats int    `json:"chats"`
		}
		if err := m.hubJSON(ctx, http.MethodPost, "/api/v1/relay/link", map[string]string{}, secret, &out); err != nil {
			m.log.Warn("shared bot link", "err", err)
			return Status{Shared: true}
		}
		l = sharedLink{link: out.Link, bot: out.Bot, chats: out.Chats, at: time.Now()}
		m.mu.Lock()
		m.link = l
		m.mu.Unlock()
	}
	return Status{Name: l.bot, Operators: l.chats, Link: l.link, Shared: true}
}
