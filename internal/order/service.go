package order

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/antifraud"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/flow"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// Mailer sends plain-text email to customers (codes and status changes).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Notifier tells the operators. The Telegram bot implements it; nil-safe through NopNotifier.
type Notifier interface {
	OrderCreated(ctx context.Context, o *Order, d antifraud.Decision)
	OrderChanged(ctx context.Context, o *Order, ev Event)
	Refused(ctx context.Context, email string, d antifraud.Decision)
}

// Observer counts what happens for metrics; nil-safe through NopObserver.
type Observer interface {
	Decision(action string)
	Transition(to string)
}

type NopNotifier struct{}

func (NopNotifier) OrderCreated(context.Context, *Order, antifraud.Decision) {}
func (NopNotifier) OrderChanged(context.Context, *Order, Event)              {}
func (NopNotifier) Refused(context.Context, string, antifraud.Decision)      {}

type NopObserver struct{}

func (NopObserver) Decision(string)   {}
func (NopObserver) Transition(string) {}

var (
	ErrPaused      = errors.New("приём заказов временно остановлен")
	ErrRefused     = errors.New("заказ не может быть принят")
	ErrCodeInvalid = errors.New("неверный или устаревший код")
	ErrCodeLimit   = errors.New("слишком много попыток, запросите новый код")
	ErrCodeWait    = errors.New("код уже отправлен, новый можно запросить через минуту")
	ErrForbidden   = errors.New("действие недоступно")
)

const (
	codeTTL      = 15 * time.Minute
	codeAttempts = 5
	codeResend   = time.Minute
	codeSends    = 5
)

// Service is the order core. Build it with New.
type Service struct {
	P       *profile.Profile
	Flow    *flow.Engine
	Store   Store
	Mail    Mailer
	Notify  Notifier
	Observe Observer
	Req     *Requisites
	BaseURL string
	Now     func() time.Time
	Log     *slog.Logger
	secret  []byte
	finals  []string
	paused  atomic.Bool
}

// New wires the service. secret keys the email codes and the client hashes; keep it in the environment.
func New(p *profile.Profile, st Store, mailer Mailer, req *Requisites, secret []byte, baseURL string) *Service {
	s := &Service{
		P: p, Flow: flow.New(p.Workflow), Store: st, Mail: mailer, Notify: NopNotifier{}, Observe: NopObserver{},
		Req: req, BaseURL: strings.TrimRight(baseURL, "/"), Now: func() time.Time { return time.Now().UTC() },
		Log: slog.Default(), secret: secret,
	}
	for _, st := range p.Workflow.States {
		if st.Final {
			s.finals = append(s.finals, st.ID)
		}
	}
	return s
}

func (s *Service) Finals() []string { return s.finals }

func (s *Service) IsFinal(state string) bool { return slices.Contains(s.finals, state) }

func (s *Service) Pause()               { s.paused.Store(true) }
func (s *Service) Resume()              { s.paused.Store(false) }
func (s *Service) Paused() bool         { return s.paused.Load() }
func (s *Service) Link(o *Order) string { return s.BaseURL + "/o/" + o.Token }

// Reference is what the payer writes in the payment purpose: the prefix and the order number.
func (s *Service) Reference(o *Order) string {
	return fmt.Sprintf("%s-%d", s.P.Payment.ReferencePrefix, o.ID)
}

