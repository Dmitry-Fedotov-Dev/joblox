// Package bot is the operators' Telegram bot: a card for every new order with the buttons the profile's
// workflow allows right now, payment confirmation, approval after the anti-fraud, quotes, the blocklist,
// pause. Only Telegram accounts listed in JOBLOX_OPERATORS are served.
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

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/antifraud"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

type Bot struct {
	api       *API
	svc       *order.Service
	operators map[int64]bool
	log       *slog.Logger
	// Join lets someone become an operator with the code from the cabinet's link (/start CODE); nil: the
	// operators are only those the server was started with.
	Join func(ctx context.Context, id int64, code string) bool
	opMu sync.RWMutex

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

func (b *Bot) isOperator(id int64) bool {
	b.opMu.RLock()
	defer b.opMu.RUnlock()
	return b.operators[id]
}

func (b *Bot) operatorIDs() []int64 {
	b.opMu.RLock()
	defer b.opMu.RUnlock()
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
	if b.svc.Req() != nil {
		req = fmt.Sprintf("получатель %s, отпечаток <code>%s</code>", html.EscapeString(b.svc.Req().Recipient), b.svc.Req().Fingerprint())
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
		if cmd, code, ok := strings.Cut(strings.TrimSpace(u.Message.Text), " "); ok && cmd == "/start" && b.Join != nil &&
			!b.isOperator(u.Message.From.ID) && b.Join(ctx, u.Message.From.ID, strings.TrimSpace(code)) {
			b.opMu.Lock()
			b.operators[u.Message.From.ID] = true
			b.opMu.Unlock()
			_ = b.api.SendMenu(ctx, u.Message.Chat.ID, "✅ Готово: новые заказы будут приходить сюда, с кнопками.\n\n"+
				"Меню внизу: активные заказы, оплаты на проверке, статистика, ссылки на сайт и админку.", menuRows)
			return
		}
		if !b.isOperator(u.Message.From.ID) {
			_, _ = b.api.Send(ctx, u.Message.Chat.ID, fmt.Sprintf("Этот бот только для операторов. Ваш id: <code>%d</code>.", u.Message.From.ID), nil)
			return
		}
		b.command(ctx, u.Message)
	case u.Callback != nil:
		if !b.isOperator(u.Callback.From.ID) {
			_ = b.api.Answer(ctx, u.Callback.ID, "Нет доступа")
			return
		}
		b.button(ctx, u.Callback)
	}
}

// ---------------------------------------------------------------- cards

// The operator's menu under the input field, as in the bot of Mama's workshop.
const (
	btnActive = "📋 Активные заказы"
	btnCheck  = "💸 Проверить оплату"
	btnStats  = "📊 Статистика"
	btnLinks  = "🔗 Сайт и админка"
)

var menuRows = [][]string{{btnActive, btnCheck}, {btnStats, btnLinks}}

// tone is what the order waits for, as an icon: the price or the money, a payment to check, work, the end.
func (b *Bot) tone(o *order.Order) string {
	st, _ := b.svc.P.State(o.State)
	switch {
	case st.Final && o.Paid:
		return "✅"
	case st.Final:
		return "✖️"
	case o.ReportedAt != nil && !o.Paid:
		return "💸"
	case o.Review && !o.Approved:
		return "⚠️"
	case !o.Priced || o.Method == profile.MethodTransfer && !o.Prepaid():
		return "⏳"
	}
	return "🛠"
}

// fieldIcon puts a familiar icon before the customer's answers that have one.
func fieldIcon(f profile.Field) string {
	switch {
	case f.Type == profile.FieldPhone:
		return "📞 "
	case f.Type == profile.FieldEmail:
		return "✉️ "
	case f.ID == "name" || f.ID == "company":
		return "👤 "
	case strings.Contains(f.ID, "address") || f.ID == "from" || f.ID == "to":
		return "📍 "
	case f.ID == "comment" || f.Type == profile.FieldTextarea:
		return "📝 "
	}
	return ""
}

// cancelling: a transition that ends the order without the work done asks «are you sure?» first.
func cancelling(p *profile.Profile, t profile.Transition) bool {
	to, _ := p.State(t.To)
	return to.Final && (strings.Contains(t.To, "cancel") || strings.Contains(t.To, "reject") || strings.Contains(t.ID, "cancel"))
}

func (b *Bot) card(o *order.Order) (string, *Keyboard) {
	p := b.svc.P
	st, _ := p.State(o.State)
	cur := p.Business.Currency
	var s strings.Builder
	fmt.Fprintf(&s, "<b>Заказ №%d</b> · %s %s\n<i>%s</i>\n\n", o.ID, b.tone(o), html.EscapeString(st.Title),
		o.CreatedAt.Format("02.01 15:04 UTC"))
	for _, l := range o.Lines {
		qty := ""
		if l.Qty > 1 {
			qty = fmt.Sprintf(" × %d", l.Qty)
		}
		fmt.Fprintf(&s, "• %s%s · %s\n", html.EscapeString(l.Title), qty, l.Sum)
	}
	for _, f := range o.Fees {
		fmt.Fprintf(&s, "• %s · %s\n", html.EscapeString(f.Title), f.Sum)
	}
	if o.Priced {
		fmt.Fprintf(&s, "💰 <b>Итого: %s %s</b>\n", o.Total, cur)
	} else {
		fmt.Fprintf(&s, "💰 <b>Цена не названа:</b> /price %d сумма\n", o.ID)
	}
	if o.Split() {
		got := "ждём"
		if o.AdvanceAt != nil {
			got = "✓ получен"
		}
		fmt.Fprintf(&s, "💵 Аванс %d%%: %s %s, %s\n", o.Prepay, o.Advance(), cur, got)
	}
	pay := "перевод"
	if o.Method == profile.MethodCash {
		pay = "при получении"
	}
	switch {
	case o.Paid:
		pay += ", ✅ оплачен"
		if o.PaidNote != "" {
			pay += " (" + html.EscapeString(o.PaidNote) + ")"
		}
	case o.ReportedAt != nil:
		pay += ", 💸 покупатель сообщил, что оплатил: проверьте поступление"
	case o.Method == profile.MethodTransfer && o.Priced:
		pay += ", ⏳ ждём оплату"
	}
	fmt.Fprintf(&s, "💳 %s · назначение <code>%s</code>\n\n", pay, html.EscapeString(b.svc.Reference(o)))
	for _, f := range p.Checkout.Fields {
		if v := o.Fields[f.ID]; v != "" {
			icon := fieldIcon(f)
			if icon == "" {
				fmt.Fprintf(&s, "%s: %s%s\n", html.EscapeString(f.Label), html.EscapeString(f.Display(v)), unit(f.Unit))
			} else {
				fmt.Fprintf(&s, "%s%s%s\n", icon, html.EscapeString(f.Display(v)), unit(f.Unit))
			}
		}
	}
	switch {
	case o.Review && !o.Approved:
		fmt.Fprintf(&s, "\n⚠️ <b>На проверке</b>, риск %d: %s\n", o.Risk, html.EscapeString(strings.Join(o.RiskReasons, "; ")))
	case len(o.RiskReasons) > 0:
		fmt.Fprintf(&s, "\nПроверка: риск %d (%s)\n", o.Risk, html.EscapeString(strings.Join(o.RiskReasons, "; ")))
	}
	blocked := b.svc.Flow.Blocked(o.State, profile.ActorOperator, o.Flags())
	for _, t := range p.Workflow.Transitions { // profile order, not map order
		if need, ok := blocked[t.ID]; ok {
			fmt.Fprintf(&s, "«%s» ждёт: %s\n", html.EscapeString(t.Title), needText(need))
		}
	}
	fmt.Fprintf(&s, "\n<a href=\"%s/admin/orders/%d\">Открыть в админке</a>", html.EscapeString(b.svc.BaseURL()), o.ID)

	kb := &Keyboard{}
	final := b.svc.IsFinal(o.State)
	add := func(btns ...Button) { kb.Rows = append(kb.Rows, btns) }
	if o.Review && !o.Approved && !final {
		add(Button{Text: "✅ Одобрить", Data: fmt.Sprintf("a:%d", o.ID)})
	}
	reported := o.ReportedAt != nil && !o.Paid && !final
	if o.Split() && o.AdvanceAt == nil && !o.Paid && !final && o.Priced {
		title := fmt.Sprintf("💵 Аванс %d%% пришёл", o.Prepay)
		if reported {
			title = fmt.Sprintf("✅ Подтвердить аванс %d%%", o.Prepay)
		}
		add(Button{Text: title, Data: fmt.Sprintf("v:%d", o.ID)})
	}
	if !o.Paid && !final && o.Priced {
		title := "✅ Оплата пришла"
		if waitAdvance := o.Split() && o.AdvanceAt == nil; reported && !waitAdvance {
			title = "✅ Подтвердить оплату"
		}
		add(Button{Text: title, Data: fmt.Sprintf("p:%d", o.ID)})
	}
	if reported {
		add(Button{Text: "↩️ Оплата не найдена", Data: fmt.Sprintf("n:%d", o.ID)})
	}
	var row, cancel []Button
	for _, t := range b.svc.Flow.Open(o.State, profile.ActorOperator, o.Flags()) {
		if cancelling(p, t) {
			cancel = append(cancel, Button{Text: "✖️ " + t.Title, Data: fmt.Sprintf("x:%d:%s", o.ID, t.ID)})
			continue
		}
		row = append(row, Button{Text: "→ " + t.Title, Data: fmt.Sprintf("t:%d:%s", o.ID, t.ID)})
		if len(row) == 2 {
			kb.Rows, row = append(kb.Rows, row), nil
		}
	}
	if len(row) > 0 {
		kb.Rows = append(kb.Rows, row)
	}
	add(append(cancel, Button{Text: "🔄 Обновить", Data: fmt.Sprintf("r:%d", o.ID)})...)
	return s.String(), kb
}

// list is the orders as buttons: a tap opens the order's card.
func (b *Bot) list(ctx context.Context, chat int64, head, empty string, orders []*order.Order) {
	if len(orders) == 0 {
		_, _ = b.api.Send(ctx, chat, empty, nil)
		return
	}
	kb := &Keyboard{}
	for i, o := range orders {
		if i == 40 {
			break
		}
		who := []rune(o.Fields["name"])
		if len(who) > 18 {
			who = who[:18]
		}
		sum := "цена?"
		if o.Priced {
			sum = o.Total.String()
		}
		kb.Rows = append(kb.Rows, []Button{{Text: fmt.Sprintf("%s №%d · %s · %s", b.tone(o), o.ID, string(who), sum),
			Data: fmt.Sprintf("o:%d", o.ID)}})
	}
	_, _ = b.api.Send(ctx, chat, head, kb)
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
	case "reported":
		note = "💸 Клиент сообщил, что оплатил: проверьте поступление и нажмите «Оплата получена»"
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
	case "o": // from a list: the card comes as a new message, the list stays
		text, kb := b.card(o)
		_ = b.api.Answer(ctx, cq.ID, "")
		_, _ = b.api.Send(ctx, cq.Message.Chat.ID, text, kb)
		return
	case "r":
		done = "Обновлено"
	case "n":
		err, done = b.svc.RejectReport(ctx, o, op), "Покупатель снова увидит «Я оплатил»"
	case "x": // a cancel asks first
		if len(parts) != 3 {
			err = errors.New("bad button")
			break
		}
		kb := &Keyboard{Rows: [][]Button{{{Text: "Да, " + strings.ToLower(transitionTitle(b.svc.P, parts[2])), Data: fmt.Sprintf("t:%d:%s", o.ID, parts[2])},
			{Text: "Нет", Data: fmt.Sprintf("r:%d", o.ID)}}}}
		_ = b.api.Answer(ctx, cq.ID, "Точно?")
		text, _ := b.card(o)
		if eerr := b.api.Edit(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, kb); eerr != nil {
			b.log.Debug("edit card", "err", eerr)
		}
		return
	case "a":
		err, done = b.svc.Approve(ctx, o, op), "Одобрено"
	case "p":
		err, done = b.svc.ConfirmPayment(ctx, o, op, "отмечено в боте"), "Оплата отмечена"
	case "v":
		err, done = b.svc.ConfirmAdvance(ctx, o, op, "отмечено в боте"), "Аванс отмечен"
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
/orders: открытые заказы
/review: заказы на проверке
/order N: карточка заказа
/price N 1500: назвать цену заказа по запросу
/paid N [комментарий]: отметить оплату
/block email или телефон: в чёрный список, /unblock убирает
/stats: заказы и выручка за сутки
/pause [причина]: остановить приём заказов, /resume возобновляет`

func (b *Bot) command(ctx context.Context, m *Message) {
	switch strings.TrimSpace(m.Text) {
	case btnActive:
		m.Text = "/orders"
	case btnCheck:
		m.Text = "/check"
	case btnStats:
		m.Text = "/stats"
	case btnLinks:
		m.Text = "/links"
	}
	cmd, args, _ := strings.Cut(strings.TrimSpace(m.Text), " ")
	cmd = strings.SplitN(cmd, "@", 2)[0]
	args = strings.TrimSpace(args)
	op := strconv.FormatInt(m.From.ID, 10)
	say := func(text string) { _, _ = b.api.Send(ctx, m.Chat.ID, text, nil) }
	switch cmd {
	case "/start":
		_ = b.api.SendMenu(ctx, m.Chat.ID, fmt.Sprintf("Привет! Я присылаю сюда новые заказы «%s».\n\n"+
			"Кнопками под заказом отмечайте оплату и статус. Меню внизу: заказы, оплаты на проверке, статистика.",
			html.EscapeString(b.svc.P.Business.Name)), menuRows)
	case "/help":
		say(html.EscapeString(help))
	case "/links":
		base := html.EscapeString(b.svc.BaseURL())
		say(fmt.Sprintf("🛍 <a href=\"%s/\">Сайт</a>\n🔐 <a href=\"%s/admin\">Админка</a>: заказы, товары, настройки "+
			"(откроется, если вы уже входили по мастер-ссылке)", base, base))
	case "/check":
		list, err := b.svc.Store.List(ctx, order.Filter{Open: true, Limit: 200}, b.svc.Finals())
		if err != nil {
			say("Ошибка: " + html.EscapeString(err.Error()))
			return
		}
		var due []*order.Order
		for _, o := range list {
			if o.ReportedAt != nil && !o.Paid {
				due = append(due, o)
			}
		}
		b.list(ctx, m.Chat.ID, "Покупатели сообщили об оплате, проверьте поступления:", "Нет оплат на проверке.", due)
	case "/orders", "/review":
		list, err := b.svc.Store.List(ctx, order.Filter{Open: true, Review: cmd == "/review", Limit: 40}, b.svc.Finals())
		if err != nil {
			say("Ошибка: " + html.EscapeString(err.Error()))
			return
		}
		head, empty := fmt.Sprintf("Активные заказы: %d", len(list)), "Активных заказов нет."
		if cmd == "/review" {
			head, empty = "Ждут вашей проверки:", "Заказов на проверке нет."
		}
		b.list(ctx, m.Chat.ID, head, empty, list)
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
		now := b.svc.Now()
		day, err := b.svc.Store.Stats(ctx, now.Add(-24*time.Hour), b.svc.Finals())
		if err != nil {
			say("Ошибка: " + html.EscapeString(err.Error()))
			return
		}
		month, _ := b.svc.Store.Stats(ctx, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), b.svc.Finals())
		cur := b.svc.P.Business.Currency
		say(fmt.Sprintf("<b>📊 Статистика</b>\n\nВ работе: <b>%d</b>, на проверке: %d\n\nЗа сутки: %d %s, оплачено %d на %s %s\n"+
			"За этот месяц: %d %s, оплачено на <b>%s %s</b>", day.Open, day.Review,
			day.Today, plural(day.Today, "заказ", "заказа", "заказов"), day.PaidToday, day.RevenueDay, cur,
			month.Today, plural(month.Today, "заказ", "заказа", "заказов"), month.RevenueDay, cur))
	case "/pause":
		b.svc.Pause()
		reason := args
		if reason == "" {
			reason = "без причины"
		}
		b.broadcast(ctx, "⏸ Приём заказов остановлен: "+html.EscapeString(reason)+". Возобновить: /resume.", nil)
	case "/resume":
		b.svc.Resume()
		b.broadcast(ctx, "▶️ Приём заказов возобновлён.", nil)
	default:
		say("Не понял. Список команд: /help.")
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

func plural(n int, one, few, many string) string {
	n %= 100
	switch {
	case n%10 == 1 && n != 11:
		return one
	case n%10 >= 2 && n%10 <= 4 && (n < 12 || n > 14):
		return few
	}
	return many
}
