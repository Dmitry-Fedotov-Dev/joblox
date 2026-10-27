package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Listing is the card an owner chose to show on the market: only what they typed into "Показать на бирже".
// It travels with the heartbeat, so a card disappears by itself when its shop goes offline.
type Listing struct {
	Title   string `json:"title"`
	Place   string `json:"place"`
	About   string `json:"about"`
	Contact string `json:"contact"`
	URL     string `json:"url"`
}

// Card is one tile on the market: a live shop, a project of the network's author or an invitation to start one.
type Card struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Place   string `json:"place,omitempty"`
	About   string `json:"about"`
	Contact string `json:"contact,omitempty"`
	URL     string `json:"url"`
	Badge   string `json:"badge,omitempty"`  // "OpenLine" for the author's own projects
	Invite  bool   `json:"invite,omitempty"` // "start your own": leads to the launch instructions
	Online  int    `json:"online,omitempty"`
}

// Featured are the author's projects. They stand in the same grid as everyone else's cards.
var Featured = []Card{
	{Kind: "call", Title: "Трубка: Контакт", About: "Позвоните клиенту или другу по ссылке и говорите один на один. Только голос, без регистрации и номеров.",
		URL: "https://openline.one/", Badge: "OpenLine"},
	{Kind: "radio", Title: "OpenLine Радио", About: "Открытое радио в браузере: крутите ручку между станциями или займите свою волну с микрофоном и музыкой.",
		URL: "https://openline.one/radio/", Badge: "OpenLine"},
	{Kind: "agent", Title: "Agent Pay", About: "Оплата зарубежных услуг и IT-сервисов для ООО и ИП через агента, с полным комплектом документов для бухгалтерии.",
		URL: "https://pay.openline.one", Badge: "OpenLine"},
}

// Roles are the "become a performer" choices under the big button: one per ready-made profile.
var Roles = []Card{
	{Kind: "taxi", Title: "Водитель такси", About: "Своя машина и телефон. Клиенты заказывают поездку, вы принимаете заказ и называете цену.", URL: "/start#taxi", Invite: true},
	{Kind: "delivery", Title: "Курьер", About: "Доставка посылок, продуктов и документов по селу или городу.", URL: "/start#delivery", Invite: true},
	{Kind: "bakery", Title: "Пекарня у дома", About: "Хлеб и выпечка с самовывозом к удобному времени.", URL: "/start#bakery", Invite: true},
	{Kind: "freight", Title: "Перевозчик", About: "Переезды и грузы. Клиент описывает задачу, вы называете цену.", URL: "/start#freight", Invite: true},
	{Kind: "shop", Title: "Продавец", About: "Свой каталог с корзиной, доставка или самовывоз.", URL: "/start#shop", Invite: true},
}

// Sections are the order in which the app offers services to a customer.
var Sections = []struct{ Kind, Action string }{
	{"taxi", "Заказать такси"}, {"delivery", "Сделать доставку"}, {"bakery", "Свежая выпечка"},
	{"freight", "Перевезти груз"}, {"shop", "Купить"}, {"other", "Другие услуги"},
}

var kindTitles = map[string]string{
	"taxi": "Такси", "bakery": "Пекарни", "delivery": "Доставка", "freight": "Грузоперевозки", "shop": "Магазины",
	"agent": "Оплата услуг", "radio": "Радио", "call": "Связь", "other": "Другое",
}

// KindTitle is the Russian name of a kind of business.
func KindTitle(kind string) string {
	if t, ok := kindTitles[kind]; ok {
		return t
	}
	return kindTitles["other"]
}

// clip trims, folds whitespace and control characters into single spaces and cuts to n letters.
func clip(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// Clean normalises a listing; a listing without a title or a valid https address is dropped.
func (l *Listing) Clean() error {
	l.Title, l.Place, l.About, l.Contact = clip(l.Title, 60), clip(l.Place, 40), clip(l.About, 160), clip(l.Contact, 40)
	if l.Title == "" {
		return errors.New("listing: title is empty")
	}
	u, err := url.Parse(strings.TrimSpace(l.URL))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(l.URL) > 200 {
		return errors.New("listing: url must be an https address")
	}
	l.URL = "https://" + u.Host + "/"
	return nil
}

// Market returns the live performers (busiest first) followed by the author's projects. kind filters by kind
// of business.
func (h *Hub) Market(ctx context.Context, kind string) ([]Card, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT kind, listing, online FROM installs
		WHERE seen_at >= ? AND listing != '' AND id NOT IN (SELECT id FROM hidden) ORDER BY online DESC, seen_at DESC LIMIT 500`,
		h.now().Add(-ActiveWindow).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []Card
	for rows.Next() {
		var k, raw string
		var online int
		if err := rows.Scan(&k, &raw, &online); err != nil {
			return nil, err
		}
		var l Listing
		if json.Unmarshal([]byte(raw), &l) != nil {
			continue
		}
		cards = append(cards, Card{Kind: k, Title: l.Title, Place: l.Place, About: l.About, Contact: l.Contact, URL: l.URL, Online: online})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cards = append(cards, Featured...)
	if kind == "" {
		return cards, nil
	}
	out := cards[:0]
	for _, c := range cards {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out, nil
}

// Hide takes a card off the market for good (the installation keeps counting).
func (h *Hub) Hide(ctx context.Context, id string, hidden bool) error {
	q := `INSERT OR IGNORE INTO hidden (id) VALUES (?)`
	if !hidden {
		q = `DELETE FROM hidden WHERE id = ?`
	}
	_, err := h.db.ExecContext(ctx, q, id)
	return err
}

func (h *Hub) market(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	cards, err := h.Market(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=30")
	_ = json.NewEncoder(w).Encode(map[string]any{"cards": cards})
}

// hide is the network owner's moderation: POST {"id": "...", "hidden": true} with the admin token.
func (h *Hub) hide(w http.ResponseWriter, r *http.Request) {
	if h.AdminToken == "" || !strings.EqualFold(r.Header.Get("Authorization"), "Bearer "+h.AdminToken) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var in struct {
		ID     string `json:"id"`
		Hidden bool   `json:"hidden"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !idRe.MatchString(in.ID) {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := h.Hide(r.Context(), in.ID, in.Hidden); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
