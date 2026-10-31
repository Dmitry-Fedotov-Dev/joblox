package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The shared bot of the hosting tariff: one Telegram bot for every shop on the network's own hosting. A shop
// connects it from its back office with one button; its orders then come to the seller's Telegram with the
// same buttons as with a bot of its own. The hub is the go-between:
//
//   - a shop proves it owns its address (the hub calls the address back) and gets a key;
//   - the shop's bot talks to POST /api/v1/relay/tg/{method} as if it were Telegram: the hub lets it write
//     only to the chats linked to it, tags its buttons with its number and sends with the shared token;
//   - what the seller presses or types comes back to the shop, signed with its key, at /bot/relay/update.
//
// So the network sees the order news passing through: the hosting tariff says so; a shop that wants nobody
// in between connects its own bot instead.

type relay struct {
	h       *Hub
	token   string
	name    string // the bot's @username
	api     string // https://api.telegram.org/bot<token>
	client  *http.Client
	mu      sync.Mutex
	pending map[string]relayCB // callback query id → the shop it went to, to let only that shop answer it
}

type relayCB struct {
	site int64
	at   time.Time
}

type relaySite struct {
	ID     int64
	URL    string
	Name   string
	Secret string
}

func (h *Hub) relayTables() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS relay_sites (id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL, secret TEXT NOT NULL, created INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS relay_chats (chat INTEGER NOT NULL, site INTEGER NOT NULL, active INTEGER NOT NULL,
			PRIMARY KEY (chat, site))`,
		`CREATE TABLE IF NOT EXISTS relay_codes (code TEXT PRIMARY KEY, site INTEGER NOT NULL, expires INTEGER NOT NULL)`,
	} {
		if _, err := h.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// telegramAPI and siteAddr are replaced in tests: Telegram, and where a shop's address really is.
var (
	telegramAPI = "https://api.telegram.org/bot"
	siteAddr    = func(u string) string { return u }
)

// StartBot runs the shared bot with the token (JOBLOX_HUB_BOT_TOKEN) until ctx ends.
func (h *Hub) StartBot(ctx context.Context, token string) error {
	r := &relay{h: h, token: token, api: telegramAPI + token,
		client: &http.Client{Timeout: 70 * time.Second}, pending: map[string]relayCB{}}
	var me struct {
		Username string `json:"username"`
	}
	if err := r.tg(ctx, "getMe", map[string]any{}, &me); err != nil {
		return fmt.Errorf("shared bot: %w", err)
	}
	r.name = me.Username
	h.mu.Lock()
	h.relay = r
	h.mu.Unlock()
	go r.poll(ctx)
	h.log.Info("shared bot", "name", r.name)
	return nil
}

func (h *Hub) bot() *relay {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.relay
}

// tg calls Telegram with the shared token.
func (r *relay) tg(ctx context.Context, method string, payload, out any) error {
	raw, err := r.raw(ctx, method, payload)
	if err != nil {
		return err
	}
	var rep struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return err
	}
	if !rep.OK {
		return fmt.Errorf("telegram %s: %s", method, rep.Description)
	}
	if out != nil {
		return json.Unmarshal(rep.Result, out)
	}
	return nil
}

func (r *relay) raw(ctx context.Context, method string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.api+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, errors.New("telegram unreachable") // the error would carry the token in the address
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- the shops' side of the API

func (h *Hub) relayRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/relay", h.relayInfo)
	mux.HandleFunc("POST /api/v1/relay/register", h.relayRegister)
	mux.HandleFunc("POST /api/v1/relay/link", h.relayLink)
	mux.HandleFunc("POST /api/v1/relay/off", h.relayOff)
	mux.HandleFunc("POST /api/v1/relay/tg/{method}", h.relayTG)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// relayInfo says whether the shared bot runs and what it is called.
func (h *Hub) relayInfo(w http.ResponseWriter, _ *http.Request) {
	r := h.bot()
	if r == nil {
		writeJSON(w, http.StatusOK, map[string]any{"on": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"on": true, "bot": r.name})
}

// verifier calls a shop back to see it asked for the registration itself. Tests replace it.
var verifier = func(ctx context.Context, site, nonce string) bool {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, siteAddr(site)+"/bot/relay/verify?nonce="+url.QueryEscape(nonce), nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return resp.StatusCode == http.StatusOK && strings.TrimSpace(string(b)) == "ok"
}

// relayRegister: a shop on the network's hosting asks for its key. The hub calls the shop's address back with
// the shop's nonce: only the real shop answers, so nobody can take another shop's place.
func (h *Hub) relayRegister(w http.ResponseWriter, r *http.Request) {
	if h.bot() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "общий бот выключен"})
		return
	}
	var in struct{ URL, Name, Nonce string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	in.URL = strings.TrimRight(in.URL, "/")
	if !hosted(in.URL) || len(in.Nonce) < 16 || len(in.Name) > 200 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "общий бот только для сайтов на хостинге openline.one"})
		return
	}
	if !h.allow("relay:"+in.URL, clientAddr(r)) || !verifier(r.Context(), in.URL, in.Nonce) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "сайт не подтвердил запрос"})
		return
	}
	secret := randomHex(32)
	if _, err := h.db.ExecContext(r.Context(), `INSERT INTO relay_sites (url, name, secret, created) VALUES (?,?,?,?)
		ON CONFLICT(url) DO UPDATE SET name=excluded.name, secret=excluded.secret`, in.URL, in.Name, secret, h.now().Unix()); err != nil {
		http.Error(w, "db", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "bot": h.bot().name})
}

// site finds the calling shop by its address and key.
func (h *Hub) site(r *http.Request) (*relaySite, bool) {
	u, key := strings.TrimRight(r.Header.Get("X-Relay-Site"), "/"), r.Header.Get("X-Relay-Secret")
	if u == "" || key == "" {
		return nil, false
	}
	var s relaySite
	err := h.db.QueryRowContext(r.Context(), `SELECT id, url, name, secret FROM relay_sites WHERE url=?`, u).Scan(&s.ID, &s.URL, &s.Name, &s.Secret)
	if err != nil || subtle.ConstantTimeCompare([]byte(s.Secret), []byte(key)) != 1 {
		return nil, false
	}
	return &s, true
}

func (h *Hub) chats(ctx context.Context, site int64) ([]int64, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT chat FROM relay_chats WHERE site=?`, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var c int64
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// relayLink gives the shop a one-time link for the seller: open it in Telegram, press Start, and the chat is
// linked to the shop. It also says how many chats get the shop's orders now.
func (h *Hub) relayLink(w http.ResponseWriter, r *http.Request) {
	bot := h.bot()
	s, ok := h.site(r)
	if bot == nil || !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "нет доступа"})
		return
	}
	code := randomHex(10)
	ctx := r.Context()
	_, _ = h.db.ExecContext(ctx, `DELETE FROM relay_codes WHERE expires < ?`, h.now().Unix())
	if _, err := h.db.ExecContext(ctx, `INSERT INTO relay_codes (code, site, expires) VALUES (?,?,?)`, code, s.ID, h.now().Add(30*time.Minute).Unix()); err != nil {
		http.Error(w, "db", http.StatusInternalServerError)
		return
	}
	cs, _ := h.chats(ctx, s.ID)
	writeJSON(w, http.StatusOK, map[string]any{"link": "https://t.me/" + bot.name + "?start=" + code, "bot": bot.name, "chats": len(cs)})
}

