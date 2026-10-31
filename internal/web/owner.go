package web

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/hub"
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
	// Portal returns the market's cards to show in the owner's area: who is already working, the network's
	// own services. Nil or an empty answer hides the block.
	Portal func(context.Context) []hub.Card
}

// BotPanel is the operator bot the owner connects in the cabinet with a token from @BotFather.
type BotPanel interface {
	BotStatus(ctx context.Context) (name string, operators int, link string)
	Connect(ctx context.Context, token string) error
	Disconnect(ctx context.Context) error
	// the shared bot of the hosting tariff
	BotShared(ctx context.Context) (available, on bool)
	ConnectShared(ctx context.Context) error
	DisconnectShared(ctx context.Context) error
	VerifyNonce(nonce string) bool
	RelayUpdate(ctx context.Context, body []byte, sign string) error
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
	{"shop", "Продавец", "Товары с фото, размеры и остатки, корзина"},
	{"master", "Мастер на выезд", "Ремонт, сборка, ручная работа: цену называете вы"},
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
	mux.HandleFunc("GET /owner/status.json", s.ownerOnly(s.ownerStatus))
	mux.HandleFunc("POST /owner/setup", s.ownerOnly(s.ownerSetup))
	mux.HandleFunc("POST /owner/market", s.ownerOnly(s.ownerMarket))
	mux.HandleFunc("POST /owner/pause", s.ownerOnly(s.ownerPause))
	mux.HandleFunc("POST /owner/prepay", s.ownerOnly(s.ownerPrepay))
	mux.HandleFunc("POST /owner/logout", s.ownerOnly(s.ownerLogout))
	mux.HandleFunc("POST /owner/bot", s.ownerOnly(s.ownerBot))
	mux.HandleFunc("POST /owner/bot/shared", s.ownerOnly(s.ownerBotShared))
	// the hub of the shared bot: its call-back while registering, and what the seller pressed
	mux.HandleFunc("GET /bot/relay/verify", s.relayVerify)
	mux.HandleFunc("POST /bot/relay/update", s.relayUpdate)
	mux.HandleFunc("POST /owner/requisites", s.ownerOnly(s.ownerRequisites))
	mux.HandleFunc("GET /pay-qr", s.payQR)
	mux.HandleFunc("POST /owner/o/{id}/{action}", s.ownerOnly(s.ownerAction))
	s.backofficeRoutes(mux)
	mux.HandleFunc("GET /admin/live.json", s.ownerOnly(s.adminLive))
	if s.hasCatalog() {
		s.catalogRoutes(mux)
	}
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
	http.SetCookie(w, &http.Cookie{Name: ownerCookie, Value: s.ownerToken(), Path: s.cookiePath(), MaxAge: 30 * 24 * 3600,
		HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	to := "/admin" // the key leaves the address bar at once; a new installation starts with the setup
	if o := s.cfg.Owner; o != nil && o.Configured != nil && !o.Configured() {
		to = "/owner"
	}
	s.redirect(w, r, to, http.StatusSeeOther)
}

// ---------------------------------------------------------------- views

type ownerView struct {
	base
	Configured  bool
	SiteURL     string
	Temporary   bool
	MarketURL   string
	Shown       bool
	DataDir     string
	Orders      []ownerOrder
	Letters     []mail.Message
	NoMail      bool
	BotPanel    bool   // the bot can be connected here
	BotName     string // @username of the running bot
	BotOps      int
	BotLink     string
	BotError    string
	HasQR       bool              // the bank's payment QR is uploaded
	BotCanShare bool              // the shared bot of the hosting is there for this shop
	BotShared   bool              // and it is the one in use
	Req         *order.Requisites // the requisites in use
	ReqFixed    bool              // they come from the server's file
	ReqRows     []order.RequisiteRow
	ReqError    string
	CashOff     bool
	Transfer    bool // the profile takes transfers at all
	Cash        bool
	Form        SetupInput
	FormError   string
	Kinds       any
	Countries   any
	Currency    string
	Portal      []hub.Card
	SiteQR      template.HTML
	Waiting     bool          // the public address is not there yet
	PhoneQR     template.HTML // the master link: the owner scans it to open the cabinet on a phone
	Prepay      int           // the prepayment of new orders, percent
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
	CanAdv  bool // a split order whose advance has not been marked yet
	Quote   bool
	Actions []profile.Transition
	Final   bool
	Call    string
}

func (s *Server) ownerView(w http.ResponseWriter, r *http.Request) (ownerView, error) {
	ow := s.cfg.Owner
	site := s.svc.BaseURL()
	v := ownerView{base: s.base(w, r, "Кабинет"), Configured: ow.Configured(), SiteURL: site,
		Temporary: strings.Contains(site, ".trycloudflare.com"), MarketURL: ow.MarketURL, Shown: ow.Shown(), DataDir: ow.DataDir,
		NoMail: s.outbox != nil, Kinds: Kinds, Countries: Countries, Currency: currencySign(s.svc.P.Business.Currency)}
	v.Form = SetupInput{Kind: Kinds[0].ID, Country: Countries[0].ID, Show: true}
	if s.cfg.Bot != nil {
		v.BotPanel = true
		v.BotName, v.BotOps, v.BotLink = s.cfg.Bot.BotStatus(r.Context())
		v.BotCanShare, v.BotShared = s.cfg.Bot.BotShared(r.Context())
		v.BotError = r.URL.Query().Get("bot")
	}
	v.Req, v.ReqFixed, v.CashOff = s.svc.Req(), s.cfg.ReqFixed, s.svc.CashOff()
	v.Transfer = slices.Contains(s.svc.P.Payment.Methods, profile.MethodTransfer)
	v.Cash = slices.Contains(s.svc.P.Payment.Methods, profile.MethodCash)
	v.ReqError = r.URL.Query().Get("req")
	v.HasQR = s.svc.PaymentQR(r.Context()) != nil
	if v.Req != nil {
		v.ReqRows = append(v.ReqRows, v.Req.Lines...)
	}
	for len(v.ReqRows) < 3 {
		v.ReqRows = append(v.ReqRows, order.RequisiteRow{})
	}
	v.Nav = "settings"
	v.Prepay = s.svc.PrepayPercent(r.Context())
	if ow.Portal != nil {
		v.Portal = ow.Portal(r.Context())
	}
	if strings.HasPrefix(site, "https://") {
		v.SiteQR = qrSVG(site)
		v.PhoneQR = qrSVG(site + "/owner/enter?k=" + ow.Key)
	} else {
		v.Waiting = true
	}
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
		v.Orders = append(v.Orders, s.ownerCard(o))
	}
	_ = p
	return v, nil
}

// ownerCard is an order as the owner works with it: its state, sums, the customer's answers and the buttons.
func (s *Server) ownerCard(o *order.Order) ownerOrder {
	p := s.svc.P
	st, _ := p.State(o.State)
	oo := ownerOrder{O: o, State: st.Title, Method: methodTitle(o.Method), Review: o.Review && !o.Approved, Final: st.Final,
		Quote: !o.Priced, CanPay: !o.Paid && o.Priced && !st.Final,
		CanAdv: o.Split() && o.AdvanceAt == nil && !o.Paid && o.Priced && !st.Final}
	oo.Total = "цена по запросу"
	if o.Priced {
		oo.Total = o.Total.String() + " " + currencySign(p.Business.Currency)
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
	oo.Call = s.callLink(o)
	return oo
}

func (s *Server) ownerPage(w http.ResponseWriter, r *http.Request) {
	v, err := s.ownerView(w, r)
	if err != nil {
		s.log.Error("owner page", "err", err)
		s.message(w, r, http.StatusInternalServerError, "Ошибка", "Не получилось загрузить заказы, обновите страницу.")
		return
	}
	page := "owner"
	if !v.Configured {
		page = "ownersetup"
	}
	s.render(w, page, http.StatusOK, v)
}

// ownerLogout forgets the owner on this browser; the master link signs in again.
func (s *Server) ownerLogout(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ownerCookie, Value: "", Path: s.cookiePath(), MaxAge: -1, HttpOnly: true,
		Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	s.redirect(w, r, "/", http.StatusSeeOther)
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

// ownerStatus lets the open cabinet notice the public address arriving.
func (s *Server) ownerStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"site": s.svc.BaseURL()})
}