func (s *Service) keyed(parts ...string) string {
	m := hmac.New(sha256.New, s.secret)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

// ClientHash keys the client IP: velocity checks work, the database holds no addresses.
func (s *Service) ClientHash(ip string) string { return s.keyed("client", ip)[:32] }

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateInput is one checkout submit.
type CreateInput struct {
	Items       map[string]int
	Fields      map[string]string
	Method      string
	ClientIP    string
	FillSeconds float64
	Honeypot    string
}

// Create validates, prices and checks the order, stores it and sends the email code.
// A refused attempt returns ErrRefused and creates nothing.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Order, antifraud.Decision, error) {
	if s.Paused() {
		return nil, antifraud.Decision{}, ErrPaused
	}
	fields, errs := ValidateFields(s.P, in.Fields)
	method := in.Method
	if method == "" {
		method = s.P.Payment.Methods[0]
	}
	if !slices.Contains(s.P.Payment.Methods, method) {
		errs = append(errs, &FieldError{Field: "method", Msg: "выберите способ оплаты"})
	}
	if method == profile.MethodTransfer && s.Req == nil {
		errs = append(errs, &FieldError{Field: "method", Msg: "оплата переводом сейчас недоступна"})
	}
	if len(errs) > 0 {
		return nil, antifraud.Decision{}, errs
	}
	lines, fees, total, priced, perrs := Price(s.P, in.Items, fields)
	if len(perrs) > 0 {
		return nil, antifraud.Decision{}, perrs
	}
	now := s.Now()
	email := fields["email"]
	client := s.ClientHash(in.ClientIP)
	counts, err := s.Store.Counts(ctx, email, client, total, s.finals, now)
	if err != nil {
		return nil, antifraud.Decision{}, err
	}
	blocked, err := s.Store.IsBlocked(ctx, email, normPhone(fields["phone"]), client)
	if err != nil {
		return nil, antifraud.Decision{}, err
	}
	d := antifraud.Check(s.P.Antifraud, antifraud.Input{
		Email: email, Total: total, Priced: priced, FillSeconds: in.FillSeconds, HoneypotFilled: in.Honeypot != "",
		Blocked: blocked, ByClientHour: counts.ByClientHour, ByEmailDay: counts.ByEmailDay, OpenUnpaid: counts.OpenUnpaid,
		PaidBefore: counts.PaidByEmail, DuplicateHour: counts.DuplicateHour,
	})
	s.Observe.Decision(string(d.Action))
	if d.Action == antifraud.Block {
		s.Log.Info("order refused", "reasons", d.Reasons)
		s.Notify.Refused(ctx, email, d)
		return nil, d, ErrRefused
	}
	token, err := newToken()
	if err != nil {
		return nil, d, err
	}
	o := &Order{
		Token: token, State: s.Flow.Initial(), Lines: lines, Fees: fees, Fields: fields, Email: email, Method: method,
		Total: total, Priced: priced, Review: d.Action == antifraud.Review, Approved: d.Action == antifraud.Allow,
		Risk: d.Score, RiskReasons: d.Reasons, ClientHash: client, CreatedAt: now, UpdatedAt: now,
	}
	if o.RiskReasons == nil {
		o.RiskReasons = []string{}
	}
	if err := s.Store.Create(ctx, o, Event{At: now, Actor: profile.ActorCustomer, Kind: "created", To: o.State,
		Note: fmt.Sprintf("%s, риск %d", d.Action, d.Score)}); err != nil {
		return nil, d, err
	}
	s.Notify.OrderCreated(ctx, o, d)
	if s.P.Payment.RequireEmailCode {
		if err := s.SendCode(ctx, o); err != nil && !errors.Is(err, ErrCodeWait) {
			s.Log.Warn("email code not sent", "order", o.ID, "err", err)
		}
	}
	return o, d, nil
}

func normPhone(p string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, p)
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// SendCode emails a fresh six-digit code. Resends are throttled and capped per order.
func (s *Service) SendCode(ctx context.Context, o *Order) error {
	now := s.Now()
	if o.VerifiedAt != nil {
		return nil
	}
	if !o.CodeSentAt.IsZero() && now.Sub(o.CodeSentAt) < codeResend {
		return ErrCodeWait
	}
	if o.CodeSends >= codeSends {
		return ErrCodeLimit
	}
	code, err := randomCode()
	if err != nil {
		return err
	}
	o.CodeHash, o.CodeExpires, o.CodeSentAt, o.CodeAttempts = s.keyed("code", fmt.Sprint(o.ID), code), now.Add(codeTTL), now, 0
	o.CodeSends++
	o.UpdatedAt = now
	if err := s.Store.Save(ctx, o, Event{At: now, Actor: profile.ActorSystem, Kind: "code_sent", Note: "код отправлен"}); err != nil {
		return err
	}
	body := fmt.Sprintf("Код подтверждения заказа №%d: %s\n\nКод действует 15 минут. Если вы не оформляли заказ в «%s», "+
		"просто удалите это письмо.\n\nСтраница заказа: %s\n", o.ID, code, s.P.Business.Name, s.Link(o))
	return s.Mail.Send(ctx, o.Email, fmt.Sprintf("Код подтверждения: %s", code), body)
}

// VerifyCode checks the code; five wrong tries burn it.
func (s *Service) VerifyCode(ctx context.Context, o *Order, code string) error {
	now := s.Now()
	if o.VerifiedAt != nil {
		return nil
	}
	if o.CodeHash == "" || now.After(o.CodeExpires) {
		return ErrCodeInvalid
	}
	if o.CodeAttempts >= codeAttempts {
		return ErrCodeLimit
	}
	want := s.keyed("code", fmt.Sprint(o.ID), strings.TrimSpace(code))
	if subtle.ConstantTimeCompare([]byte(want), []byte(o.CodeHash)) != 1 {
		o.CodeAttempts++
		o.UpdatedAt = now
		if err := s.Store.Save(ctx, o, Event{}); err != nil {
			return err
		}
		return ErrCodeInvalid
	}
	o.VerifiedAt, o.CodeHash, o.UpdatedAt = &now, "", now
	return s.save(ctx, o, Event{At: now, Actor: profile.ActorCustomer, Kind: "verified", Note: "email подтверждён"})
}

// RequisitesVisible is the single rule for showing the owner's account: transfer chosen, the price known,
// the anti-fraud satisfied or the operator approved, the email confirmed when the profile asks, not paid yet,
// and the order still open.
func (s *Service) RequisitesVisible(o *Order) bool {
	return s.Req != nil && o.Method == profile.MethodTransfer && o.Priced && o.Approved && !o.Paid &&
		!s.IsFinal(o.State) && (o.VerifiedAt != nil || !s.P.Payment.RequireEmailCode)
}

