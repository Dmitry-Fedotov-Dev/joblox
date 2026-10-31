package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// The owner's back office at /admin, made after the admin of «Мамина мастерская»: a home page with what needs
// doing, the orders with a page each, the products, the settings. The load dashboard moved to /admin/load.

func (s *Server) backofficeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", s.ownerOnly(s.adminHome))
	mux.HandleFunc("GET /admin/orders", s.ownerOnly(s.adminOrders))
	mux.HandleFunc("GET /admin/orders/{id}", s.ownerOnly(s.adminOrder))
	mux.HandleFunc("GET /admin/load", s.ownerOnly(s.adminPage))
}

// orderRow is one line of an order table.
type orderRow struct {
	ID       int64
	When     string
	Customer string
	Phone    string
	Items    int
	Total    string
	State    string
	Tone     string // the colour of the status pill: wait, check, work, done, off
}

type homeView struct {
	base
	Wait, Check, Work, Review int
	Month                     string // revenue since the first of the month
	MonthOrders               int
	Rows                      []orderRow
	Products, Shown           int
	Low                       []string // products running out
	NoBot                     bool
	Prepay                    int
}

type ordersView struct {
	base
	Filter string
	Tabs   []tab
	Rows   []orderRow
}

type tab struct {
	ID, Title string
	Count     int
}

type orderPage struct {
	base
	ownerOrder
	Row    orderRow
	Events []eventRow
	Link   string
	Lines  bool
}

type eventRow struct {
	When, Who, What string
}

// tone sorts an order into what the owner has to do about it.
func (s *Server) tone(o *order.Order) string {
	st, _ := s.svc.P.State(o.State)
	switch {
	case st.Final && (o.Paid || strings.Contains(st.ID, "done") || strings.Contains(st.ID, "complet")):
		return "done"
	case st.Final:
		return "off"
	case o.ReportedAt != nil && !o.Paid:
		return "check"
	case o.Review && !o.Approved, !o.Priced, o.Method == profile.MethodTransfer && !o.Prepaid():
		return "wait"
	}
	return "work"
}

func (s *Server) row(o *order.Order) orderRow {
	st, _ := s.svc.P.State(o.State)
	r := orderRow{ID: o.ID, When: o.CreatedAt.In(s.loc()).Format("02.01 15:04"), Customer: o.Fields["name"],
		Phone: o.Fields["phone"], State: st.Title, Tone: s.tone(o), Total: "цена по запросу"}
	if r.Customer == "" {
		r.Customer = o.Fields["company"]
	}
	if o.Priced {
		r.Total = o.Total.String() + " " + currencySign(s.svc.P.Business.Currency)
	}
	for _, l := range o.Lines {
		r.Items += l.Qty
	}
	return r
}

func (s *Server) loc() *time.Location {
	if l, err := time.LoadLocation(s.svc.P.Business.Timezone); err == nil {
		return l
	}
	return time.UTC
}

func (s *Server) adminHome(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v := homeView{base: s.base(w, r, "Главная"), Prepay: s.svc.PrepayPercent(ctx)}
	v.Nav = "home"
	open, err := s.svc.Store.List(ctx, order.Filter{Open: true, Limit: 500}, s.svc.Finals())
	if err != nil {
		s.message(w, r, http.StatusInternalServerError, "Ошибка", "Заказы не загрузились, обновите страницу.")
		return
	}
	for _, o := range open {
		switch s.tone(o) {
		case "wait":
			v.Wait++
		case "check":
			v.Check++
		default:
			v.Work++
		}
		if o.Review && !o.Approved {
			v.Review++
		}
		if len(v.Rows) < 12 {
			v.Rows = append(v.Rows, s.row(o))
		}
	}
	now := time.Now().In(s.loc())
	if st, err := s.svc.Store.Stats(ctx, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), s.svc.Finals()); err == nil {
		v.Month, v.MonthOrders = st.RevenueDay.String()+" "+currencySign(s.svc.P.Business.Currency), st.Today
	}
	if s.cfg.Shop != nil && s.hasCatalog() {
		if list, err := s.cfg.Shop.List(ctx, true); err == nil {
			v.Products = len(list)
			for _, p := range list {
				if p.Hidden {
					continue
				}
				v.Shown++
				if p.Preorder || s.isServices() {
					continue
				}
				for _, vr := range p.Variants {
					if vr.Stock <= 1 {
						name := p.Title
						if vr.Name != "" && len(p.Variants) > 1 {
							name += " (" + vr.Name + ")"
						}
						v.Low = append(v.Low, name+": "+map[bool]string{true: "нет", false: "1 шт."}[vr.Stock == 0])
					}
				}
			}
		}
	}
	if s.cfg.Bot != nil {
		name, _, _ := s.cfg.Bot.BotStatus(ctx)
		v.NoBot = name == ""
	}
	s.render(w, "home", http.StatusOK, v)
}

func (s *Server) adminOrders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := r.URL.Query().Get("status")
	if filter == "" {
		filter = "active"
	}
	all, err := s.svc.Store.List(ctx, order.Filter{Limit: 1000}, s.svc.Finals())
	if err != nil {
		s.message(w, r, http.StatusInternalServerError, "Ошибка", "Заказы не загрузились, обновите страницу.")
		return
	}
	v := ordersView{base: s.base(w, r, "Заказы"), Filter: filter}
	v.Nav = "orders"
	count := map[string]int{}
	for _, o := range all {
		count[o.State]++
	}
	for _, st := range s.svc.P.Workflow.States {
		v.Tabs = append(v.Tabs, tab{ID: st.ID, Title: st.Title, Count: count[st.ID]})
	}
	for _, o := range all {
		st, _ := s.svc.P.State(o.State)
		if filter == "all" || (filter == "active" && !st.Final) || filter == o.State {
			v.Rows = append(v.Rows, s.row(o))
		}
	}
	s.render(w, "orders", http.StatusOK, v)
}

func (s *Server) adminOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	o, err := s.svc.Store.Get(ctx, id)
	if err != nil {
		s.message(w, r, http.StatusNotFound, "Заказ не найден", "Вернитесь к списку заказов.")
		return
	}
	v := orderPage{base: s.base(w, r, "Заказ №"+strconv.FormatInt(id, 10)), ownerOrder: s.ownerCard(o), Row: s.row(o),
		Link: s.svc.BaseURL() + "/o/" + o.Token, Lines: len(o.Lines) > 0}
	v.Nav = "orders"
	if evs, err := s.svc.Store.Events(ctx, o.ID); err == nil {
		for _, e := range evs {
			who := map[string]string{profile.ActorCustomer: "покупатель", profile.ActorOperator: "вы", profile.ActorSystem: "сайт"}[e.Actor]
			what := e.Note
			if e.Kind == "transition" {
				if st, ok := s.svc.P.State(e.To); ok {
					what = "→ " + st.Title
				}
			}
			if what == "" {
				what = e.Kind
			}
			v.Events = append(v.Events, eventRow{When: e.At.In(s.loc()).Format("02.01 15:04"), Who: who, What: what})
		}
		for i, j := 0, len(v.Events)-1; i < j; i, j = i+1, j-1 {
			v.Events[i], v.Events[j] = v.Events[j], v.Events[i]
		}
	}
	s.render(w, "aorder", http.StatusOK, v)
}
