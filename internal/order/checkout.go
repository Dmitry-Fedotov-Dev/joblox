package order

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// FieldError is a checkout mistake the customer can fix; Field is the input id ("" for the cart).
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

// FieldErrors collects every mistake of one submit, so the form shows them all at once.
type FieldErrors []*FieldError

func (e FieldErrors) Error() string {
	parts := make([]string, len(e))
	for i, f := range e {
		parts[i] = f.Error()
	}
	return strings.Join(parts, "; ")
}

var phoneRe = regexp.MustCompile(`^\+?[0-9][0-9 ()\-]{5,22}$`)

const datetimeLayout = "2006-01-02T15:04" // what <input type="datetime-local"> sends

func clean(s string) string {
	s = strings.TrimSpace(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// ValidateFields checks the answers against the profile and returns the cleaned values.
func ValidateFields(p *profile.Profile, in map[string]string) (map[string]string, FieldErrors) {
	out := map[string]string{}
	var errs FieldErrors
	bad := func(id, msg string) { errs = append(errs, &FieldError{Field: id, Msg: msg}) }
	for _, f := range p.Checkout.Fields {
		v := clean(in[f.ID])
		if v == "" {
			if f.Required {
				bad(f.ID, "обязательное поле")
			}
			continue
		}
		limit := f.MaxLen
		if limit == 0 {
			limit = 200
			if f.Type == profile.FieldTextarea {
				limit = 1000
			}
		}
		if len([]rune(v)) > limit {
			bad(f.ID, fmt.Sprintf("не длиннее %d символов", limit))
			continue
		}
		switch f.Type {
		case profile.FieldEmail:
			addr, err := mail.ParseAddress(v)
			if err != nil || addr.Address != v || !strings.Contains(v[strings.LastIndex(v, "@"):], ".") {
				bad(f.ID, "проверьте адрес почты")
				continue
			}
			v = strings.ToLower(v)
		case profile.FieldPhone:
			if !phoneRe.MatchString(v) {
				bad(f.ID, "проверьте номер телефона")
				continue
			}
		case profile.FieldNumber:
			n, err := strconv.ParseFloat(strings.Replace(v, ",", ".", 1), 64)
			if err != nil {
				bad(f.ID, "нужно число")
				continue
			}
			if f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
				bad(f.ID, fmt.Sprintf("от %s до %s", fnum(f.Min), fnum(f.Max)))
				continue
			}
			v = strconv.FormatFloat(n, 'f', -1, 64)
		case profile.FieldDatetime:
			if _, err := time.Parse(datetimeLayout, v); err != nil {
				bad(f.ID, "выберите дату и время")
				continue
			}
		case profile.FieldSelect:
			if !slices.Contains(f.Options, v) {
				bad(f.ID, "выберите вариант из списка")
				continue
			}
		case profile.FieldURL:
			u, err := url.Parse(v)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				bad(f.ID, "нужна ссылка, начинающаяся с https://")
				continue
			}
		}
		out[f.ID] = v
	}
	return out, errs
}

func fnum(v *float64) string {
	if v == nil {
		return "…"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

// Price builds the lines and fees and the total. Quote profiles return an unpriced order.
func Price(p *profile.Profile, items map[string]int, fields map[string]string) ([]Line, []FeeLine, money.Amount, bool, FieldErrors) {
	if p.Pricing.Mode == profile.PricingQuote {
		return nil, nil, 0, false, nil
	}
	var errs FieldErrors
	var lines []Line
	var sum money.Amount
	for _, it := range p.Catalog {
		q := items[it.ID]
		if q == 0 || it.Hidden {
			continue
		}
		maxQ := it.MaxQty
		if maxQ == 0 {
			maxQ = 99
		}
		if q < 0 || q > maxQ {
			errs = append(errs, &FieldError{Field: "item_" + it.ID, Msg: fmt.Sprintf("от 1 до %d", maxQ)})
			continue
		}
		l := Line{ItemID: it.ID, Title: it.Title, Qty: q, Price: it.Price, Sum: it.Price.MulQty(q)}
		lines = append(lines, l)
		sum += l.Sum
	}
	for id := range items {
		if _, ok := p.Item(id); !ok {
			errs = append(errs, &FieldError{Field: "item_" + id, Msg: "такой позиции нет"})
		}
	}
	if len(lines) == 0 && len(errs) == 0 {
		errs = append(errs, &FieldError{Msg: "выберите хотя бы одну позицию"})
	}
	if len(errs) > 0 {
		return nil, nil, 0, false, errs
	}
	cart := sum
	var fees []FeeLine
	for _, f := range p.Pricing.Fees {
		if f.When != "" {
			name, want, _ := strings.Cut(f.When, "=")
			if fields[name] != want {
				continue
			}
		}
		if f.FreeFrom > 0 && cart >= f.FreeFrom {
			continue
		}
		var v money.Amount
		if f.PerField != "" {
			n, err := strconv.ParseFloat(fields[f.PerField], 64)
			if err != nil {
				continue // the field is optional and empty: nothing to charge
			}
			v = f.Rate.MulRate(n)
		} else {
			v = f.Amount
		}
		if v > 0 {
			fees = append(fees, FeeLine{ID: f.ID, Title: f.Title, Sum: v})
			sum += v
		}
	}
	if p.Pricing.MinTotal > 0 && sum < p.Pricing.MinTotal {
		errs = append(errs, &FieldError{Msg: "минимальная сумма заказа " + p.Pricing.MinTotal.String() + " " + p.Business.Currency})
	}
	if p.Pricing.MaxTotal > 0 && sum > p.Pricing.MaxTotal {
		errs = append(errs, &FieldError{Msg: "сумма заказа больше " + p.Pricing.MaxTotal.String() + " " + p.Business.Currency})
	}
	return lines, fees, sum, true, errs
}
