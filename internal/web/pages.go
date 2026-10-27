package web

import (
	"encoding/json"
	"errors"
	"net/http"
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
}

func (s *Server) base(w http.ResponseWriter, r *http.Request, title string) base {
	v := s.visitor(w, r)
	s.rec.Seen(v)
	return base{Business: s.svc.P.Business, Title: title, CSRF: s.csrfToken(v), Paused: s.svc.Paused()}
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
	for _, m := range p.Payment.Methods {
		if m == profile.MethodTransfer && s.svc.Req == nil {
			continue
		}
		v.Methods = append(v.Methods, methodView{ID: m, Title: methodTitle(m), Checked: m == method})
	}
	if msg := byField["method"]; msg != "" && formErr == "" {
		v.FormError = msg
	}
	return v
}

func (s *Server) shop(w http.ResponseWriter, r *http.Request) {
	s.render(w, "shop", http.StatusOK, s.shopView(w, r, nil, nil, "", nil))
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
	for key, vals := range r.PostForm {
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
		http.Redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
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
		v.Requisites = s.svc.Req
		v.Purpose = s.svc.Req.PurposeFor(v.Ref, o.Total.Plain())
	}
	v.Actions = s.svc.Flow.Open(o.State, profile.ActorCustomer, o.Flags())
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
	http.Redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
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
	http.Redirect(w, r, "/o/"+o.Token, http.StatusSeeOther)
}

// beat keeps the online counter honest for pages left open: the script calls it every 30 seconds.
func (s *Server) beat(w http.ResponseWriter, r *http.Request) {
	s.rec.Seen(s.visitor(w, r))
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- admin

type adminView struct {
	Title    string
	Paused   bool
	Business profile.Business
	Version  string
	Orders   []adminOrder
	Stats    order.Stats
	Currency string
	Finger   string
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
	if s.svc.Req != nil {
		finger = s.svc.Req.Fingerprint()
	}
	s.render(w, "admin", http.StatusOK, adminView{Title: "Дашборд", Paused: s.svc.Paused(), Business: s.svc.P.Business, Version: s.cfg.Version,
		Orders: list, Stats: st, Currency: currencySign(s.svc.P.Business.Currency), Finger: finger})
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
