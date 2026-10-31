package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// base is what every page's layout needs.
type base struct {
	Business profile.Business
	Title    string
	CSRF     string
	Paused   bool
	Shop     bool // a shop with photos: the header shows the cart
	Cart     int  // items in the cart
	Demo     bool
	Cover    string // the address of the cover picture with its version; empty: no cover
	Services bool   // the catalog holds services: cards of work with a request form
	Catalog  bool   // the owner edits products or services in the cabinet
	// Link previews (Open Graph): the page's absolute address, its picture and summary.
	URL, Preview, Summary string
	// Clone is the «make yourself the same» button of the public pages: what to call it and where it leads.
	CloneText, CloneURL string
	// Nav is the back office section the page belongs to (home, orders, products, settings, load); empty on
	// public pages. It shows the back office menu.
	Nav string
}

func (s *Server) base(w http.ResponseWriter, r *http.Request, title string) base {
	v := s.visitor(w, r)
	s.rec.Seen(v)
	b := base{Business: s.svc.P.Business, Title: title, CSRF: s.csrfToken(v), Paused: s.svc.Paused(), Shop: s.isShop(), Demo: s.cfg.Demo, Cover: s.coverURL(),
		Services: s.isServices(), Catalog: s.hasCatalog()}
	if b.Shop {
		b.Cart = s.readCart(r).count()
	}
	b.URL, b.Preview, b.Summary = s.svc.BaseURL()+r.URL.Path, s.sitePreview(), s.svc.P.Business.Description
	if s.cfg.MarketURL != "" && !strings.HasPrefix(r.URL.Path, "/owner") && !strings.HasPrefix(r.URL.Path, "/admin") {
		b.CloneText, b.CloneURL = cloneText(s.svc.P.Business.Kind), strings.TrimRight(s.cfg.MarketURL, "/")+"/start?for="+s.svc.P.Business.Kind
	}
	return b
}

type fieldView struct {
	profile.Field
	Value string
	Error string
}

type itemView struct {
	profile.Item
	Qty   int
	Max   int
	Error string
}

type shopView struct {
	base
	P         *profile.Profile
	Items     []itemView
	Fields    []fieldView
	Methods   []methodView
	Stamp     string
	FormError string
	Quote     bool
	Cards     []productCard // the services catalog (service_catalog in the profile)
	// Picked is the service of a request page; empty on a services site's front page, which then shows
	// the cards and no form.
	Picked string
}

type methodView struct {
	ID, Title string
	Checked   bool
}

func methodTitle(m string) string {
	if m == profile.MethodCash {
		return "При получении"
	}
	return "Переводом по реквизитам"
}

func (s *Server) shopView(w http.ResponseWriter, r *http.Request, form map[string]string, qty map[string]int, method string, errs order.FieldErrors) shopView {
	p := s.svc.P
	byField := map[string]string{}
	formErr := ""
	for _, e := range errs {
		if e.Field == "" {
			formErr = e.Msg
		} else {
			byField[e.Field] = e.Msg
		}
	}
	v := shopView{base: s.base(w, r, p.Business.Name), P: p, Stamp: s.formStamp(time.Now()), FormError: formErr,
		Quote: p.Pricing.Mode == profile.PricingQuote}
	if !v.Quote {
		for _, it := range p.Catalog {
			if it.Hidden {
				continue
			}
			maxQ := it.MaxQty
			if maxQ == 0 {
				maxQ = 99
			}
			v.Items = append(v.Items, itemView{Item: it, Qty: qty[it.ID], Max: maxQ, Error: byField["item_"+it.ID]})
		}
	}
	for _, f := range p.Checkout.Fields {
		v.Fields = append(v.Fields, fieldView{Field: f, Value: form[f.ID], Error: byField[f.ID]})
	}
	if method == "" {
		method = p.Payment.Methods[0]
	}
	for _, m := range s.svc.Methods() {
		v.Methods = append(v.Methods, methodView{ID: m, Title: methodTitle(m), Checked: m == method})
	}
	if len(v.Methods) > 0 && !slices.ContainsFunc(v.Methods, func(m methodView) bool { return m.Checked }) {
		v.Methods[0].Checked = true
	}
	if msg := byField["method"]; msg != "" && formErr == "" {
		v.FormError = msg
	}
	if s.isServices() {
		v.Picked = form[p.ServiceCatalog.Field]
	}
	return v
}