// Transition moves the order. actorID is the operator's Telegram id, empty for customers and the system.
func (s *Service) Transition(ctx context.Context, o *Order, id, actor, actorID string) error {
	t, err := s.Flow.Apply(id, o.State, actor, o.Flags())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrForbidden, err)
	}
	from := o.State
	now := s.Now()
	o.State, o.UpdatedAt = t.To, now
	ev := Event{At: now, Actor: actor, ActorID: actorID, Kind: "transition", From: from, To: t.To, Note: t.Title}
	if err := s.save(ctx, o, ev); err != nil {
		return err
	}
	s.Observe.Transition(t.To)
	if t.Notify {
		s.mailState(ctx, o)
	}
	return nil
}

func (s *Service) mailState(ctx context.Context, o *Order) {
	st, _ := s.P.State(o.State)
	text := st.Customer
	if text == "" {
		text = st.Title
	}
	body := fmt.Sprintf("Заказ №%d: %s.\n\nСтраница заказа: %s\n\n%s\n", o.ID, text, s.Link(o), s.P.Business.Name)
	if err := s.Mail.Send(ctx, o.Email, fmt.Sprintf("Заказ №%d: %s", o.ID, text), body); err != nil {
		s.Log.Warn("status email not sent", "order", o.ID, "err", err)
	}
}

// save writes and tells the operators about every change except their own button presses, which the bot shows itself.
func (s *Service) save(ctx context.Context, o *Order, ev Event) error {
	if err := s.Store.Save(ctx, o, ev); err != nil {
		return err
	}
	if ev.Actor != profile.ActorOperator {
		s.Notify.OrderChanged(ctx, o, ev)
	}
	return nil
}

// Approve clears an order held by the anti-fraud.
func (s *Service) Approve(ctx context.Context, o *Order, operatorID string) error {
	if o.Approved {
		return nil
	}
	now := s.Now()
	o.Approved, o.UpdatedAt = true, now
	return s.save(ctx, o, Event{At: now, Actor: profile.ActorOperator, ActorID: operatorID, Kind: "approved", Note: "проверка пройдена"})
}

// ConfirmPayment records that the money arrived. Only an operator can do it, and only once.
func (s *Service) ConfirmPayment(ctx context.Context, o *Order, operatorID, note string) error {
	if o.Paid {
		return nil
	}
	if s.IsFinal(o.State) {
		return ErrForbidden
	}
	now := s.Now()
	o.Paid, o.PaidAt, o.PaidNote, o.UpdatedAt = true, &now, note, now
	return s.save(ctx, o, Event{At: now, Actor: profile.ActorOperator, ActorID: operatorID, Kind: "paid", Note: note})
}

// SetPrice names the price of a quote order; naming it is the operator's approval.
func (s *Service) SetPrice(ctx context.Context, o *Order, total money.Amount, operatorID string) error {
	if s.IsFinal(o.State) || o.Paid {
		return ErrForbidden
	}
	if total <= 0 || s.P.Pricing.MinTotal > 0 && total < s.P.Pricing.MinTotal {
		return fmt.Errorf("%w: цена должна быть не меньше %s", ErrForbidden, s.P.Pricing.MinTotal)
	}
	now := s.Now()
	o.Total, o.Priced, o.Approved, o.UpdatedAt = total, true, true, now
	return s.save(ctx, o, Event{At: now, Actor: profile.ActorOperator, ActorID: operatorID, Kind: "priced", Note: total.Plain()})
}

// BlockCustomer puts the order's email and phone on the blocklist.
func (s *Service) BlockCustomer(ctx context.Context, o *Order, operatorID string) error {
	for _, v := range []string{o.Email, normPhone(o.Fields["phone"])} {
		if v != "" {
			if err := s.Store.Block(ctx, v, fmt.Sprintf("заказ №%d", o.ID)); err != nil {
				return err
			}
		}
	}
	now := s.Now()
	o.UpdatedAt = now
	return s.Store.Save(ctx, o, Event{At: now, Actor: profile.ActorOperator, ActorID: operatorID, Kind: "blocked", Note: "клиент в чёрном списке"})
}

// Sweep closes unpaid orders that sat longer than the payment window in a state the workflow can expire.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	finals := map[string]bool{}
	for _, f := range s.finals {
		finals[f] = true
	}
	var states []string
	for _, st := range s.P.Workflow.States {
		if _, ok := s.Flow.Expiring(st.ID, finals); ok {
			states = append(states, st.ID)
		}
	}
	stale, err := s.Store.Stale(ctx, states, s.Now().Add(-time.Duration(s.P.Payment.WindowHours)*time.Hour))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range stale {
		t, _ := s.Flow.Expiring(o.State, finals)
		if err := s.Transition(ctx, o, t.ID, profile.ActorSystem, ""); err != nil {
			if !errors.Is(err, ErrConflict) {
				s.Log.Warn("sweep", "order", o.ID, "err", err)
			}
			continue
		}
		n++
	}
	return n, nil
}
