package bot

import (
	"context"
	"strings"
	"testing"
)

// A customer ties his Telegram to the order from its page, writes; the operator answers by a reply; the answer
// and the order's news reach the customer, nobody writes to anybody's private account.
func TestOrderChat(t *testing.T) {
	b, svc, fake := setup(t, "bakery")
	svc.Customer = b
	ctx := context.Background()
	o := create(t, svc)
	const customer = 555
	b.Handle(ctx, Update{Message: &Message{From: &User{ID: customer, Username: "anna_k", FirstName: "Анна"}, Chat: Chat{ID: customer}, Text: "/start o" + o.Token}})
	if o, _ = svc.Store.Get(ctx, o.ID); o.Fields["telegram_id"] != "555" {
		t.Fatalf("the chat is not tied to the order: %v", o.Fields)
	}
	say(b, customer, "Можно забрать в пять?")
	texts := fake.texts("sendMessage")
	if !strings.Contains(strings.Join(texts, "\n"), "💬 <b>Заказ №1</b>") || !strings.Contains(texts[len(texts)-1], "Передали продавцу") {
		t.Fatalf("the customer's message did not reach the operators: %v", texts)
	}
	b.Handle(ctx, Update{Message: &Message{From: &User{ID: operator}, Chat: Chat{ID: operator}, Text: "Да, ждём",
		ReplyTo: &Message{Text: "💬 Заказ №1 · Анна: Можно забрать в пять?"}}})
	msgs, _ := svc.Messages(ctx, o)
	if len(msgs) != 2 || !msgs[0].Mine || msgs[1].Mine || msgs[1].Text != "Да, ждём" {
		t.Fatalf("the chat: %+v", msgs)
	}
	sentTo := func(chat float64, part string) bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, c := range fake.calls {
			if c.Method == "sendMessage" && c.Body["chat_id"] == chat && strings.Contains(c.Body["text"].(string), part) {
				return true
			}
		}
		return false
	}
	if !sentTo(customer, "Да, ждём") {
		t.Fatal("the answer did not reach the customer's Telegram")
	}
	o, _ = svc.Store.Get(ctx, o.ID)
	if err := svc.ConfirmPayment(ctx, o, "1001", ""); err != nil {
		t.Fatal(err)
	}
	if !sentTo(customer, "оплата получена") {
		t.Fatal("the customer was not told the payment arrived")
	}
	// another chat cannot read or write the order
	say(b, 999, "привет")
	if msgs, _ := svc.Messages(ctx, o); len(msgs) != 2 {
		t.Fatal("a stranger's message landed in the order")
	}
}

// «Связь только через бота» is on the operators' card.
func TestBotOnlyOnTheCard(t *testing.T) {
	b, svc, _ := setup(t, "bakery")
	o := create(t, svc)
	if err := svc.SetBotOnly(context.Background(), o, true); err != nil {
		t.Fatal(err)
	}
	if text, _ := b.card(o); !strings.Contains(text, "Связь только через бота") {
		t.Fatal("the card does not say it")
	}
}

// «Забанить» under a customer's message closes his chat on the page and in the bot, and his new orders.
func TestBanFromTheChat(t *testing.T) {
	b, svc, fake := setup(t, "bakery")
	svc.Customer = b
	ctx := context.Background()
	o := create(t, svc)
	const customer = 556
	b.Handle(ctx, Update{Message: &Message{From: &User{ID: customer}, Chat: Chat{ID: customer}, Text: "/start o" + o.Token}})
	say(b, customer, "спам")
	press(b, "b:1")
	o, _ = svc.Store.Get(ctx, o.ID)
	if svc.ChatOpen(ctx, o) {
		t.Fatal("the banned customer can still write")
	}
	if err := svc.CustomerSays(ctx, o, "ещё спам"); err == nil {
		t.Fatal("a banned customer's message was taken from the page")
	}
	say(b, customer, "и ещё")
	if texts := fake.texts("sendMessage"); !strings.Contains(texts[len(texts)-1], "отключены") {
		t.Fatalf("the bot took a banned customer's message: %v", texts[len(texts)-1])
	}
	if blocked, _ := svc.Store.IsBlocked(ctx, "tg:556"); !blocked {
		t.Fatal("the Telegram account is not on the blocklist")
	}
}

// «Выключить чат»: nobody writes, from the page or the bot, until it is on again; the news still go.
func TestChatSwitch(t *testing.T) {
	b, svc, fake := setup(t, "bakery")
	ctx := context.Background()
	o := create(t, svc)
	press(b, "c:off")
	if !svc.ChatOff(ctx) || svc.ChatOpen(ctx, o) || !strings.Contains(strings.Join(fake.texts("sendMessage"), "\n"), "Чат выключен") {
		t.Fatal("the chat is not off")
	}
	if err := svc.CustomerSays(ctx, o, "привет"); err == nil {
		t.Fatal("a message got through a closed chat")
	}
	say(b, operator, "/chat on")
	if svc.ChatOff(ctx) || !svc.ChatOpen(ctx, o) {
		t.Fatal("/chat on did not open it")
	}
}
