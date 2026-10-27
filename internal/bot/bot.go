// Package bot is the operators' Telegram bot: a card for every new order with the buttons the profile's
// workflow allows right now, payment confirmation, approval after the anti-fraud, quotes, the blocklist,
// pause. Only Telegram accounts listed in OPENLINE_OPERATORS are served.
package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/antifraud"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/money"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/order"
	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/profile"
)

type Bot struct {
	api       *API
	svc       *order.Service
	operators map[int64]bool
	log       *slog.Logger

	mu        sync.Mutex
	refused   int       // refused attempts since the last summary
	lastNotes time.Time // throttles refusal summaries: an attack must not flood the operators
}

func New(api *API, svc *order.Service, operators []int64, log *slog.Logger) *Bot {
	ops := map[int64]bool{}
	for _, id := range operators {
		ops[id] = true
	}
	if log == nil {
		log = slog.Default()
	}
	return &Bot{api: api, svc: svc, operators: ops, log: log}
}

func (b *Bot) operatorIDs() []int64 {
	ids := make([]int64, 0, len(b.operators))
	for id := range b.operators {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (b *Bot) broadcast(ctx context.Context, text string, kb *Keyboard) {
	for _, id := range b.operatorIDs() {
		if _, err := b.api.Send(ctx, id, text, kb); err != nil {
			b.log.Warn("telegram send", "err", err)
		}
	}
}

// Started is the tripwire: every restart tells the operators which requisites the server is showing.
// If the fingerprint changes without a reason, someone has edited the payment details on the server.
func (b *Bot) Started(ctx context.Context, version string) {
	req := "оплата переводом выключена"
	if b.svc.Req != nil {
		req = fmt.Sprintf("получатель %s, отпечаток <code>%s</code>", html.EscapeString(b.svc.Req.Recipient), b.svc.Req.Fingerprint())
	}
	b.broadcast(ctx, fmt.Sprintf("🟢 <b>%s</b> запущен (%s).\nРеквизиты: %s.\nЕсли отпечаток изменился, а вы реквизиты не меняли, "+
		"остановите приём командой /pause и проверьте сервер.", html.EscapeString(b.svc.P.Business.Name), html.EscapeString(version), req), nil)
}

// Run polls Telegram until ctx ends.
func (b *Bot) Run(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		updates, err := b.api.Updates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() == nil {
				b.log.Warn("telegram updates", "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.Handle(ctx, u)
		}
	}
}

// Handle processes one update; exported for tests.
func (b *Bot) Handle(ctx context.Context, u Update) {
	switch {
	case u.Message != nil && u.Message.From != nil:
		if !b.operators[u.Message.From.ID] {
			_, _ = b.api.Send(ctx, u.Message.Chat.ID, fmt.Sprintf("Этот бот только для операторов. Ваш id: <code>%d</code>.", u.Message.From.ID), nil)
			return
		}
		b.command(ctx, u.Message)
	case u.Callback != nil:
		if !b.operators[u.Callback.From.ID] {
			_ = b.api.Answer(ctx, u.Callback.ID, "Нет доступа")
			return
		}
		b.button(ctx, u.Callback)
	}
}

// ---------------------------------------------------------------- cards

func (b *Bot) card(o *order.Order) (string, *Keyboard) {
	p := b.svc.P
	st, _ := p.State(o.State)
	var s strings.Builder
	fmt.Fprintf(&s, "<b>Заказ №%d</b> · %s\n", o.ID, html.EscapeString(st.Title))
	for _, l := range o.Lines {
		fmt.Fprintf(&s, "• %s × %d = %s\n", html.EscapeString(l.Title), l.Qty, l.Sum)
	}
	for _, f := range o.Fees {
		fmt.Fprintf(&s, "• %s: %s\n", html.EscapeString(f.Title), f.Sum)
	}
	if o.Priced {
		fmt.Fprintf(&s, "<b>Сумма: %s %s</b>\n", o.Total, p.Business.Currency)
	} else {
		fmt.Fprintf(&s, "<b>Цена по запросу:</b> /price %d сумма\n", o.ID)
	}
	for _, f := range p.Checkout.Fields {
		if v := o.Fields[f.ID]; v != "" {
			fmt.Fprintf(&s, "%s: %s%s\n", html.EscapeString(f.Label), html.EscapeString(v), unit(f.Unit))
		}
	}
	method := "перевод"
	if o.Method == profile.MethodCash {
		method = "при получении"
	}
	paid := "не оплачен"
	if o.Paid {
		paid = "оплачен"
		if o.PaidNote != "" {
			paid += " (" + html.EscapeString(o.PaidNote) + ")"
		}
	}
	fmt.Fprintf(&s, "Оплата: %s, %s · ссылка: %s\n", method, paid, b.svc.Reference(o))
	verified := "не подтверждён"
	if o.VerifiedAt != nil {
		verified = "подтверждён"
	}
	fmt.Fprintf(&s, "Email %s\n", verified)
	switch {
	case o.Review && !o.Approved:
		fmt.Fprintf(&s, "⚠️ <b>На проверке</b>, риск %d: %s\n", o.Risk, html.EscapeString(strings.Join(o.RiskReasons, "; ")))
	case len(o.RiskReasons) > 0:
		fmt.Fprintf(&s, "Проверка: риск %d (%s)\n", o.Risk, html.EscapeString(strings.Join(o.RiskReasons, "; ")))
	}
	blocked := b.svc.Flow.Blocked(o.State, profile.ActorOperator, o.Flags())
	for _, t := range p.Workflow.Transitions { // profile order, not map order
		if need, ok := blocked[t.ID]; ok {
			fmt.Fprintf(&s, "«%s» ждёт: %s\n", html.EscapeString(t.Title), needText(need))
		}
	}

	kb := &Keyboard{}
	final := b.svc.IsFinal(o.State)
	if o.Review && !o.Approved && !final {
		kb.Rows = append(kb.Rows, []Button{{Text: "✅ Одобрить", Data: fmt.Sprintf("a:%d", o.ID)}})
	}
	if !o.Paid && !final && o.Priced {
		kb.Rows = append(kb.Rows, []Button{{Text: "💰 Оплата получена", Data: fmt.Sprintf("p:%d", o.ID)}})
	}
	var row []Button
	for _, t := range b.svc.Flow.Open(o.State, profile.ActorOperator, o.Flags()) {
		row = append(row, Button{Text: "→ " + t.Title, Data: fmt.Sprintf("t:%d:%s", o.ID, t.ID)})
		if len(row) == 2 {
			kb.Rows, row = append(kb.Rows, row), nil
		}
	}
	if len(row) > 0 {
		kb.Rows = append(kb.Rows, row)
	}
	if !final {
		kb.Rows = append(kb.Rows, []Button{{Text: "⛔ В чёрный список", Data: fmt.Sprintf("b:%d", o.ID)}})
	}
	if len(kb.Rows) == 0 {
		kb = &Keyboard{Rows: [][]Button{}}
	}
	return s.String(), kb
}

func unit(u string) string {
	if u == "" {
		return ""
	}
	return " " + html.EscapeString(u)
}

func transitionTitle(p *profile.Profile, id string) string {
	for _, t := range p.Workflow.Transitions {
		if t.ID == id {
			return t.Title
		}
	}
	return id
}

func needText(flag string) string {
	switch flag {
	case profile.FlagPaid:
		return "оплаты"
	case profile.FlagApproved:
		return "одобрения"
	case profile.FlagPriced:
		return "цены"
	case profile.FlagVerified:
		return "подтверждения email клиентом"
	}
	return flag
}

// ---------------------------------------------------------------- order.Notifier

func (b *Bot) OrderCreated(ctx context.Context, o *order.Order, d antifraud.Decision) {
	text, kb := b.card(o)
	head := "🆕 "
	if d.Action == antifraud.Review {
		head = "🆕⚠️ "
	}
	b.broadcast(ctx, head+text, kb)
}

func (b *Bot) OrderChanged(ctx context.Context, o *order.Order, ev order.Event) {
	var note string
	switch ev.Kind {
	case "verified":
		note = "Клиент подтвердил email"
	case "transition":
		if ev.Actor == profile.ActorCustomer {
			note = "Клиент: " + ev.Note
		} else {
			note = "Автоматически: " + ev.Note
		}
	default:
		return
	}
	text, kb := b.card(o)
	b.broadcast(ctx, "🔔 "+html.EscapeString(note)+"\n"+text, kb)
}

// Refused collects refusals and reports at most once in ten minutes.
func (b *Bot) Refused(ctx context.Context, _ string, d antifraud.Decision) {
	b.mu.Lock()
	b.refused++
	if time.Since(b.lastNotes) < 10*time.Minute {
		b.mu.Unlock()
		return
	}
	n := b.refused
	b.refused, b.lastNotes = 0, time.Now()
	b.mu.Unlock()
	b.broadcast(ctx, fmt.Sprintf("🛑 Антифрод отклонил попыток: %d. Последняя причина: %s.", n,
		html.EscapeString(strings.Join(d.Reasons, "; "))), nil)
}

// ---------------------------------------------------------------- buttons

func (b *Bot) button(ctx context.Context, cq *CallbackQuery) {
	parts := strings.Split(cq.Data, ":")
	if len(parts) < 2 || cq.Message == nil {
		_ = b.api.Answer(ctx, cq.ID, "Неизвестная кнопка")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		_ = b.api.Answer(ctx, cq.ID, "Неизвестная кнопка")
		return
	}
	o, err := b.svc.Store.Get(ctx, id)
	if err != nil {
		_ = b.api.Answer(ctx, cq.ID, "Заказ не найден")
		return
	}
	op := strconv.FormatInt(cq.From.ID, 10)
	var done string
	switch parts[0] {
	case "a":
		err, done = b.svc.Approve(ctx, o, op), "Одобрено"
	case "p":
		err, done = b.svc.ConfirmPayment(ctx, o, op, "отмечено в боте"), "Оплата отмечена"
	case "b":
		err, done = b.svc.BlockCustomer(ctx, o, op), "Клиент в чёрном списке"
	case "t":
		if len(parts) != 3 {
			err = errors.New("bad button")
			break
		}
		err, done = b.svc.Transition(ctx, o, parts[2], profile.ActorOperator, op), transitionTitle(b.svc.P, parts[2])
	default:
		err = errors.New("bad button")
	}
	if err != nil {
		msg := "Не выполнено: " + err.Error()
		if errors.Is(err, order.ErrConflict) {
			msg = "Заказ уже изменили, карточка обновлена"
		}
		_ = b.api.Answer(ctx, cq.ID, msg)
		if fresh, gerr := b.svc.Store.Get(ctx, id); gerr == nil {
			o = fresh
		}
	} else {
		_ = b.api.Answer(ctx, cq.ID, done)
	}
	text, kb := b.card(o)
	who := cq.From.Username
	if who == "" {
		who = op
	}
	if err == nil {
		text += "\n<i>" + html.EscapeString(done+": "+who) + "</i>"
	}
	if eerr := b.api.Edit(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, kb); eerr != nil {
		b.log.Debug("edit card", "err", eerr)
	}
}

// ---------------------------------------------------------------- commands

const help = `Команды:
/orders — открытые заказы
/review — заказы на проверке
/order N — карточка заказа
/price N 1500 — назвать цену заказа по запросу
/paid N [комментарий] — отметить оплату
/block email|телефон — в чёрный список, /unblock — убрать
/stats — заказы и выручка за сутки
/pause [причина] — остановить приём заказов, /resume — возобновить`

func (b *Bot) command(ctx context.Context, m *Message) {
	cmd, args, _ := strings.Cut(strings.TrimSpace(m.Text), " ")
	cmd = strings.SplitN(cmd, "@", 2)[0]
	args = strings.TrimSpace(args)
	op := strconv.FormatInt(m.From.ID, 10)
	say := func(text string) { _, _ = b.api.Send(ctx, m.Chat.ID, text, nil) }
	switch cmd {
	case "/start", "/help":
		say(html.EscapeString(help))
	case "/orders", "/review":
		list, err := b.svc.Store.List(ctx, order.Filter{Open: true, Review: cmd == "/review", Limit: 15}, b.svc.Finals())
		if err != nil {
			say("Ошибка: " + html.EscapeString(err.Error()))
			return
		}
		if len(list) == 0 {
			say("Нет заказов.")
			return
		}
		var s strings.Builder
		for _, o := range list {
			st, _ := b.svc.P.State(o.State)
			sum := "по запросу"
			if o.Priced {
				sum = o.Total.String()
			}
			flag := ""
			if o.Review && !o.Approved {
				flag = " ⚠️"
			}
			fmt.Fprintf(&s, "/order %d · %s · %s%s\n", o.ID, html.EscapeString(st.Title), sum, flag)
		}
		say(s.String())
	case "/order":
		o, ok := b.find(ctx, args, say)
		if ok {
			text, kb := b.card(o)
			_, _ = b.api.Send(ctx, m.Chat.ID, text, kb)
		}
	case "/price":
		num, amount, _ := strings.Cut(args, " ")
		o, ok := b.find(ctx, num, say)
		if !ok {
			return
		}
		a, err := money.Parse(amount)
		if err != nil {
			say("Использование: /price N 1500")
			return
		}
		if err := b.svc.SetPrice(ctx, o, a, op); err != nil {
			say("Не выполнено: " + html.EscapeString(err.Error()))
			return
		}
		text, kb := b.card(o)
		_, _ = b.api.Send(ctx, m.Chat.ID, "Цена названа.\n"+text, kb)
	case "/paid":
		num, note, _ := strings.Cut(args, " ")
		o, ok := b.find(ctx, num, say)
		if !ok {
			return
		}
		if note == "" {
			note = "отмечено командой"
		}
		if err := b.svc.ConfirmPayment(ctx, o, op, note); err != nil {
			say("Не выполнено: " + html.EscapeString(err.Error()))
			return
		}
		text, kb := b.card(o)
		_, _ = b.api.Send(ctx, m.Chat.ID, "Оплата отмечена.\n"+text, kb)
	case "/block", "/unblock":
		v := strings.ToLower(args)
		if v == "" {
			say("Использование: /block email или телефон")
			return
		}
		if !strings.Contains(v, "@") {
			v = strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, v)
		}
		var err error
		if cmd == "/block" {
			err = b.svc.Store.Block(ctx, v, "командой оператора "+op)
		} else {
			err = b.svc.Store.Unblock(ctx, v)
		}
		if err != nil {
			say("Ошибка: " + html.EscapeString(err.Error()))
			return
		}
		say("Готово: " + html.EscapeString(v))
	case "/stats":
		st, err := b.svc.Store.Stats(ctx, b.svc.Now().Add(-24*time.Hour), b.svc.Finals())
		if err != nil {
			say("Ошибка: " + html.EscapeString(err.Error()))
			return
		}
		say(fmt.Sprintf("За сутки: заказов %d, оплачено %d на %s %s.\nОткрыто: %d, на проверке: %d.", st.Today, st.PaidToday,
			st.RevenueDay, b.svc.P.Business.Currency, st.Open, st.Review))
	case "/pause":
		b.svc.Pause()
		reason := args
		if reason == "" {
			reason = "без причины"
		}
		b.broadcast(ctx, "⏸ Приём заказов остановлен: "+html.EscapeString(reason)+". /resume — возобновить.", nil)
	case "/resume":
		b.svc.Resume()
		b.broadcast(ctx, "▶️ Приём заказов возобновлён.", nil)
	default:
		say("Не понял. /help — список команд.")
	}
}

func (b *Bot) find(ctx context.Context, num string, say func(string)) (*order.Order, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(num), "№"), 10, 64)
	if err != nil {
		say("Укажите номер заказа, например /order 12")
		return nil, false
	}
	o, err := b.svc.Store.Get(ctx, id)
	if err != nil {
		say(fmt.Sprintf("Заказ №%d не найден.", id))
		return nil, false
	}
	return o, true
}
