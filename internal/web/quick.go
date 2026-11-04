package web

import (
	"errors"
	"net/http"
	"strconv"
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

// chat fills the order's chat: shown while the order is open and a bot carries it; a quick order shows it after
// «Я оплатил», with the rest of what comes after.
func (s *Server) chat(r *http.Request, o *order.Order, v *orderView) {
	name := ""
	if s.cfg.CustomerBot != nil {
		name = s.cfg.CustomerBot()
	}
	if name == "" || s.svc.IsFinal(o.State) || (s.svc.P.Checkout.Quick() && o.ReportedAt == nil && !o.Paid) {
		return
	}
	v.Chat, v.BotLinked, v.ChatOpen = true, o.Fields["telegram_id"] != "", s.svc.ChatOpen(r.Context(), o)
	v.BotLink = "https://t.me/" + name + "?start=o" + o.Token
	msgs, err := s.svc.Messages(r.Context(), o)
	if err != nil {
		s.log.Error("order messages", "err", err)
	}
	v.Messages = msgs
}

func (s *Server) orderMessage(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	if !s.codes.Allow(ClientIP(r, s.cfg.Trusted)) {
		s.message(w, r, http.StatusTooManyRequests, "Слишком много сообщений", "Подождите минуту и напишите ещё раз.")
		return
	}
	if err := s.svc.CustomerSays(r.Context(), o, r.PostFormValue("text")); err != nil && !errors.Is(err, order.ErrForbidden) &&
		!errors.Is(err, order.ErrChatClosed) {
		s.log.Error("order message", "err", err)
	}
	s.redirect(w, r, "/o/"+o.Token+"#chat", http.StatusSeeOther)
}

func (s *Server) orderBotOnly(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	if err := s.svc.SetBotOnly(r.Context(), o, r.PostFormValue("on") == "1"); err != nil {
		s.log.Error("order bot only", "err", err)
	}
	s.redirect(w, r, "/o/"+o.Token+"#talk", http.StatusSeeOther)
}

// setupFields is the card «Осталось настроить» of a paid order (checkout.setup): its fields, typed or saved;
// done when the needed first one is there.
func (s *Server) setupFields(o *order.Order, typed map[string]string, errs order.FieldErrors) ([]fieldView, bool) {
	p := s.svc.P
	if len(p.Checkout.Setup) == 0 || (!o.Paid && o.AdvanceAt == nil) || s.svc.IsFinal(o.State) {
		return nil, false
	}
	var out []fieldView
	for i, id := range p.Checkout.Setup {
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
	return out, o.Fields[p.Checkout.Setup[0]] != ""
}

func (s *Server) orderSetup(w http.ResponseWriter, r *http.Request) {
	o, ok := s.formOrder(w, r)
	if !ok {
		return
	}
	typed := map[string]string{}
	for _, id := range s.svc.P.Checkout.Setup {
		typed[id] = r.PostFormValue("f_" + id)
	}
	err := s.svc.SetSetup(r.Context(), o, typed)
	var fe order.FieldErrors
	switch {
	case errors.As(err, &fe):
		v := s.orderView(w, r, o, "", "")
		v.Setup, _ = s.setupFields(o, typed, fe)
		v.SetupDone = false
		s.render(w, "order", http.StatusUnprocessableEntity, v)
		return
	case err != nil && !errors.Is(err, order.ErrForbidden):
		s.log.Error("order setup", "err", err)
	}
	s.redirect(w, r, "/o/"+o.Token+"#setup", http.StatusSeeOther)
}
