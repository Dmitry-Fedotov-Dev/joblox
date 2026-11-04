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
