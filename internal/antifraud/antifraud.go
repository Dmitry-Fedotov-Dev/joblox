// Package antifraud decides what happens to a new order before anyone sees the owner's requisites:
// let it through, hold it for the operator, or refuse it. The rules are plain and explainable: every
// decision carries the reasons the operator reads in the bot.
package antifraud

import (
	"strings"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

type Action string

const (
	Allow  Action = "allow"
	Review Action = "review" // the order exists, requisites stay hidden until the operator approves
	Block  Action = "block"  // no order is created
)

// Input is everything the check knows about the attempt.
type Input struct {
	Email          string
	Total          money.Amount
	Priced         bool // false for quote orders: the sum is unknown yet
	FillSeconds    float64
	HoneypotFilled bool
	Blocked        bool // the email, phone or client is on the blocklist
	// from the store
	ByClientHour  int
	ByEmailDay    int
	OpenUnpaid    int
	PaidBefore    int // paid orders with this email: a known customer
	DuplicateHour int
}

type Decision struct {
	Action  Action
	Score   int
	Reasons []string
}

// Check applies the profile's limits. Hard signals block, soft ones add up to a review.
func Check(cfg profile.Antifraud, in Input) Decision {
	d := Decision{Action: Allow}
	hard := func(reason string) { d.Action, d.Reasons = Block, append(d.Reasons, reason) }
	soft := func(points int, reason string) { d.Score += points; d.Reasons = append(d.Reasons, reason) }

	if in.Blocked {
		hard("в чёрном списке")
	}
	if in.HoneypotFilled {
		hard("заполнено скрытое поле (бот)")
	}
	if in.ByClientHour >= cfg.MaxOrdersPerIPHour {
		hard("слишком много заказов с одного адреса за час")
	}
	if in.ByEmailDay >= cfg.MaxOrdersPerEmailDay {
		hard("слишком много заказов на один email за сутки")
	}
	if in.OpenUnpaid >= cfg.MaxOpenUnpaidPerEmail {
		hard("уже есть неоплаченные заказы на этот email")
	}
	if cfg.BlockDisposableEmail && Disposable(in.Email) {
		hard("одноразовый почтовый ящик")
	}
	if d.Action == Block {
		return d
	}

	if in.FillSeconds < float64(cfg.MinFillSeconds) {
		soft(40, "форма заполнена слишком быстро")
	}
	if in.DuplicateHour > 0 {
		soft(30, "такой же заказ уже был в течение часа")
	}
	newCustomer := in.PaidBefore == 0
	if newCustomer && in.Priced && cfg.NewCustomerReviewAbove > 0 && in.Total > cfg.NewCustomerReviewAbove {
		soft(50, "новый клиент, сумма выше порога")
	}
	if in.Priced && cfg.ReviewAbove > 0 && in.Total > cfg.ReviewAbove {
		soft(50, "сумма выше порога ручной проверки")
	}
	if !in.Priced {
		// a quote order is read by the operator anyway: naming the price is the approval
		soft(0, "цена по запросу")
	}
	if cfg.ReviewAll {
		soft(50, "ручная проверка всех заказов")
	}
	if Disposable(in.Email) {
		soft(30, "одноразовый почтовый ящик")
	}
	if d.Score >= 30 || !in.Priced {
		d.Action = Review
	}
	return d
}

// Disposable reports throwaway mailboxes. The list is short on purpose: the big services people really use
// are never on it, and the operator can block any other domain from the bot.
func Disposable(email string) bool {
	_, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	if !ok {
		return false
	}
	_, hit := disposable[domain]
	return hit
}

var disposable = map[string]struct{}{
	"mailinator.com": {}, "guerrillamail.com": {}, "guerrillamail.info": {}, "sharklasers.com": {},
	"10minutemail.com": {}, "10minutemail.net": {}, "temp-mail.org": {}, "tempmail.com": {}, "tempmail.net": {},
	"yopmail.com": {}, "yopmail.net": {}, "trashmail.com": {}, "dispostable.com": {}, "getnada.com": {},
	"maildrop.cc": {}, "mintemail.com": {}, "throwawaymail.com": {}, "fakeinbox.com": {}, "mohmal.com": {},
	"emailondeck.com": {}, "tempail.com": {}, "dropmail.me": {}, "mail.tm": {}, "1secmail.com": {},
}
