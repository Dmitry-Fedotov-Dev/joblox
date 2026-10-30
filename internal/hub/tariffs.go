package hub

import (
	"net/http"
	"net/url"
	"strings"
)

// The start page opens with three ways to get a site: on your own for free, hosted on the network's server
// for a monthly fee, or set up turnkey for a one-off payment. Requests for the paid ones go to Sales, a
// Telegram chat (a person now, an agent later) with the request already typed in.

// DefaultSales is where the requests go unless JOBLOX_HUB_SALES says otherwise.
const DefaultSales = "https://t.me/HowManyRoads"

var kindPhrases = map[string]string{
	"shop": "такой же магазин", "master": "такую же страничку мастера", "taxi": "такой же таксопарк",
	"bakery": "такую же пекарню", "delivery": "такую же доставку", "freight": "такой же сервис перевозок",
}

type startView struct {
	Roles   []Card
	Contact string
	Kind    string // the kind of site the visitor came for from a performer's «make the same» button
	Want    string // «такой же магазин»; empty when the visitor came from the market
	Hosting string // Telegram link with the hosting request typed in
	Turnkey string
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
		Hosting: h.salesLink("Здравствуйте! Хочу " + what + " на хостинге openline.one, тариф 7 $ в месяц."),
		Turnkey: h.salesLink("Здравствуйте! Хочу " + what + " под ключ, оформить на моё ИП / ООО.")})
}