func (s *Server) shop(w http.ResponseWriter, r *http.Request) {
	if s.isShop() {
		s.storeFront(w, r)
		return
	}
	var form map[string]string
	q := r.URL.Query().Get("s")
	if s.isServices() {
		if p, err := s.cfg.Shop.BySlug(r.Context(), q); err == nil && !p.Hidden {
			form = map[string]string{s.svc.P.ServiceCatalog.Field: p.Title} // the card the customer came from
		}
	} else if i, err := strconv.Atoi(q); err == nil && i >= 0 && i < len(s.svc.P.Services) {
		if name, val, ok := strings.Cut(s.svc.P.Services[i].Pick, "="); ok {
			form = map[string]string{name: val} // the tile the customer pressed is already chosen
		}
	}
	v := s.shopView(w, r, form, nil, "", nil)
	if s.isServices() {
		list, err := s.cfg.Shop.List(r.Context(), false)
		if err != nil {
			s.log.Error("services", "err", err)
		}
		v.Cards = cards(list)
	}
	s.render(w, "shop", http.StatusOK, v)
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || !s.csrfOK(r) {
		s.message(w, r, http.StatusForbidden, "Страница устарела", "Обновите страницу и отправьте форму ещё раз.")
		return
	}
	ip := ClientIP(r, s.cfg.Trusted)
	if !s.orders.Allow(ip) {
		s.message(w, r, http.StatusTooManyRequests, "Слишком много заказов", "Попробуйте ещё раз через несколько минут.")
		return
	}
	p := s.svc.P
	fields := map[string]string{}
	for _, f := range p.Checkout.Fields {
		fields[f.ID] = r.PostFormValue("f_" + f.ID)
	}
	qty := map[string]int{}
	if s.isShop() {
		qty = s.readCart(r) // the cart is the signed cookie, not what the form claims
	}
	for key, vals := range r.PostForm {
		if s.isShop() {
			break
		}
		if id, ok := strings.CutPrefix(key, "item_"); ok && len(vals) > 0 {
			if q, err := strconv.Atoi(strings.TrimSpace(vals[0])); err == nil && q != 0 {
				qty[id] = q
			} else if err != nil && strings.TrimSpace(vals[0]) != "" {
				qty[id] = -1 // reported as a quantity mistake
			}
		}
	}
	method := r.PostFormValue("method")
	o, _, err := s.svc.Create(r.Context(), order.CreateInput{
		Items: qty, Fields: fields, Method: method, ClientIP: ip,
		FillSeconds: s.fillSeconds(r.PostFormValue("stamp"), time.Now()), Honeypot: r.PostFormValue("website"),
	})
	var fe order.FieldErrors
	switch {
	case errors.As(err, &fe) && s.isShop():
		s.shopCreateFailed(w, r, fields, method, fe)
	case errors.As(err, &fe):
		s.render(w, "shop", http.StatusUnprocessableEntity, s.shopView(w, r, fields, qty, method, fe))
	case errors.Is(err, order.ErrPaused):
		s.message(w, r, http.StatusServiceUnavailable, "Приём заказов приостановлен", "Попробуйте позже или свяжитесь с нами: "+p.Business.Contact)
	case errors.Is(err, order.ErrRefused):
		// the reasons stay with the operator: telling a fraudster which rule fired helps him get round it
		s.message(w, r, http.StatusForbidden, "Заказ не принят", "Мы не можем принять этот заказ онлайн. Свяжитесь с нами: "+p.Business.Contact)
	case err != nil:
		s.log.Error("create order", "err", err)
		s.message(w, r, http.StatusInternalServerError, "Ошибка", "Заказ не сохранён. Попробуйте ещё раз через минуту.")
	default:
		if s.isShop() {
			s.writeCart(w, r, nil)
		}
		s.redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
	}
}

func (s *Server) message(w http.ResponseWriter, r *http.Request, code int, title, text string) {
	s.render(w, "message", code, struct {
		base
		Heading, Text string
	}{s.base(w, r, title), title, text})
}

// ---------------------------------------------------------------- order page

