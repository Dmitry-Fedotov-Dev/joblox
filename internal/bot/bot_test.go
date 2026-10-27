package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/mail"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/order"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/store"
)

// fakeTelegram records every Bot API call.
type fakeTelegram struct {
	mu    sync.Mutex
	calls []call
}

type call struct {
	Method string
	Body   map[string]any
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, call{Method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], Body: body})
	f.mu.Unlock()
	_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
}

func (f *fakeTelegram) texts(method string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.Method == method {
			if t, ok := c.Body["text"].(string); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

func (f *fakeTelegram) lastKeyboard(t *testing.T) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		kb, ok := f.calls[i].Body["reply_markup"].(map[string]any)
		if !ok {
			continue
		}
		var out []string
		for _, row := range kb["inline_keyboard"].([]any) {
			for _, b := range row.([]any) {
				out = append(out, b.(map[string]any)["callback_data"].(string))
			}
		}
		return out
	}
	t.Fatal("no keyboard sent")
	return nil
}

const operator = 1001

func setup(t *testing.T, preset string) (*Bot, *order.Service, *fakeTelegram) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := profile.Load(filepath.Join(filepath.Dir(file), "..", "..", "profiles", preset+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	req := &order.Requisites{Recipient: "ИП Тест", Lines: []order.RequisiteRow{{Label: "Счёт", Value: "1"}}, Purpose: "{ref}"}
	svc := order.New(p, st, &mail.Outbox{}, req, []byte("s"), "https://shop.example")
	svc.Now = func() time.Time { return time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC) }
	fake := &fakeTelegram{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	b := New(&API{Base: srv.URL + "/botTEST", HTTP: srv.Client()}, svc, []int64{operator}, nil)
	svc.Notify = b
	return b, svc, fake
}

func create(t *testing.T, svc *order.Service) *order.Order {
	t.Helper()
	o, _, err := svc.Create(context.Background(), order.CreateInput{
		Items:  map[string]int{"baton": 3},
		Fields: map[string]string{"name": "Анна", "phone": "+79001112233", "email": "anna@example.ru", "pickup_at": "2026-10-27T15:00"},
		Method: profile.MethodTransfer, ClientIP: "203.0.113.9", FillSeconds: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func press(b *Bot, data string) {
	b.Handle(context.Background(), Update{Callback: &CallbackQuery{ID: "cb", From: User{ID: operator, Username: "op"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: operator}}, Data: data}})
}

func say(b *Bot, from int64, text string) {
	b.Handle(context.Background(), Update{Message: &Message{From: &User{ID: from}, Chat: Chat{ID: from}, Text: text}})
}

func TestNewOrderCardAndButtons(t *testing.T) {
	b, svc, fake := setup(t, "bakery")
	o := create(t, svc)
	texts := fake.texts("sendMessage")
	if len(texts) == 0 || !strings.Contains(texts[0], "Заказ №1") || !strings.Contains(texts[0], "Батон") {
		t.Fatalf("card: %v", texts)
	}
	kb := fake.lastKeyboard(t)
	if strings.Join(kb, " ") != "p:1 t:1:accept t:1:cancel b:1" {
		t.Fatalf("buttons: %v", kb)
	}
	press(b, "t:1:accept")
	press(b, "t:1:ready")
	press(b, "t:1:hand_over") // not paid yet
	got, _ := svc.Store.Get(context.Background(), o.ID)
	if got.State != "ready" {
		t.Fatalf("state %s", got.State)
	}
	press(b, "p:1")
	press(b, "t:1:hand_over")
	got, _ = svc.Store.Get(context.Background(), o.ID)
	if got.State != "done" || !got.Paid {
		t.Fatalf("after payment: %s paid=%v", got.State, got.Paid)
	}
	edits := fake.texts("editMessageText")
	if !strings.Contains(edits[len(edits)-1], "Выдан: op") {
		t.Fatalf("last edit: %s", edits[len(edits)-1])
	}
}

func TestStrangersAreRefused(t *testing.T) {
	b, svc, fake := setup(t, "bakery")
	create(t, svc)
	say(b, 777, "/orders")
	if texts := fake.texts("sendMessage"); !strings.Contains(texts[len(texts)-1], "только для операторов") {
		t.Fatalf("stranger: %v", texts)
	}
	b.Handle(context.Background(), Update{Callback: &CallbackQuery{ID: "x", From: User{ID: 777}, Data: "p:1"}})
	if got, _ := svc.Store.Get(context.Background(), 1); got.Paid {
		t.Fatal("a stranger marked the order paid")
	}
}

func TestQuoteCommandAndReviewButton(t *testing.T) {
	b, svc, fake := setup(t, "freight")
	_, _, err := svc.Create(context.Background(), order.CreateInput{
		Fields: map[string]string{"name": "Пётр", "phone": "+79001234567", "email": "petr@example.ru", "from_address": "А",
			"to_address": "Б", "cargo": "Шкаф", "weight_kg": "80", "loaders": "1", "date": "2026-10-28T09:00"},
		Method: profile.MethodTransfer, ClientIP: "203.0.113.10", FillSeconds: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if kb := strings.Join(fake.lastKeyboard(t), " "); !strings.HasPrefix(kb, "a:1") || strings.Contains(kb, "p:1") {
		t.Fatalf("quote card buttons: %s", kb)
	}
	say(b, operator, "/price 1 12 500")
	o, _ := svc.Store.Get(context.Background(), 1)
	if o.Total != 1250000 || !o.Priced || !o.Approved {
		t.Fatalf("after /price: %+v", o)
	}
	press(b, "t:1:quote")
	if o, _ = svc.Store.Get(context.Background(), 1); o.State != "quoted" {
		t.Fatalf("state %s", o.State)
	}
}

func TestPauseAndStats(t *testing.T) {
	b, svc, fake := setup(t, "bakery")
	say(b, operator, "/pause проверка сервера")
	if !svc.Paused() {
		t.Fatal("not paused")
	}
	say(b, operator, "/resume")
	if svc.Paused() {
		t.Fatal("not resumed")
	}
	create(t, svc)
	say(b, operator, "/stats")
	texts := fake.texts("sendMessage")
	if !strings.Contains(texts[len(texts)-1], "заказов 1") {
		t.Fatalf("stats: %s", texts[len(texts)-1])
	}
}

func TestStartedReportsFingerprint(t *testing.T) {
	b, svc, fake := setup(t, "bakery")
	b.Started(context.Background(), "test")
	if texts := fake.texts("sendMessage"); !strings.Contains(texts[0], svc.Req.Fingerprint()) {
		t.Fatalf("tripwire: %v", texts)
	}
}
