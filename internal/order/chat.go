package order

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// The order's chat: the customer writes on the order page (or to the bot, once he linked Telegram), the
// operators get it in their bot and answer there; the answer shows on the page and, linked, in Telegram.
// Nobody writes to anybody's private account. The messages are the order's events, kinds message and reply.

// CustomerChannel reaches the customer of an order outside the page: the bot, when he linked Telegram.
type CustomerChannel interface {
	Tell(ctx context.Context, o *Order, text string)
}

// NopChannel reaches nobody: the page shows everything anyway.
type NopChannel struct{}

func (NopChannel) Tell(context.Context, *Order, string) {}

// MaxMessage is the longest message of the order's chat, in characters.
const MaxMessage = 1000

// Message is one line of the order's chat.
type Message struct {
	Mine bool // the customer's own
	Text string
	Event
}

func message(text string) (string, bool) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" || utf8.RuneCountInString(text) > MaxMessage {
		return "", false
	}
	return text, true
}

// ErrChatClosed: the chat is off for everyone, or this customer is banned.
var ErrChatClosed = errors.New("сообщения отключены")

// ChatOpen says the customer of the order may write: the chat is on and he is not banned.
func (s *Service) ChatOpen(ctx context.Context, o *Order) bool {
	if s.ChatOff(ctx) || o.Fields["chat_blocked"] == "1" {
		return false
	}
	if id := o.Fields["telegram_id"]; id != "" {
		if banned, err := s.Store.IsBlocked(ctx, "tg:"+id); err == nil && banned {
			return false
		}
	}
	return true
}

// ChatOff: the operators turned the customers' messages off for every order (one button in the bot).
func (s *Service) ChatOff(ctx context.Context) bool { return s.Setting(ctx, "chat_off") == "1" }

// SetChatOff turns the customers' messages off or back on; the order's news still reach them.
func (s *Service) SetChatOff(ctx context.Context, off bool) error {
	v := ""
	if off {
		v = "1"
	}
	return s.SetSetting(ctx, "chat_off", v)
}

// CustomerSays is a message of the customer: the operators get it in their bot.
func (s *Service) CustomerSays(ctx context.Context, o *Order, text string) error {
	if !s.ChatOpen(ctx, o) {
		return ErrChatClosed
	}
	text, ok := message(text)
	if !ok {
		return ErrForbidden
	}
	now := s.Now()
	o.UpdatedAt = now
	return s.save(ctx, o, Event{At: now, Actor: profile.ActorCustomer, Kind: "message", Note: text})
}

// OperatorSays is an operator's answer: on the order page, and in Telegram when the customer linked it.
func (s *Service) OperatorSays(ctx context.Context, o *Order, operatorID, text string) error {
	text, ok := message(text)
	if !ok {
		return ErrForbidden
	}
	now := s.Now()
	o.UpdatedAt = now
	if err := s.save(ctx, o, Event{At: now, Actor: profile.ActorOperator, ActorID: operatorID, Kind: "reply", Note: text}); err != nil {
		return err
	}
	s.customer().Tell(ctx, o, "💬 "+s.P.Business.Name+": "+text)
	return nil
}

