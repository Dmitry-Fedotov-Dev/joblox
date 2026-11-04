package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
)

// The order's chat in the bot. A customer opens the bot from his order page (/start o<token>): the chat is tied
// to the order, the bot sends him its news, and what he writes goes to the operators. An operator answers by
// replying to that message; the answer shows on the order page and in the customer's chat.

// Tell writes to the customer of the order, when he tied his Telegram to it.
func (b *Bot) Tell(ctx context.Context, o *order.Order, text string) {
	if b.Relay {
		return
	}
	id, err := strconv.ParseInt(o.Fields["telegram_id"], 10, 64)
	if err != nil || id <= 0 {
		return
	}
	if _, err := b.api.Send(ctx, id, html.EscapeString(text), nil); err != nil {
		b.log.Warn("telegram to the customer", "order", o.ID, "err", err)
	}
}

// customerSays serves a customer's message: «Старт» from the order page, or a message about the order tied to
// his chat. False: not a customer's matter, the operators' part of the bot answers.
// An operator testing his own order may tie his chat to it too; his other messages stay the operators' commands.
func (b *Bot) customerSays(ctx context.Context, m *Message, operator bool) bool {
	text := strings.TrimSpace(m.Text)
	if cmd, arg, _ := strings.Cut(text, " "); cmd == "/start" && strings.HasPrefix(arg, "o") {
		o, err := b.svc.Store.ByToken(ctx, strings.TrimPrefix(strings.TrimSpace(arg), "o"))
		if err != nil {
			_, _ = b.api.Send(ctx, m.Chat.ID, "Заказ не найден: откройте бота заново по кнопке на странице заказа.", nil)
			return true
		}
		name := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
		if err := b.svc.LinkTelegram(ctx, o, m.Chat.ID, m.From.Username, name); err != nil {
			b.log.Warn("link telegram", "order", o.ID, "err", err)
		}
		_, _ = b.api.Send(ctx, m.Chat.ID, fmt.Sprintf("Здравствуйте! Это бот %s.\n\n<b>Заказ №%d</b>: %s.\n\n"+
			"Сюда придут новости заказа. Напишите сообщение, и его получит продавец: ответ придёт сюда и на страницу заказа.",
			html.EscapeString(b.svc.P.Business.Name), o.ID, html.EscapeString(b.svc.StateText(o))), nil)
		return true
	}
	if operator || text == "" || strings.HasPrefix(text, "/") {
		return false
	}
	o, err := b.svc.ByTelegram(ctx, m.Chat.ID)
	if err != nil {
		return false
	}
	if err := b.svc.CustomerSays(ctx, o, text); errors.Is(err, order.ErrChatClosed) {
		_, _ = b.api.Send(ctx, m.Chat.ID, "Сообщения продавцу сейчас отключены. Новости заказа будут приходить сюда.", nil)
		return true
	} else if err != nil {
		_, _ = b.api.Send(ctx, m.Chat.ID, fmt.Sprintf("Не отправилось: сообщение до %d символов.", order.MaxMessage), nil)
		return true
	}
	_, _ = b.api.Send(ctx, m.Chat.ID, "✅ Передали продавцу. Ответ придёт сюда.", nil)
	return true
}

var orderRe = regexp.MustCompile(`Заказ №(\d+)`)

// operatorAnswers: an operator's reply to a customer's message (or to any card of an order) goes to that
// customer. False: not a reply, the commands and the menu answer.
func (b *Bot) operatorAnswers(ctx context.Context, m *Message) bool {
	text := strings.TrimSpace(m.Text)
	if m.ReplyTo == nil || text == "" || strings.HasPrefix(text, "/") {
		return false
	}
	found := orderRe.FindStringSubmatch(m.ReplyTo.Text)
	if found == nil {
		return false
	}
	id, _ := strconv.ParseInt(found[1], 10, 64)
	o, err := b.svc.Store.Get(ctx, id)
	if err != nil {
		_, _ = b.api.Send(ctx, m.Chat.ID, fmt.Sprintf("Заказ №%d не найден.", id), nil)
		return true
	}
	if err := b.svc.OperatorSays(ctx, o, strconv.FormatInt(m.From.ID, 10), text); err != nil {
		_, _ = b.api.Send(ctx, m.Chat.ID, fmt.Sprintf("Не отправилось: ответ до %d символов.", order.MaxMessage), nil)
		return true
	}
	where := "на странице заказа"
	if o.Fields["telegram_id"] != "" {
		where += " и в его Telegram"
	}
	_, _ = b.api.Send(ctx, m.Chat.ID, fmt.Sprintf("✅ Заказ №%d: ответ отправлен покупателю %s.", o.ID, where), nil)
	return true
}

// chatSwitch turns the customers' messages off or on for every order and says so; callback is the button's
// query to answer, empty for the /chat command.
func (b *Bot) chatSwitch(ctx context.Context, callback string, m *Message, off bool) {
	if err := b.svc.SetChatOff(ctx, off); err != nil {
		b.log.Warn("chat switch", "err", err)
		return
	}
	text, kb := "💬 Чат включён: покупатели снова могут писать со страницы заказа и в боте.", (*Keyboard)(nil)
	if off {
		text = "🔕 Чат выключен: покупатели больше не могут писать со страницы заказа и в боте. Новости заказов им приходят."
		kb = &Keyboard{Rows: [][]Button{{{Text: "💬 Включить чат", Data: "c:on"}}}}
	}
	if callback != "" {
		_ = b.api.Answer(ctx, callback, "")
	}
	if m != nil {
		_, _ = b.api.Send(ctx, m.Chat.ID, text, kb)
	}
}

// who is the customer of the order as the operators see him: the name and the contact.
func (b *Bot) who(o *order.Order) string {
	var parts []string
	for _, k := range []string{"name", "phone"} {
		if v := o.Fields[k]; v != "" {
			parts = append(parts, html.EscapeString(v))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, ", ")
}
