// Package money keeps amounts in minor units (kopecks, cents) so that sums never lose precision.
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Amount is a sum in minor units of the business currency.
type Amount int64

// Parse reads "1250", "1250.5", "1 250,50" or "1250.50" into minor units.
func Parse(s string) (Amount, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.Replace(s, ",", ".", 1)
	if s == "" {
		return 0, errors.New("empty amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || len(frac) > 2 || strings.ContainsAny(frac, ".-+") {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	if w > 1_000_000_000_000 {
		return 0, fmt.Errorf("amount %q is too large", s)
	}
	a := Amount(w*100 + f)
	if neg {
		a = -a
	}
	return a, nil
}

// String renders the amount with two decimals and a thin space between thousands: "1 250.00".
func (a Amount) String() string {
	sign := ""
	if a < 0 {
		sign, a = "-", -a
	}
	whole := strconv.FormatInt(int64(a)/100, 10)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%s%s.%02d", sign, b.String(), int64(a)%100)
}

// Plain renders the amount without separators, for payment purposes and QR payloads: "1250.00".
func (a Amount) Plain() string {
	sign := ""
	if a < 0 {
		sign, a = "-", -a
	}
	return fmt.Sprintf("%s%d.%02d", sign, int64(a)/100, int64(a)%100)
}

// MulQty multiplies a price by a whole quantity.
func (a Amount) MulQty(q int) Amount { return a * Amount(q) }

// MulRate multiplies a per-unit rate by a measured quantity (kilometres, kilograms), rounding half up.
func (a Amount) MulRate(units float64) Amount {
	v := float64(a) * units
	if v < 0 {
		return Amount(v - 0.5)
	}
	return Amount(v + 0.5)
}

// UnmarshalYAML accepts both numbers (89, 89.5) and strings ("89.50") in profiles.
func (a *Amount) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	v, err := Parse(s)
	if err != nil {
		return err
	}
	*a = v
	return nil
}