// restarting answers while the shop restarts with the new profile; the page comes back by itself.
func (s *Server) restarting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Refresh", "4; url="+s.cfg.Prefix+"/owner")
	s.message(w, r, http.StatusOK, "Готово", "Сайт перезапускается с новыми настройками. Через несколько секунд откроется кабинет.")
}

func (s *Server) ownerMarket(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	if err := s.cfg.Owner.SetShown(r.PostFormValue("show") == "1"); err != nil {
		s.log.Error("owner market", "err", err)
	}
	s.redirect(w, r, "/owner", http.StatusSeeOther)
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
	s.redirect(w, r, "/owner", http.StatusSeeOther)
}

// ownerRequisites saves the payment requisites typed in the cabinet, or removes them.
func (s *Server) ownerRequisites(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	if r.PostFormValue("qr_off") == "1" {
		_ = s.svc.SetPaymentQR(r.Context(), nil)
		s.redirect(w, r, "/owner#pay", http.StatusSeeOther)
		return
	}
	if f, _, err := r.FormFile("qr"); err == nil { // the bank's QR picture comes with the form
		img, rerr := io.ReadAll(io.LimitReader(f, 1<<20+1))
		_ = f.Close()
		ct := http.DetectContentType(img)
		picture := ct == "image/png" || ct == "image/jpeg" || ct == "image/webp"
		if rerr != nil || !picture {
			s.redirect(w, r, "/owner?req="+url.QueryEscape("QR должен быть картинкой PNG, JPG или WebP")+"#pay", http.StatusSeeOther)
			return
		}
		if err := s.svc.SetPaymentQR(r.Context(), img); err != nil {
			s.redirect(w, r, "/owner?req="+url.QueryEscape(err.Error())+"#pay", http.StatusSeeOther)
			return
		}
		if r.PostFormValue("recipient") == "" { // only the picture was sent
			s.redirect(w, r, "/owner#pay", http.StatusSeeOther)
			return
		}
	}
	if s.cfg.ReqFixed {
		http.Error(w, "the requisites come from the server", http.StatusConflict)
		return
	}
	var req *order.Requisites
	if r.PostFormValue("off") != "1" {
		req = &order.Requisites{Recipient: strings.TrimSpace(r.PostFormValue("recipient")), Purpose: "Заказ {ref}",
			Note: strings.TrimSpace(r.PostFormValue("note"))}
		for i := 1; i <= 3; i++ {
			n := strconv.Itoa(i)
			label, value := strings.TrimSpace(r.PostFormValue("label"+n)), strings.TrimSpace(r.PostFormValue("value"+n))
			if value != "" {
				if label == "" {
					label = "Перевод"
				}
				req.Lines = append(req.Lines, order.RequisiteRow{Label: clip(label, 60), Value: clip(value, 120)})
			}
		}
		req.Recipient, req.Note = clip(req.Recipient, 120), clip(req.Note, 300)
	}
	if err := s.svc.SaveRequisites(r.Context(), req, r.PostFormValue("cash") != "1"); err != nil {
		s.redirect(w, r, "/owner?req="+url.QueryEscape(err.Error())+"#pay", http.StatusSeeOther)
		return
	}
	s.redirect(w, r, "/owner#pay", http.StatusSeeOther)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// payQR serves the bank's payment QR the owner uploaded: it is on every order page with the requisites.
func (s *Server) payQR(w http.ResponseWriter, r *http.Request) {
	img := s.svc.PaymentQR(r.Context())
	if img == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(img))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(img)
}