type orderView struct {
	base
	O           *order.Order
	State       profile.State
	Steps       []Step
	Final       bool
	Ref         string
	Fields      []fieldView
	NeedCode    bool
	CodeError   string
	CodeNote    string
	Requisites  *order.Requisites
	Purpose     string
	Actions     []profile.Transition
	Waiting     string
	MethodTitle string
	P           *profile.Profile
	Call        string
}

func (s *Server) loadOrder(w http.ResponseWriter, r *http.Request) (*order.Order, bool) {
	token := r.PathValue("token")
	if len(token) < 20 || len(token) > 64 {
		s.message(w, r, http.StatusNotFound, "Заказ не найден", "Проверьте ссылку из письма.")
		return nil, false
	}
	o, err := s.svc.Store.ByToken(r.Context(), token)
	if err != nil {
		s.message(w, r, http.StatusNotFound, "Заказ не найден", "Проверьте ссылку из письма.")
		return nil, false
	}
	return o, true
}

func (s *Server) orderView(w http.ResponseWriter, r *http.Request, o *order.Order, codeErr, codeNote string) orderView {
	p := s.svc.P
	st, _ := p.State(o.State)
	v := orderView{base: s.base(w, r, "Заказ №"+strconv.FormatInt(o.ID, 10)), O: o, State: st, Final: st.Final,
		Ref: s.svc.Reference(o), CodeError: codeErr, CodeNote: codeNote, MethodTitle: methodTitle(o.Method), P: p}
	for _, f := range p.Checkout.Fields {
		if val := o.Fields[f.ID]; val != "" {
			v.Fields = append(v.Fields, fieldView{Field: f, Value: f.Display(val)})
		}
	}
	v.NeedCode = p.Payment.RequireEmailCode && o.VerifiedAt == nil && !st.Final
	if s.svc.RequisitesVisible(o) {
		v.Requisites = s.svc.Req()
		v.Purpose = s.svc.Req().PurposeFor(v.Ref, o.Due().Plain())
	}
	v.Actions = s.svc.Flow.Open(o.State, profile.ActorCustomer, o.Flags())
	v.Steps = track(p, o)
	v.Call = s.callLink(o)
	switch {
	case st.Final:
	case !o.Priced:
		v.Waiting = "Цену назовёт оператор после проверки заявки. Мы пришлём письмо."
	case !o.Approved:
		v.Waiting = "Заказ на проверке у оператора. Реквизиты появятся здесь после проверки, обычно в течение рабочего дня."
	case o.Method == profile.MethodTransfer && v.NeedCode:
		v.Waiting = "Введите код из письма, чтобы увидеть реквизиты для оплаты."
	}
	return v
}

func (s *Server) orderPage(w http.ResponseWriter, r *http.Request) {
	o, ok := s.loadOrder(w, r)
	if !ok {
		return
	}
	s.render(w, "order", http.StatusOK, s.orderView(w, r, o, "", ""))
}

func (s *Server) orderState(w http.ResponseWriter, r *http.Request) {
	o, err := s.svc.Store.ByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.rec.Seen(s.visitor(w, r))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"version": o.Version, "state": o.State})
}

func (s *Server) formOrder(w http.ResponseWriter, r *http.Request) (*order.Order, bool) {
	if err := r.ParseForm(); err != nil || !s.csrfOK(r) {
		s.message(w, r, http.StatusForbidden, "Страница устарела", "Обновите страницу и повторите действие.")
		return nil, false
	}
	return s.loadOrder(w, r)
}

func (s *Server) verifyCode(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	if !s.codes.Allow(ClientIP(r, s.cfg.Trusted)) {
		s.render(w, "order", http.StatusTooManyRequests, s.orderView(w, r, o, "Слишком много попыток, подождите минуту.", ""))
		return
	}
	if err := s.svc.VerifyCode(r.Context(), o, r.PostFormValue("code")); err != nil {
		msg := err.Error()
		if !errors.Is(err, order.ErrCodeInvalid) && !errors.Is(err, order.ErrCodeLimit) {
			s.log.Error("verify code", "err", err)
			msg = "Не получилось проверить код, попробуйте ещё раз."
		}
		s.render(w, "order", http.StatusUnprocessableEntity, s.orderView(w, r, o, msg, ""))
		return
	}
	s.redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
}

func (s *Server) resendCode(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	note := "Новый код отправлен на " + o.Email + "."
	if err := s.svc.SendCode(r.Context(), o); err != nil {
		note = err.Error()
	}
	s.render(w, "order", http.StatusOK, s.orderView(w, r, o, "", note))
}