// relayOff unlinks every chat from the shop: the shop no longer uses the shared bot.
func (h *Hub) relayOff(w http.ResponseWriter, r *http.Request) {
	s, ok := h.site(r)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "нет доступа"})
		return
	}
	_, _ = h.db.ExecContext(r.Context(), `DELETE FROM relay_chats WHERE site=?`, s.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// relayTG is Telegram for a shop's bot: the shop may write only to its own chats (chat 0: all of them), its
// buttons get its number, and it may answer only the button presses that went to it.
func (h *Hub) relayTG(w http.ResponseWriter, r *http.Request) {
	bot := h.bot()
	s, ok := h.site(r)
	if bot == nil || !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "description": "нет доступа"})
		return
	}
	var p map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "description": "bad json"})
		return
	}
	ctx := r.Context()
	mine, _ := h.chats(ctx, s.ID)
	linked := func(c int64) bool {
		for _, m := range mine {
			if m == c {
				return true
			}
		}
		return false
	}
	method := r.PathValue("method")
	switch method {
	case "sendMessage", "editMessageText":
		tagButtons(p, s.ID)
		chat := toInt(p["chat_id"])
		if method == "sendMessage" && chat == 0 { // to everyone who gets this shop's orders
			var first []byte
			for _, c := range mine {
				p["chat_id"] = c
				raw, err := bot.raw(ctx, method, p)
				if err == nil && first == nil {
					first = raw
				}
			}
			if first == nil {
				first = []byte(`{"ok":true,"result":{"message_id":0,"chat":{"id":0}}}`)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(first)
			return
		}
		if !linked(chat) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "description": "чат не подключён к этому сайту"})
			return
		}
	case "answerCallbackQuery":
		id, _ := p["callback_query_id"].(string)
		bot.mu.Lock()
		cb, ok := bot.pending[id]
		bot.mu.Unlock()
		if !ok || cb.site != s.ID {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": true}) // stale or not its own: quietly nothing
			return
		}
	default:
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "description": "метод недоступен"})
		return
	}
	raw, err := bot.raw(ctx, method, p)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "description": "telegram unreachable"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

