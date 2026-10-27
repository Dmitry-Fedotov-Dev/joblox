package web

import (
	"crypto/hmac"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// Owner is the owner's area on the public site, for installations started with `joblox up`: there is no
// server to tunnel into, so the owner signs in with the master link and works from the browser.
type Owner struct {
	Key        string                 // the master key; the master link is BaseURL/owner/enter?k=Key
	MarketURL  string                 // the network's market, empty when the network is off
	DataDir    string                 // where the profile, the settings and joblox.env live
	Configured func() bool            // the owner has gone through the setup
	Setup      func(SetupInput) error // writes the profile and restarts the shop; a SetupError goes back to the form
	Shown      func() bool            // the card is on the market
	SetShown   func(bool) error
}

// SetupInput is the "become a performer" form.
type SetupInput struct {
	Kind, Name, Contact, Place, About, Country, Pay string
	Show                                            bool
}

// SetupError is a mistake in the form, shown to the owner as is.
type SetupError string

func (e SetupError) Error() string { return string(e) }

// Kinds offered in the setup, in this order.
var Kinds = []struct{ ID, Title, Hint string }{
	{"taxi", "Водитель такси", "Поездки по заказу: откуда, куда, когда"},
	{"delivery", "Курьер", "Посылки, продукты, документы"},
	{"bakery", "Пекарня у дома", "Выпечка с самовывозом к времени"},
	{"freight", "Перевозчик", "Переезды и грузы, цена по запросу"},
	{"shop", "Продавец", "Каталог с корзиной и доставкой"},
}

// Countries set the currency and the time zone of a new profile.
var Countries = []struct{ ID, Title, Currency, Timezone string }{
	{"kg", "Кыргызстан", "KGS", "Asia/Bishkek"},
	{"kz", "Казахстан", "KZT", "Asia/Almaty"},
	{"ru", "Россия", "RUB", "Europe/Moscow"},
}

const ownerCookie = "jx_o"

func (s *Server) ownerToken() string { return s.sign("owner", s.cfg.Owner.Key) }

func (s *Server) isOwner(r *http.Request) bool {
	if s.cfg.Owner == nil || s.cfg.Owner.Key == "" {
		return false
	}
	c, err := r.Cookie(ownerCookie)
	return err == nil && hmac.Equal([]byte(c.Value), []byte(s.ownerToken()))
}

// ownerOnly lets the owner through and answers everyone else with a plain 404: the area is not advertised.
func (s *Server) ownerOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOwner(r) {
			s.message(w, r, http.StatusNotFound, "Страница не найдена", "Владелец входит по мастер-ссылке из окна программы.")
			return
		}
		h(w, r)
	}
}

func (s *Server) ownerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /owner/enter", s.ownerEnter)
	mux.HandleFunc("GET /owner", s.ownerOnly(s.ownerPage))
	mux.HandleFunc("POST /owner/setup", s.ownerOnly(s.ownerSetup))
	mux.HandleFunc("POST /owner/market", s.ownerOnly(s.ownerMarket))
	mux.HandleFunc("POST /owner/pause", s.ownerOnly(s.ownerPause))
	mux.HandleFunc("POST /owner/o/{id}/{action}", s.ownerOnly(s.ownerAction))
	mux.HandleFunc("GET /admin", s.ownerOnly(s.adminPage))
	mux.HandleFunc("GET /admin/live.json", s.ownerOnly(s.adminLive))
}

