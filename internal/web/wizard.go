package web

import (
	"context"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// The setup wizard: a shop made with one button opens on it the first time its owner comes in. Step by step:
// the name and the contact, where customers pay, the first product, and the ready shop with its link. Every
// step can be skipped; the same settings stay in «Настройки» and «Товары».

const (
	wizardPath    = "/owner/welcome"
	settingWizard = "wizard_done"
)

func (s *Server) wizardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+wizardPath, s.ownerOnly(s.wizardPage))
	mux.HandleFunc("POST "+wizardPath+"/name", s.ownerOnly(s.wizardName))
	mux.HandleFunc("POST "+wizardPath+"/done", s.ownerOnly(s.wizardDone))
}

// wizardBack is where a form sent from the wizard returns: only a step of the wizard itself, never elsewhere.
func wizardBack(r *http.Request) string {
	b := r.FormValue("back")
	if !strings.HasPrefix(b, wizardPath+"?step=") || strings.ContainsAny(b, "\\\r\n") || strings.Contains(b, "//") {
		return ""
	}
	return b
}

// wizardDue: the owner has not finished or dismissed the wizard, and the shop still lacks something to sell
// (requisites or products). Shops that already work never see it.
func (s *Server) wizardDue(ctx context.Context) bool {
	if s.cfg.Owner == nil || s.svc.Setting(ctx, settingWizard) == "1" {
		return false
	}
	if s.wizardTransfer() && s.svc.Req() == nil {
		return true
	}
	return s.hasCatalog() && s.productCount(ctx) == 0
}

func (s *Server) wizardTransfer() bool {
	for _, m := range s.svc.P.Payment.Methods {
		if m == profile.MethodTransfer {
			return !s.cfg.ReqFixed
		}
	}
	return false
}

func (s *Server) productCount(ctx context.Context) int {
	if s.cfg.Shop == nil {
		return 0
	}
	list, err := s.cfg.Shop.List(ctx, true)
	if err != nil {
		return 0
	}
	return len(list)
}

type wizardStep struct {
	N     int
	Title string
	On    bool // the current one
	Done  bool
}

type wizardView struct {
	base
	Step, Next           int
	Steps                []wizardStep
	Name, About, Contact string
	Problems             []string
	Req                  *order.Requisites
	ReqRows              []order.RequisiteRow
	ReqError             string
	Cash, CashOff        bool
	Products             int
	Items                string // «товары» or «услуги»
	SiteURL              string
	SiteQR               template.HTML
	NoPay                bool
	HasPay, HasCatalog   bool
	PayStep, ProductStep int
	DoneStep             int
}

// wizardSteps numbers the steps this shop has: the payment step only when it takes transfers, the product
// step only with a catalog.
func (s *Server) wizardSteps() (pay, product, done int) {
	n := 1
	if s.wizardTransfer() {
		n++
		pay = n
	}
	if s.hasCatalog() {
		n++
		product = n
	}
	return pay, product, n + 1
}

func (s *Server) wizardPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pay, product, done := s.wizardSteps()
	step, _ := strconv.Atoi(r.URL.Query().Get("step"))
	if step < 1 || step > done || (step == pay && pay == 0) {
		step = 1
	}
	b := s.svc.P.Business
	v := wizardView{base: s.base(w, r, "Настройка магазина"), Step: step, Next: step + 1, Name: b.Name, About: b.Description,
		Contact: b.Contact, Req: s.svc.Req(), ReqError: r.URL.Query().Get("req"), CashOff: s.svc.CashOff(),
		Products: s.productCount(ctx), SiteURL: s.svc.BaseURL(), NoPay: len(s.svc.Methods()) == 0,
		HasPay: pay > 0, HasCatalog: product > 0, PayStep: pay, ProductStep: product, DoneStep: done, Items: "товары"}
	if s.isServices() {
		v.Items = "услуги"
	}
	for _, m := range s.svc.P.Payment.Methods {
		if m == profile.MethodCash {
			v.Cash = true
		}
	}
	titles := map[int]string{1: "Название", pay: "Оплата", product: map[bool]string{true: "Услуги", false: "Товары"}[s.isServices()], done: "Готово"}
	for i := 1; i <= done; i++ {
		if i == 0 || titles[i] == "" {
			continue
		}
		v.Steps = append(v.Steps, wizardStep{N: i, Title: titles[i], On: i == step, Done: i < step})
	}
	if v.Req != nil {
		v.ReqRows = append(v.ReqRows, v.Req.Lines...)
	}
	for len(v.ReqRows) < 2 {
		v.ReqRows = append(v.ReqRows, order.RequisiteRow{})
	}
	if strings.HasPrefix(v.SiteURL, "https://") {
		v.SiteQR = qrSVG(v.SiteURL)
	}
	s.render(w, "wizard", http.StatusOK, v)
}

func (s *Server) wizardName(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	name, ok1 := field(r, "name", 60)
	about, ok2 := field(r, "about", 160)
	contact, ok3 := field(r, "contact", 60)
	var problems []string
	if !ok1 || utf8.RuneCountInString(name) < 2 {
		problems = append(problems, "название от 2 до 60 букв")
	}
	if !ok2 {
		problems = append(problems, "описание до 160 букв")
	}
	if !ok3 {
		problems = append(problems, "контакт до 60 знаков")
	}
	if len(problems) > 0 {
		pay, product, done := s.wizardSteps()
		v := wizardView{base: s.base(w, r, "Настройка магазина"), Step: 1, Next: 2, Name: name, About: about, Contact: contact,
			Problems: problems, HasPay: pay > 0, HasCatalog: product > 0, PayStep: pay, ProductStep: product, DoneStep: done}
		v.Steps = []wizardStep{{N: 1, Title: "Название", On: true}}
		s.render(w, "wizard", http.StatusUnprocessableEntity, v)
		return
	}
	if err := s.svc.SetBusiness(r.Context(), name, about, contact); err != nil {
		s.log.Error("wizard name", "err", err)
		s.message(w, r, http.StatusInternalServerError, "Не сохранилось", "Попробуйте ещё раз.")
		return
	}
	s.redirect(w, r, wizardPath+"?step=2", http.StatusSeeOther)
}

// wizardDone closes the wizard for good: the owner lands in the back office from now on.
func (s *Server) wizardDone(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	if err := s.svc.SetSetting(r.Context(), settingWizard, "1"); err != nil {
		s.log.Error("wizard done", "err", err)
	}
	to := "/admin"
	if r.PostFormValue("to") == "site" {
		to = "/"
	}
	s.redirect(w, r, to, http.StatusSeeOther)
}
