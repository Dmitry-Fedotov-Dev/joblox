package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
)

// A quick order (a profile with checkout.after): «Запустить на хостинге» opens /quick?add=hosting, the page
// places the order at once and the customer lands on it — the status, the sum and the requisites, no form.
// The contact is asked after «Я оплатил», by one tap through Telegram or one field.

// quickOrder is the one-tap start: a page whose form sends itself. A link does not place an order (a crawler or a
// link preview would); a form the browser sends does, and a robot that does not run scripts sees a button.
func (s *Server) quickOrder(w http.ResponseWriter, r *http.Request) {
	p := s.svc.P
	it, ok := p.Item(r.URL.Query().Get("add"))
	if !p.Checkout.Quick() || !ok || it.Hidden {
		s.redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "quick", http.StatusOK, struct {
		base
		Item  string
		Title string
		Stamp string
	}{s.base(w, r, it.Title), it.ID, it.Title, s.formStamp(time.Now())})
}

// monthChoice is one button of «1 мес · 3 мес · 12 мес» on the order page.
type monthChoice struct {
	Qty   int
	Label string
	On    bool
}

// choices are the quantity buttons of an order still waiting for money, nil when there is nothing to choose.
func (s *Server) choices(o *order.Order) []monthChoice {
	if len(o.Lines) != 1 || o.Paid || o.ReportedAt != nil || o.AdvanceAt != nil || o.State != s.svc.Flow.Initial() {
		return nil
	}
	it, ok := s.svc.P.Item(o.Lines[0].ItemID)
	if !ok || len(it.Choices) < 2 {
		return nil
	}
	var out []monthChoice
	for _, q := range it.Choices {
		label := strconv.Itoa(q)
		if it.Unit != "" {
			label += " " + it.Unit
		}
		out = append(out, monthChoice{Qty: q, Label: label, On: q == o.Lines[0].Qty})
	}
	return out
}

// askAfter is the contact card of a quick order: after «Я оплатил», while the contact is still empty.
func (s *Server) askAfter(o *order.Order, typed map[string]string, errs order.FieldErrors) []fieldView {
	p := s.svc.P
	if !p.Checkout.Quick() || o.ReportedAt == nil || s.svc.IsFinal(o.State) || (o.Fields[p.Checkout.After[0]] != "" && typed == nil) {
		return nil
	}
	var out []fieldView
	for i, id := range p.Checkout.After {
		for _, f := range p.Checkout.Fields {
			if f.ID != id {
				continue
			}
			f.Required = i == 0
			v := fieldView{Field: f, Value: o.Fields[id]}
			if typed != nil {
				v.Value = typed[id]
			}
			for _, e := range errs {
				if e.Field == id {
					v.Error = e.Msg
				}
			}
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) orderQty(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	q, _ := strconv.Atoi(r.PostFormValue("qty"))
	if err := s.svc.SetQty(r.Context(), o, q); err != nil && !errors.Is(err, order.ErrForbidden) {
		s.log.Error("order qty", "err", err)
	}
	s.redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
}

func (s *Server) orderContact(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	typed := map[string]string{}
	for _, id := range s.svc.P.Checkout.After {
		typed[id] = r.PostFormValue("f_" + id)
	}
	err := s.svc.SetFields(r.Context(), o, typed)
	var fe order.FieldErrors
	switch {
	case errors.As(err, &fe):
		v := s.orderView(w, r, o, "", "")
		v.Ask = s.askAfter(o, typed, fe)
		s.render(w, "order", http.StatusUnprocessableEntity, v)
		return
	case err != nil && !errors.Is(err, order.ErrForbidden):
		s.log.Error("order contact", "err", err)
	}
	s.redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
}

// orderTelegram takes what Telegram sent back after «Продолжить через Telegram» (the page passes it on from the
// address): the account, signed with the bot's token.
func (s *Server) orderTelegram(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	u, err := verifyTelegram(s.cfg.TelegramLogin, r.PostFormValue("data"), time.Now())
	if err != nil {
		s.log.Info("telegram login refused", "err", err)
		v := s.orderView(w, r, o, "", "Telegram не подтвердил вход. Попробуйте ещё раз или впишите контакт.")
		s.render(w, "order", http.StatusForbidden, v)
		return
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if err := s.svc.SetTelegram(r.Context(), o, u.ID, name, u.Username); err != nil && !errors.Is(err, order.ErrForbidden) {
		s.log.Error("order telegram", "err", err)
	}
	s.redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
}

// telegramLogin is the address of Telegram's sign-in for the bot, coming back to the order page; empty without
// the bot's token. Telegram shows it only on the domain the bot was given (BotFather → /setdomain).
func (s *Server) telegramLogin(o *order.Order) string {
	token := s.cfg.TelegramLogin
	id, _, ok := strings.Cut(token, ":")
	if token == "" || !ok {
		return ""
	}
	back, err := url.Parse(s.svc.Link(o))
	if err != nil {
		return ""
	}
	q := url.Values{"bot_id": {id}, "origin": {back.Scheme + "://" + back.Host}, "request_access": {"write"}, "return_to": {back.String()}}
	return "https://oauth.telegram.org/auth?" + q.Encode()
}

type tgUser struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
}

// verifyTelegram checks what Telegram's sign-in returned (base64 of a JSON object): the hash is the HMAC-SHA-256
// of the other fields, sorted «key=value» lines, keyed by the SHA-256 of the bot's token; a day old at most.
func verifyTelegram(token, data string, now time.Time) (tgUser, error) {
	if token == "" {
		return tgUser{}, errors.New("no bot token")
	}
	data = strings.TrimRight(strings.TrimSpace(data), "=")
	raw, err := base64.RawURLEncoding.DecodeString(strings.NewReplacer("+", "-", "/", "_").Replace(data))
	if err != nil {
		return tgUser{}, fmt.Errorf("not base64: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return tgUser{}, fmt.Errorf("not JSON: %w", err)
	}
	hash, _ := m["hash"].(string)
	var lines []string
	for k, v := range m {
		if k == "hash" {
			continue
		}
		lines = append(lines, k+"="+fmt.Sprint(v))
	}
	sort.Strings(lines)
	key := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(strings.Join(lines, "\n")))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(hash))) {
		return tgUser{}, errors.New("bad hash")
	}
	num := func(k string) int64 {
		n, _ := m[k].(json.Number)
		v, _ := n.Int64()
		return v
	}
	str := func(k string) string { v, _ := m[k].(string); return v }
	if at := time.Unix(num("auth_date"), 0); now.Sub(at) > 24*time.Hour || at.After(now.Add(5*time.Minute)) {
		return tgUser{}, errors.New("too old")
	}
	u := tgUser{ID: num("id"), FirstName: str("first_name"), LastName: str("last_name"), Username: str("username")}
	if u.ID <= 0 {
		return tgUser{}, errors.New("no account")
	}
	return u, nil
}