func (s *Server) ownerEnter(w http.ResponseWriter, r *http.Request) {
	if !s.codes.Allow(ClientIP(r, s.cfg.Trusted)) {
		s.message(w, r, http.StatusTooManyRequests, "Слишком много попыток", "Подождите минуту.")
		return
	}
	if !hmac.Equal([]byte(r.URL.Query().Get("k")), []byte(s.cfg.Owner.Key)) {
		s.message(w, r, http.StatusNotFound, "Ссылка не подошла", "Откройте мастер-ссылку из окна программы ещё раз.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ownerCookie, Value: s.ownerToken(), Path: "/", MaxAge: 30 * 24 * 3600,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/owner", http.StatusSeeOther) // the key leaves the address bar at once
}

// ---------------------------------------------------------------- views

type ownerView struct {
	base
	Configured bool
	SiteURL    string
	Temporary  bool
	MarketURL  string
	Shown      bool
	DataDir    string
	Orders     []ownerOrder
	Letters    []mail.Message
	NoMail     bool
	Form       SetupInput
	FormError  string
	Kinds      any
	Countries  any
	Currency   string
}

type ownerOrder struct {
	O       *order.Order
	State   string
	Total   string
	Fields  []fieldView
	Method  string
	Review  bool
	Risk    string
	CanPay  bool
	Quote   bool
	Actions []profile.Transition
	Final   bool
}

func (s *Server) ownerView(w http.ResponseWriter, r *http.Request) (ownerView, error) {
	ow := s.cfg.Owner
	site := s.svc.BaseURL()
	v := ownerView{base: s.base(w, r, "Кабинет"), Configured: ow.Configured(), SiteURL: site,
		Temporary: strings.Contains(site, ".trycloudflare.com"), MarketURL: ow.MarketURL, Shown: ow.Shown(), DataDir: ow.DataDir,
		NoMail: s.outbox != nil, Kinds: Kinds, Countries: Countries, Currency: currencySign(s.svc.P.Business.Currency)}
	v.Form = SetupInput{Kind: Kinds[0].ID, Country: Countries[0].ID, Show: true}
	if s.outbox != nil {
		v.Letters = s.outbox.Messages()
		if len(v.Letters) > 10 {
			v.Letters = v.Letters[:10]
		}
	}
	list, err := s.svc.Store.List(r.Context(), order.Filter{Open: true, Limit: 50}, s.svc.Finals())
	if err != nil {
		return v, err
	}
	p := s.svc.P
	for _, o := range list {
		st, _ := p.State(o.State)
		oo := ownerOrder{O: o, State: st.Title, Method: methodTitle(o.Method), Review: o.Review && !o.Approved, Final: st.Final,
			Quote: !o.Priced, CanPay: !o.Paid && o.Priced && !st.Final}
		oo.Total = "цена по запросу"
		if o.Priced {
			oo.Total = o.Total.String() + " " + v.Currency
		}
		if len(o.RiskReasons) > 0 {
			oo.Risk = strings.Join(o.RiskReasons, "; ")
		}
		for _, f := range p.Checkout.Fields {
			if val := o.Fields[f.ID]; val != "" {
				oo.Fields = append(oo.Fields, fieldView{Field: f, Value: f.Display(val)})
			}
		}
		oo.Actions = s.svc.Flow.Open(o.State, profile.ActorOperator, o.Flags())
		v.Orders = append(v.Orders, oo)
	}
	return v, nil
}

func (s *Server) ownerPage(w http.ResponseWriter, r *http.Request) {
	v, err := s.ownerView(w, r)
	if err != nil {
		s.log.Error("owner page", "err", err)
		s.message(w, r, http.StatusInternalServerError, "Ошибка", "Не получилось загрузить заказы, обновите страницу.")
		return
	}
	s.render(w, "owner", http.StatusOK, v)
}

// ---------------------------------------------------------------- actions

func (s *Server) ownerForm(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil || !s.csrfOK(r) {
		s.message(w, r, http.StatusForbidden, "Страница устарела", "Обновите страницу и повторите действие.")
		return false
	}
	return true
}

func field(r *http.Request, name string, max int) (string, bool) {
	v := strings.TrimSpace(r.PostFormValue(name))
	return v, utf8.ValidString(v) && utf8.RuneCountInString(v) <= max
}

func (s *Server) ownerSetup(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	in := SetupInput{Kind: r.PostFormValue("kind"), Country: r.PostFormValue("country"), Show: r.PostFormValue("show") == "1"}
	var problems []string
	var ok bool
	if in.Name, ok = field(r, "name", 60); !ok || utf8.RuneCountInString(in.Name) < 2 {
		problems = append(problems, "название от 2 до 60 букв")
	}
	if in.Contact, ok = field(r, "contact", 40); !ok || in.Contact == "" {
		problems = append(problems, "телефон или другой контакт, до 40 знаков")
	}
	if in.Place, ok = field(r, "place", 40); !ok {
		problems = append(problems, "место короче 40 букв")
	}
	if in.About, ok = field(r, "about", 160); !ok {
		problems = append(problems, "описание короче 160 букв")
	}
	if in.Pay, ok = field(r, "pay", 200); !ok {
		problems = append(problems, "реквизиты короче 200 знаков")
	}
	known := false
	for _, k := range Kinds {
		known = known || k.ID == in.Kind
	}
	if !known {
		problems = append(problems, "выберите роль")
	}
	if r.PostFormValue("terms") != "1" {
		problems = append(problems, "примите пользовательское соглашение")
	}
	if len(problems) == 0 {
		err := s.cfg.Owner.Setup(in)
		var se SetupError
		switch {
		case errors.As(err, &se):
			problems = append(problems, string(se))
		case err != nil:
			s.log.Error("owner setup", "err", err)
			problems = append(problems, "не получилось сохранить, попробуйте ещё раз")
		default:
			s.restarting(w, r)
			return
		}
	}
	v, err := s.ownerView(w, r)
	if err != nil {
		s.log.Error("owner page", "err", err)
	}
	v.Configured, v.Form, v.FormError = false, in, "Проверьте: "+strings.Join(problems, ", ")+"."
	s.render(w, "owner", http.StatusUnprocessableEntity, v)
}

// restarting answers while the shop restarts with the new profile; the page comes back by itself.
func (s *Server) restarting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Refresh", "4; url=/owner")
	s.message(w, r, http.StatusOK, "Готово", "Сайт перезапускается с новыми настройками. Через несколько секунд откроется кабинет.")
}

