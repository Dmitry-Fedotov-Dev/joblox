package hub

import (
	"net/http"
	"net/url"
	"strings"
)

// The start page opens with three ways to get a site: on your own for free, hosted on the network's server
// for a monthly fee, or set up turnkey for a one-off payment. The paid ones are ordered and paid in full up
// front on the order site (Order, a Joblox site itself, the Agent Pay scheme); questions go to Sales, a
// Telegram chat with the question already typed in.

// DefaultOrder is the tariff order site unless JOBLOX_HUB_ORDER says otherwise.
const DefaultOrder = "/order/"

// DefaultSales is where the requests go unless JOBLOX_HUB_SALES says otherwise.
const DefaultSales = "https://t.me/how_many_roads"

var kindPhrases = map[string]string{
	"shop": "такой же магазин", "master": "такую же страничку мастера", "taxi": "такой же таксопарк",
	"bakery": "такую же пекарню", "delivery": "такую же доставку", "freight": "такой же сервис перевозок",
}

type startView struct {
	Roles   []Card
	Contact string
	Kind    string // the kind of site the visitor came for from a performer's «make the same» button
	Want    string // «такой же магазин»; empty when the visitor came from the market
	Hosting string // the order site: pay, then the request is looked at
	Turnkey string
	Ask     string // Telegram link with a question typed in
}

// salesLink opens the sales chat with the request typed in.
func (h *Hub) salesLink(text string) string {
	sales := h.Sales
	if sales == "" {
		sales = DefaultSales
	}
	return sales + "?text=" + strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
}

func (h *Hub) start(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("for")
	want := kindPhrases[kind]
	if want == "" {
		kind = ""
	}
	what := "сайт для заказов"
	if want != "" {
		what = want
	}
	h.render(w, "start.html", startView{Roles: Roles, Contact: h.Contact, Kind: kind, Want: want,
		Hosting: h.orderLink() + "?add=hosting#order", Turnkey: h.orderLink() + "?add=turnkey#order",
		Ask: h.salesLink("Здравствуйте! Вопрос про " + what + " на openline.one:")})
}

func (h *Hub) orderLink() string {
	if h.Order != "" {
		return h.Order
	}
	return DefaultOrder
}