// tagButtons prefixes the callback data of inline buttons with the shop's number: «7|p:12».
func tagButtons(p map[string]any, site int64) {
	mk, ok := p["reply_markup"].(map[string]any)
	if !ok {
		return
	}
	rows, ok := mk["inline_keyboard"].([]any)
	if !ok {
		return
	}
	for _, row := range rows {
		btns, _ := row.([]any)
		for _, b := range btns {
			if m, ok := b.(map[string]any); ok {
				if d, ok := m["callback_data"].(string); ok {
					m["callback_data"] = strconv.FormatInt(site, 10) + "|" + d
				}
			}
		}
	}
}

// ---------------------------------------------------------------- Telegram's side

type tgUpdate struct {
	UpdateID int64           `json:"update_id"`
	Message  json.RawMessage `json:"message,omitempty"`
	Callback json.RawMessage `json:"callback_query,omitempty"`
}

type tgMsg struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

type tgCB struct {
	ID      string `json:"id"`
	Data    string `json:"data"`
	Message *tgMsg `json:"message"`
}

func (r *relay) poll(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		var ups []tgUpdate
		if err := r.tg(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 30,
			"allowed_updates": []string{"message", "callback_query"}}, &ups); err != nil {
			if ctx.Err() == nil {
				r.h.log.Warn("shared bot updates", "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
			}
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			r.handle(ctx, u)
		}
	}
}

func (r *relay) say(ctx context.Context, chat int64, text string, kb any) {
	p := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if kb != nil {
		p["reply_markup"] = kb
	}
	if err := r.tg(ctx, "sendMessage", p, nil); err != nil {
		r.h.log.Warn("shared bot send", "err", err)
	}
}