func (s *Server) customerTransition(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	if err := s.svc.Transition(r.Context(), o, r.PathValue("transition"), profile.ActorCustomer, ""); err != nil {
		code := http.StatusConflict
		msg := "Это действие сейчас недоступно."
		if !errors.Is(err, order.ErrForbidden) && !errors.Is(err, order.ErrConflict) {
			code, msg = http.StatusInternalServerError, "Не получилось, попробуйте ещё раз."
			s.log.Error("customer transition", "err", err)
		}
		s.render(w, "order", code, s.orderView(w, r, o, "", msg))
		return
	}
	s.redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
}

// beat keeps the online counter honest for pages left open: the script calls it every 30 seconds.
func (s *Server) beat(w http.ResponseWriter, r *http.Request) {
	s.rec.Seen(s.visitor(w, r))
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- admin

type adminView struct {
	Shop                  bool // the layout's cart link, demo banner and «make the same» button: never on the dashboard
	Cart                  int
	Demo                  bool
	CloneURL              string
	CloneText             string
	URL, Preview, Summary string
	Title                 string
	Paused                bool
	Business              profile.Business
	Version               string
	Orders                []adminOrder
	Stats                 order.Stats
	Currency              string
	Finger                string
	Owner                 bool // opened from the owner's area through the public site, not over SSH
	Nav                   string
	Catalog, Services     bool
}

type adminOrder struct {
	ID     int64
	State  string
	Total  string
	Review bool
	Paid   bool
	At     string
}

func (s *Server) adminData(r *http.Request) (order.Stats, []adminOrder, error) {
	st, err := s.svc.Store.Stats(r.Context(), time.Now().Add(-24*time.Hour), s.svc.Finals())
	if err != nil {
		return st, nil, err
	}
	list, err := s.svc.Store.List(r.Context(), order.Filter{Open: true, Limit: 20}, s.svc.Finals())
	if err != nil {
		return st, nil, err
	}
	var out []adminOrder
	for _, o := range list {
		state, _ := s.svc.P.State(o.State)
		total := "по запросу"
		if o.Priced {
			total = o.Total.String()
		}
		out = append(out, adminOrder{ID: o.ID, State: state.Title, Total: total, Review: o.Review && !o.Approved, Paid: o.Paid,
			At: o.CreatedAt.Format("02.01 15:04")})
	}
	return st, out, nil
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	st, list, err := s.adminData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	finger := "оплата переводом выключена"
	if s.svc.Req() != nil {
		finger = s.svc.Req().Fingerprint()
	}
	s.render(w, "admin", http.StatusOK, adminView{Title: "Дашборд", Paused: s.svc.Paused(), Business: s.svc.P.Business, Version: s.cfg.Version,
		Orders: list, Stats: st, Currency: currencySign(s.svc.P.Business.Currency), Finger: finger, Owner: s.isOwner(r),
		Nav: map[bool]string{true: "load"}[s.isOwner(r)], Catalog: s.hasCatalog(), Services: s.isServices()})
}

func (s *Server) adminLive(w http.ResponseWriter, r *http.Request) {
	st, list, err := s.adminData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"live": s.rec.Live(), "orders": st, "open": list,
		"revenue": st.RevenueDay.String(), "paused": s.svc.Paused()})
}

// cloneText is the end of «Сделать себе …» for a kind of business.
func cloneText(kind string) string {
	switch kind {
	case "shop":
		return "такой же магазин"
	case "master":
		return "такую же страничку мастера"
	case "taxi":
		return "такой же таксопарк"
	case "bakery":
		return "такую же пекарню"
	case "delivery":
		return "такую же доставку"
	case "freight":
		return "такой же сервис перевозок"
	}
	return "такой же сайт"
}

// reportPaid is the customer's «Я оплатил».
func (s *Server) reportPaid(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	if err := s.svc.ReportPaid(r.Context(), o); err != nil {
		if !errors.Is(err, order.ErrForbidden) && !errors.Is(err, order.ErrConflict) {
			s.log.Error("report paid", "err", err)
		}
		s.render(w, "order", http.StatusConflict, s.orderView(w, r, o, "", "Сейчас это не нужно: обновите страницу."))
		return
	}
	s.redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
}
