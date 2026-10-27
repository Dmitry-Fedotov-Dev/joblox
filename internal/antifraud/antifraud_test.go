package antifraud

import (
	"testing"

	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/profile"
)

var cfg = profile.Antifraud{
	MaxOrdersPerIPHour: 5, MaxOrdersPerEmailDay: 5, MaxOpenUnpaidPerEmail: 2,
	NewCustomerReviewAbove: 300000, ReviewAbove: 1000000, MinFillSeconds: 3, BlockDisposableEmail: true,
}

func good() Input {
	return Input{Email: "anna@example.ru", Total: 50000, Priced: true, FillSeconds: 40}
}

func TestAllow(t *testing.T) {
	if d := Check(cfg, good()); d.Action != Allow || d.Score != 0 {
		t.Fatalf("good order: %+v", d)
	}
}

func TestBlock(t *testing.T) {
	cases := map[string]func(*Input){
		"blocklist":   func(in *Input) { in.Blocked = true },
		"honeypot":    func(in *Input) { in.HoneypotFilled = true },
		"ip velocity": func(in *Input) { in.ByClientHour = 5 },
		"email daily": func(in *Input) { in.ByEmailDay = 5 },
		"open unpaid": func(in *Input) { in.OpenUnpaid = 2 },
		"disposable":  func(in *Input) { in.Email = "x@Mailinator.com" },
	}
	for name, mut := range cases {
		in := good()
		mut(&in)
		if d := Check(cfg, in); d.Action != Block || len(d.Reasons) == 0 {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

func TestReview(t *testing.T) {
	cases := map[string]func(*Input){
		"too fast":      func(in *Input) { in.FillSeconds = 1 },
		"duplicate":     func(in *Input) { in.DuplicateHour = 1 },
		"new and large": func(in *Input) { in.Total = 400000 },
		"very large":    func(in *Input) { in.Total = 2000000; in.PaidBefore = 3 },
		"quote":         func(in *Input) { in.Priced = false; in.Total = 0 },
	}
	for name, mut := range cases {
		in := good()
		mut(&in)
		if d := Check(cfg, in); d.Action != Review {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

func TestKnownCustomerSkipsNewCustomerRule(t *testing.T) {
	in := good()
	in.Total, in.PaidBefore = 400000, 1
	if d := Check(cfg, in); d.Action != Allow {
		t.Fatalf("known customer: %+v", d)
	}
}

func TestReviewAll(t *testing.T) {
	c := cfg
	c.ReviewAll = true
	if d := Check(c, good()); d.Action != Review {
		t.Fatalf("review all: %+v", d)
	}
}

func TestDisposableSoftWhenNotBlocking(t *testing.T) {
	c := cfg
	c.BlockDisposableEmail = false
	in := good()
	in.Email = "x@yopmail.com"
	if d := Check(c, in); d.Action != Review {
		t.Fatalf("disposable soft: %+v", d)
	}
}