func (r *relay) handle(ctx context.Context, u tgUpdate) {
	db := r.h.db
	if len(u.Callback) > 0 {
		var cb tgCB
		if json.Unmarshal(u.Callback, &cb) != nil || cb.Message == nil {
			return
		}
		num, data, ok := strings.Cut(cb.Data, "|")
		site, err := strconv.ParseInt(num, 10, 64)
		if !ok || err != nil {
			return
		}
		if site == 0 && strings.HasPrefix(data, "use:") { // the seller switches between their shops
			to, _ := strconv.ParseInt(strings.TrimPrefix(data, "use:"), 10, 64)
			_, _ = db.ExecContext(ctx, `UPDATE relay_chats SET active = (site = ?) WHERE chat=?`, to, cb.Message.Chat.ID)
			_ = r.tg(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": "Готово"}, nil)
			return
		}
		var n int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_chats WHERE chat=? AND site=?`, cb.Message.Chat.ID, site).Scan(&n)
		if n == 0 {
			_ = r.tg(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": "Нет доступа"}, nil)
			return
		}
		r.mu.Lock()
		now := time.Now()
		for k, v := range r.pending {
			if now.Sub(v.at) > 10*time.Minute {
				delete(r.pending, k)
			}
		}
		r.pending[cb.ID] = relayCB{site: site, at: now}
		r.mu.Unlock()
		var full map[string]any
		_ = json.Unmarshal(u.Callback, &full)
		full["data"] = data
		r.forward(ctx, site, map[string]any{"update_id": u.UpdateID, "callback_query": full})
		return
	}
	if len(u.Message) == 0 {
		return
	}
	var m tgMsg
	if json.Unmarshal(u.Message, &m) != nil {
		return
	}
	chat := m.Chat.ID
	cmd, arg, _ := strings.Cut(strings.TrimSpace(m.Text), " ")
	if cmd == "/start" && arg != "" { // the link from a shop's back office
		var site int64
		err := db.QueryRowContext(ctx, `SELECT site FROM relay_codes WHERE code=? AND expires>=?`, arg, time.Now().Unix()).Scan(&site)
		if err != nil {
			r.say(ctx, chat, "Ссылка устарела. Откройте её заново в админке своего сайта: «Настройки» → «Telegram-бот».", nil)
			return
		}
		_, _ = db.ExecContext(ctx, `DELETE FROM relay_codes WHERE code=?`, arg)
		_, _ = db.ExecContext(ctx, `UPDATE relay_chats SET active=0 WHERE chat=?`, chat)
		_, _ = db.ExecContext(ctx, `INSERT INTO relay_chats (chat, site, active) VALUES (?,?,1)
			ON CONFLICT(chat, site) DO UPDATE SET active=1`, chat, site)
		var full map[string]any
		_ = json.Unmarshal(u.Message, &full)
		full["text"] = "/start"
		r.forward(ctx, site, map[string]any{"update_id": u.UpdateID, "message": full})
		return
	}
	if cmd == "/shops" {
		r.shops(ctx, chat)
		return
	}
	var site int64
	if err := db.QueryRowContext(ctx, `SELECT site FROM relay_chats WHERE chat=? ORDER BY active DESC LIMIT 1`, chat).Scan(&site); err != nil {
		r.say(ctx, chat, "Это бот Joblox для продавцов на хостинге openline.one. Подключите его в админке своего сайта: "+
			"«Настройки» → «Telegram-бот» → «Подключить бот Joblox».", nil)
		return
	}
	r.forward(ctx, site, map[string]any{"update_id": u.UpdateID, "message": u.Message})
}

// shops lets a seller with several shops pick the one the menu works with.
func (r *relay) shops(ctx context.Context, chat int64) {
	rows, err := r.h.db.QueryContext(ctx, `SELECT s.id, s.name, c.active FROM relay_chats c JOIN relay_sites s ON s.id=c.site WHERE c.chat=?`, chat)
	if err != nil {
		return
	}
	defer rows.Close()
	var kb [][]map[string]string
	for rows.Next() {
		var id int64
		var name string
		var active bool
		if rows.Scan(&id, &name, &active) == nil {
			mark := ""
			if active {
				mark = "✓ "
			}
			kb = append(kb, []map[string]string{{"text": mark + name, "callback_data": "0|use:" + strconv.FormatInt(id, 10)}})
		}
	}
	if len(kb) == 0 {
		r.say(ctx, chat, "Сайты пока не подключены.", nil)
		return
	}
	r.say(ctx, chat, "С каким сайтом работает меню:", map[string]any{"inline_keyboard": kb})
}

// forward hands an update to the shop, signed with its key.
func (r *relay) forward(ctx context.Context, site int64, upd any) {
	var s relaySite
	if err := r.h.db.QueryRowContext(ctx, `SELECT id, url, name, secret FROM relay_sites WHERE id=?`, site).Scan(&s.ID, &s.URL, &s.Name, &s.Secret); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.h.log.Warn("shared bot site", "err", err)
		}
		return
	}
	body, err := json.Marshal(upd)
	if err != nil {
		return
	}
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write(body)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, siteAddr(s.URL)+"/bot/relay/update", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Relay-Sign", hex.EncodeToString(mac.Sum(nil)))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		r.h.log.Warn("shared bot forward", "site", s.URL, "err", err)
		return
	}
	_ = resp.Body.Close()
}
