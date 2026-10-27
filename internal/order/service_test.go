package order_test

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/antifraud"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/store"
)

type outbox struct {
	mu   sync.Mutex
	sent []string
}

func (b *outbox) Send(_ context.Context, to, subject, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, to+"\n"+subject+"\n"+body)
	return nil
}

var codeRe = regexp.MustCompile(`Код подтверждения: (\d{6})`)

func (b *outbox) lastCode(t *testing.T) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.sent) - 1; i >= 0; i-- {
		if m := codeRe.FindStringSubmatch(b.sent[i]); m != nil {
			return m[1]
		}
	}
	t.Fatal("no code in the outbox")
	return ""
}

func preset(t *testing.T, name string) *profile.Profile {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := profile.Load(filepath.Join(filepath.Dir(file), "..", "..", "profiles", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type env struct {
	svc   *order.Service
	st    *store.SQLite
	mail  *outbox
	clock time.Time
}

func newEnv(t *testing.T, name string) *env {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	req := &order.Requisites{Recipient: "ИП Тест", Lines: []order.RequisiteRow{{Label: "Счёт", Value: "40802810000000000001"}},
		Purpose: "Оплата заказа {ref}"}
	e := &env{st: st, mail: &outbox{}, clock: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)}
	e.svc = order.New(preset(t, name), st, e.mail, req, []byte("test-secret"), "https://shop.example")
	e.svc.Now = func() time.Time { return e.clock }
	return e
}

func bakeryInput() order.CreateInput {
	return order.CreateInput{
		Items:  map[string]int{"baton": 2, "croissant": 1},
		Fields: map[string]string{"name": "Анна", "phone": "+7 900 123-45-67", "email": "Anna@Example.ru", "pickup_at": "2026-10-27T14:00"},
		Method: profile.MethodTransfer, ClientIP: "203.0.113.5", FillSeconds: 30,
	}
}

func TestBakeryLifecycle(t *testing.T) {
	e := newEnv(t, "bakery")
	ctx := context.Background()
	o, d, err := e.svc.Create(ctx, bakeryInput())
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != antifraud.Allow || !o.Approved || o.Email != "anna@example.ru" || o.Total != money.Amount(2*8900+12000) {
		t.Fatalf("created: %+v %+v", o, d)
	}
	if e.svc.RequisitesVisible(o) {
		t.Fatal("requisites before the email code")
	}
	if err := e.svc.VerifyCode(ctx, o, "000000"); !errors.Is(err, order.ErrCodeInvalid) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := e.svc.VerifyCode(ctx, o, e.mail.lastCode(t)); err != nil {
		t.Fatal(err)
	}
	if !e.svc.RequisitesVisible(o) {
		t.Fatal("requisites hidden after verification")
	}
	if err := e.svc.Transition(ctx, o, "accept", profile.ActorCustomer, ""); !errors.Is(err, order.ErrForbidden) {
		t.Fatalf("customer accepted: %v", err)
	}
	for _, id := range []string{"accept", "ready"} {
		if err := e.svc.Transition(ctx, o, id, profile.ActorOperator, "42"); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if err := e.svc.Transition(ctx, o, "hand_over", profile.ActorOperator, "42"); !errors.Is(err, order.ErrForbidden) {
		t.Fatalf("handed over unpaid: %v", err)
	}
	if err := e.svc.ConfirmPayment(ctx, o, "42", "п/п 17"); err != nil {
		t.Fatal(err)
	}
	if e.svc.RequisitesVisible(o) {
		t.Fatal("requisites shown after payment")
	}
	if err := e.svc.Transition(ctx, o, "hand_over", profile.ActorOperator, "42"); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.Get(ctx, o.ID)
	if err != nil || got.State != "done" || !got.Paid || got.PaidNote != "п/п 17" {
		t.Fatalf("stored: %+v %v", got, err)
	}
	evs, _ := e.st.Events(ctx, o.ID)
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	if strings.Join(kinds, ",") != "created,code_sent,verified,transition,transition,paid,transition" {
		t.Fatalf("journal: %v", kinds)
	}
}

func TestQuoteOrderWaitsForPrice(t *testing.T) {
	e := newEnv(t, "freight")
	ctx := context.Background()
	o, d, err := e.svc.Create(ctx, order.CreateInput{
		Fields: map[string]string{"name": "Пётр", "phone": "+79001234567", "email": "petr@example.ru",
			"from_address": "Склад 1", "to_address": "Офис 2", "cargo": "Мебель", "weight_kg": "300",
			"loaders": "2", "date": "2026-10-28T09:00"},
		Method: profile.MethodTransfer, ClientIP: "203.0.113.6", FillSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != antifraud.Review || o.Approved || o.Priced {
		t.Fatalf("quote order: %+v %+v", o, d)
	}
	_ = e.svc.VerifyCode(ctx, o, e.mail.lastCode(t))
	if e.svc.RequisitesVisible(o) {
		t.Fatal("requisites before the price")
	}
	if err := e.svc.SetPrice(ctx, o, 500, "7"); !errors.Is(err, order.ErrForbidden) {
		t.Fatalf("price below the minimum: %v", err)
	}
	if err := e.svc.SetPrice(ctx, o, 450000, "7"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Transition(ctx, o, "quote", profile.ActorOperator, "7"); err != nil {
		t.Fatal(err)
	}
	if !e.svc.RequisitesVisible(o) {
		t.Fatal("requisites hidden after the price")
	}
	if err := e.svc.Transition(ctx, o, "book", profile.ActorCustomer, ""); err != nil {
		t.Fatal(err)
	}
}

func TestVelocityRefusesWithoutCreating(t *testing.T) {
	e := newEnv(t, "bakery")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		in := bakeryInput()
		in.Fields["email"] = "user" + string(rune('a'+i)) + "@example.ru"
		if _, _, err := e.svc.Create(ctx, in); err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
	}
	in := bakeryInput()
	in.Fields["email"] = "late@example.ru"
	if _, d, err := e.svc.Create(ctx, in); !errors.Is(err, order.ErrRefused) || d.Action != antifraud.Block {
		t.Fatalf("sixth order from the same client: %v %+v", err, d)
	}
	list, _ := e.st.List(ctx, order.Filter{}, e.svc.Finals())
	if len(list) != 5 {
		t.Fatalf("orders stored: %d", len(list))
	}
}

func TestBlocklist(t *testing.T) {
	e := newEnv(t, "bakery")
	ctx := context.Background()
	o, _, err := e.svc.Create(ctx, bakeryInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.BlockCustomer(ctx, o, "1"); err != nil {
		t.Fatal(err)
	}
	in := bakeryInput()
	in.ClientIP = "198.51.100.1"
	in.Fields["email"] = "other@example.ru" // same phone
	if _, _, err := e.svc.Create(ctx, in); !errors.Is(err, order.ErrRefused) {
		t.Fatalf("blocked phone ordered again: %v", err)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t, "bakery")
	in := bakeryInput()
	in.Fields["email"] = "not-an-email"
	in.Fields["pickup_at"] = "завтра"
	in.Items = map[string]int{"baton": 50}
	_, _, err := e.svc.Create(context.Background(), in)
	var fe order.FieldErrors
	if !errors.As(err, &fe) || len(fe) != 2 {
		t.Fatalf("field errors: %v", err)
	}
	in = bakeryInput()
	in.Items = map[string]int{"baton": 50}
	if _, _, err := e.svc.Create(context.Background(), in); !errors.As(err, &fe) || fe[0].Field != "item_baton" {
		t.Fatalf("quantity error: %v", err)
	}
}

func TestPricingFees(t *testing.T) {
	shop := preset(t, "shop")
	_, fees, total, _, errs := order.Price(shop, map[string]int{"mug": 1}, map[string]string{"delivery": "Курьером"})
	if len(errs) > 0 || len(fees) != 1 || total != 90000+35000 {
		t.Fatalf("courier fee: %v %v %v", fees, total, errs)
	}
	_, fees, total, _, _ = order.Price(shop, map[string]int{"board": 3}, map[string]string{"delivery": "Курьером"})
	if len(fees) != 0 || total != 720000 {
		t.Fatalf("free delivery from 5000: %v %v", fees, total)
	}
	taxi := preset(t, "taxi")
	_, fees, total, _, _ = order.Price(taxi, map[string]int{"economy": 1}, map[string]string{"distance_km": "12.5"})
	if len(fees) != 1 || total != 15000+35000 {
		t.Fatalf("per km: %v %v", fees, total)
	}
}

func TestOptimisticLock(t *testing.T) {
	e := newEnv(t, "bakery")
	ctx := context.Background()
	o, _, err := e.svc.Create(ctx, bakeryInput())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.Get(ctx, o.ID)
	b, _ := e.st.Get(ctx, o.ID)
	if err := e.svc.Transition(ctx, a, "accept", profile.ActorOperator, "1"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Transition(ctx, b, "cancel", profile.ActorOperator, "2"); !errors.Is(err, order.ErrConflict) {
		t.Fatalf("second operator overwrote the first: %v", err)
	}
}

func TestSweepExpiresStaleOrders(t *testing.T) {
	e := newEnv(t, "bakery")
	ctx := context.Background()
	o, _, err := e.svc.Create(ctx, bakeryInput())
	if err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(5 * time.Hour) // window is 4 hours
	if n, err := e.svc.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	got, _ := e.st.Get(ctx, o.ID)
	if got.State != "expired" {
		t.Fatalf("state %s", got.State)
	}
}

func TestCodeLimits(t *testing.T) {
	e := newEnv(t, "bakery")
	ctx := context.Background()
	o, _, err := e.svc.Create(ctx, bakeryInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SendCode(ctx, o); !errors.Is(err, order.ErrCodeWait) {
		t.Fatalf("resend at once: %v", err)
	}
	for i := 0; i < 5; i++ {
		_ = e.svc.VerifyCode(ctx, o, "111111")
	}
	if err := e.svc.VerifyCode(ctx, o, e.mail.lastCode(t)); !errors.Is(err, order.ErrCodeLimit) {
		t.Fatalf("code after five failures: %v", err)
	}
	e.clock = e.clock.Add(2 * time.Minute)
	if err := e.svc.SendCode(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.VerifyCode(ctx, o, e.mail.lastCode(t)); err != nil {
		t.Fatalf("fresh code: %v", err)
	}
}

func TestPause(t *testing.T) {
	e := newEnv(t, "bakery")
	e.svc.Pause()
	if _, _, err := e.svc.Create(context.Background(), bakeryInput()); !errors.Is(err, order.ErrPaused) {
		t.Fatalf("paused: %v", err)
	}
}

func TestRequisitesFingerprint(t *testing.T) {
	a := &order.Requisites{Recipient: "ИП", Lines: []order.RequisiteRow{{Label: "Счёт", Value: "1"}}, Purpose: "{ref}"}
	b := &order.Requisites{Recipient: "ИП", Lines: []order.RequisiteRow{{Label: "Счёт", Value: "2"}}, Purpose: "{ref}"}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("changed account, same fingerprint")
	}
	if got := a.PurposeFor("KL-7", "100.00"); got != "KL-7" {
		t.Fatalf("purpose %q", got)
	}
}