// ownerBotShared switches the shop to the hosting's shared bot, or off it.
func (s *Server) ownerBotShared(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) || s.cfg.Bot == nil {
		return
	}
	var err error
	if r.PostFormValue("off") == "1" {
		err = s.cfg.Bot.DisconnectShared(r.Context())
	} else {
		err = s.cfg.Bot.ConnectShared(r.Context())
	}
	if err != nil {
		s.redirect(w, r, "/owner?bot="+url.QueryEscape(err.Error())+"#bot", http.StatusSeeOther)
		return
	}
	s.redirect(w, r, "/owner#bot", http.StatusSeeOther)
}

func (s *Server) relayVerify(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Bot == nil || !s.cfg.Bot.VerifyNonce(r.URL.Query().Get("nonce")) {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) relayUpdate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Bot == nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		http.Error(w, "too big", http.StatusRequestEntityTooLarge)
		return
	}
	if err := s.cfg.Bot.RelayUpdate(r.Context(), body, r.Header.Get("X-Relay-Sign")); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownerBot connects the operator bot with a token, or turns it off.
func (s *Server) ownerBot(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) || s.cfg.Bot == nil {
		return
	}
	var err error
	if r.PostFormValue("off") == "1" {
		err = s.cfg.Bot.Disconnect(r.Context())
	} else {
		err = s.cfg.Bot.Connect(r.Context(), r.PostFormValue("token"))
	}
	if err != nil {
		s.redirect(w, r, "/owner?bot="+url.QueryEscape(err.Error())+"#bot", http.StatusSeeOther)
		return
	}
	s.redirect(w, r, "/owner#bot", http.StatusSeeOther)
}

// ownerPrepay switches the prepayment of new orders: 0, 50 or 100 percent.
func (s *Server) ownerPrepay(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	pct, err := strconv.Atoi(r.PostFormValue("pct"))
	if err != nil || (pct != 0 && pct != 50 && pct != 100) {
		http.Error(w, "bad prepayment", http.StatusBadRequest)
		return
	}
	if err := s.svc.SetPrepayPercent(r.Context(), pct); err != nil {
		s.message(w, r, http.StatusInternalServerError, "Не сохранилось", "Попробуйте ещё раз.")
		return
	}
	s.redirect(w, r, "/owner", http.StatusSeeOther)
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
	case "advance":
		err = s.svc.ConfirmAdvance(r.Context(), o, who, "отмечено в кабинете")
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
	if next := r.PostFormValue("next"); strings.HasPrefix(next, "/admin/orders/") { // the order's own page
		s.redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.redirect(w, r, "/owner#o"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// callLink is the voice room of an open order: the customer and the performer open the same link and
// talk one to one, without phone numbers. The room comes from the order's secret token, so nobody else
// can guess it, and the button is gone once the order is closed.
func (s *Server) callLink(o *order.Order) string {
	if s.cfg.CallURL == "" || s.svc.IsFinal(o.State) {
		return ""
	}
	return strings.TrimRight(s.cfg.CallURL, "/") + "/?call=" + s.sign("call", o.Token)[:24]
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