func (s *Server) ownerMarket(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	if err := s.cfg.Owner.SetShown(r.PostFormValue("show") == "1"); err != nil {
		s.log.Error("owner market", "err", err)
	}
	http.Redirect(w, r, "/owner", http.StatusSeeOther)
}

func (s *Server) ownerPause(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	if s.svc.Paused() {
		s.svc.Resume()
	} else {
		s.svc.Pause()
	}
	http.Redirect(w, r, "/owner", http.StatusSeeOther)
}

func (s *Server) ownerAction(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	o, err := s.svc.Store.Get(r.Context(), id)
	if err != nil {
		s.message(w, r, http.StatusNotFound, "Заказ не найден", "Обновите кабинет.")
		return
	}
	const who = "owner"
	switch action := r.PathValue("action"); action {
	case "approve":
		err = s.svc.Approve(r.Context(), o, who)
	case "paid":
		err = s.svc.ConfirmPayment(r.Context(), o, who, "отмечено в кабинете")
	case "block":
		err = s.svc.BlockCustomer(r.Context(), o, who)
	case "price":
		var total money.Amount
		if total, err = money.Parse(r.PostFormValue("total")); err == nil {
			err = s.svc.SetPrice(r.Context(), o, total, who)
		}
	default:
		t, ok := strings.CutPrefix(action, "t-")
		if !ok {
			http.NotFound(w, r)
			return
		}
		err = s.svc.Transition(r.Context(), o, t, profile.ActorOperator, who)
	}
	if err != nil {
		msg := "Не выполнено: " + err.Error()
		if errors.Is(err, order.ErrConflict) {
			msg = "Заказ уже изменился. Обновите кабинет."
		}
		s.message(w, r, http.StatusConflict, "Заказ №"+strconv.FormatInt(id, 10), msg)
		return
	}
	http.Redirect(w, r, "/owner#o"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ---------------------------------------------------------------- terms

func (s *Server) terms(w http.ResponseWriter, r *http.Request) {
	market := ""
	if s.cfg.Owner != nil {
		market = s.cfg.Owner.MarketURL
	}
	s.render(w, "terms", http.StatusOK, struct {
		base
		MarketURL string
	}{s.base(w, r, "Условия"), market})
}
