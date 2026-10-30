package profile

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func presets(t *testing.T) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "..", "..", "profiles", "*.yaml"))
	if err != nil || len(paths) < 6 {
		t.Fatalf("presets not found: %v %v", paths, err)
	}
	return paths
}

func TestPresetsAreValid(t *testing.T) {
	for _, path := range presets(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			p, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if p.Business.Name == "" || len(p.Workflow.Transitions) == 0 {
				t.Fatalf("empty profile: %+v", p.Business)
			}
		})
	}
}

const minimal = `
business: {name: Test}
catalog: [{id: a, title: A, price: 10}]
checkout:
  fields: [{id: email, label: Email, type: email, required: true}]
workflow:
  initial: new
  states: [{id: new, title: New}, {id: done, title: Done, final: true}]
  transitions: [{id: finish, title: Finish, from: new, to: done, by: operator}]
`

func TestDefaults(t *testing.T) {
	p, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if p.Pricing.Mode != PricingCatalog || p.Business.Currency != "RUB" || !p.AcceptsTransfer() || p.Payment.WindowHours != 24 {
		t.Fatalf("defaults not applied: %+v %+v", p.Pricing, p.Payment)
	}
	if p.Catalog[0].Price != 1000 {
		t.Fatalf("price %v", p.Catalog[0].Price)
	}
}

func TestValidationCatchesMistakes(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"unknown key":          {"business: {name: Test}", "business: {name: Test, colour: red}", "field colour not found"},
		"unknown target":       {"to: done, by: operator", "to: finished, by: operator", `to "finished" is not a state`},
		"unreachable state":    {"{id: done, title: Done, final: true}]", "{id: done, title: Done, final: true}, {id: lost, title: Lost}]", `state "lost" cannot be reached`},
		"bad actor":            {"by: operator", "by: robot", `actor "robot"`},
		"bad requirement":      {"by: operator}", "by: operator, requires: [magic]}", `unknown requirement "magic"`},
		"no email":             {"{id: email, label: Email, type: email, required: true}", "{id: name, label: Name, type: text}", "id email and type email"},
		"select without items": {"{id: email, label: Email, type: email, required: true}", "{id: email, label: Email, type: email, required: true}, {id: s, label: S, type: select}", "select needs options"},
		"final start":          {"from: new, to: done", "from: done, to: new", `starts in final state "done"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(minimal, c.from, c.to, 1)
			if src == minimal {
				t.Fatalf("case did not change the profile")
			}
			_, err := Parse([]byte(src))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error with %q, got %v", c.want, err)
			}
		})
	}
}

func TestStringList(t *testing.T) {
	p, err := Parse([]byte(strings.Replace(minimal, "by: operator", "by: [operator, customer]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if by := p.Workflow.Transitions[0].By; !by.Has(ActorCustomer) || !by.Has(ActorOperator) {
		t.Fatalf("by = %v", by)
	}
}

func TestServicePickMustExist(t *testing.T) {
	raw := `
business: {name: Мастер, currency: KGS}
pricing: {mode: quote}
checkout:
  fields:
    - {id: email, label: Email, type: email, required: true}
    - {id: work, label: Работа, type: select, required: true, options: [Мебель]}
services:
  - {title: Мебель, pick: work=Мебель}
  - {title: Окна, pick: work=Окна}
payment: {methods: [cash]}
workflow:
  initial: new
  states:
    - {id: new, title: Новая}
    - {id: done, title: Готово, final: true}
  transitions:
    - {id: finish, title: Готово, from: new, to: done, by: operator}
`
	_, err := Parse([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), `service "Окна"`) || strings.Contains(err.Error(), `service "Мебель"`) {
		t.Fatalf("want an error about Окна only, got %v", err)
	}
}