// Messages is the order's chat, oldest first.
func (s *Service) Messages(ctx context.Context, o *Order) ([]Message, error) {
	evs, err := s.Store.Events(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, e := range evs {
		if e.Kind == "message" || e.Kind == "reply" {
			out = append(out, Message{Mine: e.Kind == "message", Text: e.Note, Event: e})
		}
	}
	return out, nil
}

// LinkTelegram ties the customer's Telegram chat to the order (he pressed «Старт» from the order page's link):
// the bot tells him the news of the order and passes on what he writes. The contact field gets his @name
// unless he typed one.
func (s *Service) LinkTelegram(ctx context.Context, o *Order, chat int64, username, name string) error {
	if chat <= 0 {
		return ErrForbidden
	}
	if o.Fields == nil {
		o.Fields = map[string]string{}
	}
	o.Fields["telegram_id"] = strconv.FormatInt(chat, 10)
	contact := "Telegram id " + strconv.FormatInt(chat, 10)
	if username != "" {
		contact = "@" + username
	}
	if after := s.P.Checkout.After; len(after) > 0 && o.Fields[after[0]] == "" {
		o.Fields[after[0]] = contact
	}
	if name = clean(name); name != "" && o.Fields["name"] == "" {
		o.Fields["name"] = name
	}
	now := s.Now()
	o.UpdatedAt = now
	if err := s.save(ctx, o, Event{At: now, Actor: profile.ActorCustomer, Kind: "telegram", Note: "клиент подключил Telegram: " + contact}); err != nil {
		return err
	}
	return s.SetSetting(ctx, "tgchat:"+strconv.FormatInt(chat, 10), o.Token)
}

// ByTelegram is the order the customer's Telegram chat was last tied to.
func (s *Service) ByTelegram(ctx context.Context, chat int64) (*Order, error) {
	token := s.Setting(ctx, "tgchat:"+strconv.FormatInt(chat, 10))
	if token == "" {
		return nil, ErrForbidden
	}
	return s.Store.ByToken(ctx, token)
}

// StateText is what the customer reads about where the order is.
func (s *Service) StateText(o *Order) string {
	st, _ := s.P.State(o.State)
	text := st.Customer
	if text == "" {
		text = st.Title
	}
	switch {
	case o.Paid && o.State == s.Flow.Initial():
		text = "оплата получена" // the state is still the first one until an operator takes the order
	case o.Paid:
	case o.ReportedAt != nil && !st.Final:
		text = "проверяем оплату"
	}
	return text
}

// tellState sends the customer the order's new state in Telegram, when he linked it.
func (s *Service) tellState(ctx context.Context, o *Order) {
	s.customer().Tell(ctx, o, "Заказ №"+strconv.FormatInt(o.ID, 10)+": "+s.StateText(o)+"\n"+s.Link(o))
}

func (s *Service) customer() CustomerChannel {
	if s.Customer == nil {
		return NopChannel{}
	}
	return s.Customer
}

// BotOnly is the customer's «Связь только через бота»: the operators answer him through the bot and the order
// page, they do not write to his phone or his private Telegram.
func (s *Service) SetBotOnly(ctx context.Context, o *Order, on bool) error {
	if o.Fields == nil {
		o.Fields = map[string]string{}
	}
	if (o.Fields["bot_only"] == "1") == on {
		return nil
	}
	note := "клиент просит связь только через бота"
	if on {
		o.Fields["bot_only"] = "1"
	} else {
		delete(o.Fields, "bot_only")
		note = "клиент снял «связь только через бота»"
	}
	now := s.Now()
	o.UpdatedAt = now
	return s.save(ctx, o, Event{At: now, Actor: profile.ActorCustomer, Kind: "bot_only", Note: note})
}

// BotOnly says the customer asked to be reached only through the bot.
func (o *Order) BotOnly() bool { return o.Fields["bot_only"] == "1" }

// ResultURL is the address of what the customer gets (checkout.result with his fields), empty while a field
// it needs is not there.
func (s *Service) ResultURL(o *Order) string {
	tpl := s.P.Checkout.Result
	if tpl == "" {
		return ""
	}
	out, missing := tpl, false
	for _, f := range s.P.Checkout.Fields {
		key := "{" + f.ID + "}"
		if strings.Contains(out, key) {
			v := o.Fields[f.ID]
			if v == "" {
				missing = true
			}
			out = strings.ReplaceAll(out, key, url.PathEscape(v))
		}
	}
	if missing {
		return ""
	}
	return out
}

// SetAdminLink keeps the link to the customer's own back office (an operator sends it once the work is done):
// the order page shows it to the customer, and the bot sends it to him with the shop's address.
func (s *Service) SetAdminLink(ctx context.Context, o *Order, operatorID, link string) error {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("нужна ссылка https://…")
	}
	if o.Fields == nil {
		o.Fields = map[string]string{}
	}
	o.Fields["admin_link"] = u.String()
	now := s.Now()
	o.UpdatedAt = now
	if err := s.save(ctx, o, Event{At: now, Actor: profile.ActorOperator, ActorID: operatorID, Kind: "admin_link", Note: "ссылка на админку передана"}); err != nil {
		return err
	}
	text := "🎉 Заказ №" + strconv.FormatInt(o.ID, 10) + " готов."
	if site := s.ResultURL(o); site != "" {
		text += "\nВаш сайт: " + site
	}
	text += "\nАдминка: " + u.String() + "\nСсылку на админку никому не пересылайте: она открывает ваши заказы."
	s.customer().Tell(ctx, o, text)
	return nil
}
