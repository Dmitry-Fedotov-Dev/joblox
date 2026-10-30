// Package profile describes a business: what it sells, what the customer fills in, how the price is made,
// how the order moves from state to state and who may move it. One YAML file is one business.
package profile

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
)

// Actors who may trigger a transition.
const (
	ActorCustomer = "customer"
	ActorOperator = "operator"
	ActorSystem   = "system"
)

// Guards a transition may require. The flags are set by operator actions outside the workflow
// (confirm payment, approve after review, name the price), so no profile can let a customer set them.
const (
	FlagPaid     = "paid"     // payment confirmed by the operator
	FlagPriced   = "priced"   // the order has a price (catalog total or the operator's quote)
	FlagVerified = "verified" // the customer confirmed the email with a code
	FlagApproved = "approved" // the operator cleared an order the anti-fraud sent to review
)

// Pricing modes.
const (
	PricingCatalog = "catalog" // the sum of the cart and the fees
	PricingQuote   = "quote"   // the operator names the price after reading the request
	// PricingShop is a shop with photos: products, sizes and stock live in the database and the owner edits
	// them in the browser (internal/shop); the YAML catalog stays empty.
	PricingShop = "shop"
)

// Payment methods.
const (
	MethodTransfer = "transfer" // bank transfer to the owner's requisites, confirmed by the operator
	MethodCash     = "cash"     // cash or card at pickup or delivery, marked by the operator
)

type Profile struct {
	Business  Business  `yaml:"business"`
	Catalog   []Item    `yaml:"catalog"`
	Pricing   Pricing   `yaml:"pricing"`
	Checkout  Checkout  `yaml:"checkout"`
	Payment   Payment   `yaml:"payment"`
	Workflow  Workflow  `yaml:"workflow"`
	Antifraud Antifraud `yaml:"antifraud"`
	Texts     Texts     `yaml:"texts"`
	// Services are the tiles above the request form of a performer who names the price (a master, a
	// courier): what they do and from what price. A tile opens the form with its choice already made.
	Services []Service `yaml:"services"`
}

// Service is one tile of the services showcase.
type Service struct {
	Title string `yaml:"title"`
	About string `yaml:"about"`
	Price string `yaml:"price"` // shown as is: «от 800 сом», «1 500 сом/час»
	Icon  string `yaml:"icon"`  // an emoji
	Pick  string `yaml:"pick"`  // field=value: the checkout choice the tile makes, e.g. work=Собрать мебель
}

type Business struct {
	Name        string `yaml:"name"`
	Kind        string `yaml:"kind"` // free text shown in the admin: shop, bakery, taxi …
	Description string `yaml:"description"`
	Currency    string `yaml:"currency"` // ISO code, shown next to sums
	Timezone    string `yaml:"timezone"`
	Contact     string `yaml:"contact"` // shown in the footer: phone, email or messenger
}

type Item struct {
	ID          string       `yaml:"id"`
	Title       string       `yaml:"title"`
	Description string       `yaml:"description"`
	Price       money.Amount `yaml:"price"`
	Unit        string       `yaml:"unit"`
	MaxQty      int          `yaml:"max_qty"`
	Hidden      bool         `yaml:"hidden"`
}

type Pricing struct {
	Mode     string       `yaml:"mode"`
	Fees     []Fee        `yaml:"fees"`
	MinTotal money.Amount `yaml:"min_total"`
	MaxTotal money.Amount `yaml:"max_total"`
}

// Fee is added to the cart: a fixed sum, or a rate per unit of a numeric checkout field (km, kg, hours).
type Fee struct {
	ID       string       `yaml:"id"`
	Title    string       `yaml:"title"`
	Amount   money.Amount `yaml:"amount"`
	PerField string       `yaml:"per_field"`
	Rate     money.Amount `yaml:"rate"`
	FreeFrom money.Amount `yaml:"free_from"` // the cart sum from which the fee is waived
	When     string       `yaml:"when"`      // field=value: the fee applies only then (delivery=courier)
}

type Checkout struct {
	Fields []Field `yaml:"fields"`
}

// Field types.
const (
	FieldText     = "text"
	FieldTextarea = "textarea"
	FieldPhone    = "phone"
	FieldEmail    = "email"
	FieldNumber   = "number"
	FieldDatetime = "datetime"
	FieldSelect   = "select"
	FieldURL      = "url"
)

type Field struct {
	ID       string   `yaml:"id"`
	Label    string   `yaml:"label"`
	Type     string   `yaml:"type"`
	Required bool     `yaml:"required"`
	Options  []string `yaml:"options"`
	Min      *float64 `yaml:"min"`
	Max      *float64 `yaml:"max"`
	Unit     string   `yaml:"unit"`
	MaxLen   int      `yaml:"max_len"`
	Hint     string   `yaml:"hint"`
}

type Payment struct {
	Methods          []string `yaml:"methods"`
	RequireEmailCode bool     `yaml:"require_email_code"`
	ReferencePrefix  string   `yaml:"reference_prefix"` // "JX" gives the payment reference JX-1042
	WindowHours      int      `yaml:"window_hours"`     // unpaid transfer orders expire after this
}

type Workflow struct {
	Initial     string       `yaml:"initial"`
	States      []State      `yaml:"states"`
	Transitions []Transition `yaml:"transitions"`
}

type State struct {
	ID       string `yaml:"id"`
	Title    string `yaml:"title"`    // for the operator
	Customer string `yaml:"customer"` // what the customer reads on the order page
	Final    bool   `yaml:"final"`
	// Restock puts the reserved items back on sale when an order arrives here (cancelled, expired).
	Restock bool `yaml:"restock"`
}

type Transition struct {
	ID       string     `yaml:"id"`
	Title    string     `yaml:"title"`
	From     StringList `yaml:"from"`
	To       string     `yaml:"to"`
	By       StringList `yaml:"by"`
	Requires StringList `yaml:"requires"`
	Notify   bool       `yaml:"notify"` // email the customer when it happens
}

type Antifraud struct {
	MaxOrdersPerIPHour     int          `yaml:"max_orders_per_ip_hour"`
	MaxOrdersPerEmailDay   int          `yaml:"max_orders_per_email_day"`
	MaxOpenUnpaidPerEmail  int          `yaml:"max_open_unpaid_per_email"`
	NewCustomerReviewAbove money.Amount `yaml:"new_customer_review_above"`
	ReviewAbove            money.Amount `yaml:"review_above"`
	MinFillSeconds         int          `yaml:"min_fill_seconds"`
	BlockDisposableEmail   bool         `yaml:"block_disposable_email"`
	ReviewAll              bool         `yaml:"review_all"`
}

type Texts struct {
	Intro       string `yaml:"intro"`
	PaymentNote string `yaml:"payment_note"`
	Rules       string `yaml:"rules"`
}

// StringList accepts both `from: new` and `from: [new, accepted]`.
type StringList []string

func (s *StringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = StringList{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*s = list
	return nil
}

func (s StringList) Has(v string) bool { return slices.Contains(s, v) }

// Load reads and validates a profile file.
func Load(path string) (*Profile, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the path comes from the operator's configuration
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse decodes a profile strictly (unknown keys are errors: a typo must not silently change the flow) and validates it.
func Parse(raw []byte) (*Profile, error) {
	var p Profile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	p.defaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Profile) defaults() {
	if p.Business.Currency == "" {
		p.Business.Currency = "RUB"
	}
	if p.Pricing.Mode == "" {
		p.Pricing.Mode = PricingCatalog
	}
	if len(p.Payment.Methods) == 0 {
		p.Payment.Methods = []string{MethodTransfer}
	}
	if p.Payment.ReferencePrefix == "" {
		p.Payment.ReferencePrefix = "JX"
	}
	if p.Payment.WindowHours == 0 {
		p.Payment.WindowHours = 24
	}
	a := &p.Antifraud
	if a.MaxOrdersPerIPHour == 0 {
		a.MaxOrdersPerIPHour = 10
	}
	if a.MaxOrdersPerEmailDay == 0 {
		a.MaxOrdersPerEmailDay = 10
	}
	if a.MaxOpenUnpaidPerEmail == 0 {
		a.MaxOpenUnpaidPerEmail = 3
	}
	if a.MinFillSeconds == 0 {
		a.MinFillSeconds = 3
	}
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Validate checks that the workflow is a closed graph and every reference points somewhere.
func (p *Profile) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if strings.TrimSpace(p.Business.Name) == "" {
		add("business.name is required")
	}
	switch p.Pricing.Mode {
	case PricingCatalog:
		if len(p.Catalog) == 0 {
			add("pricing.mode catalog needs at least one catalog item")
		}
	case PricingQuote:
	case PricingShop:
		if len(p.Catalog) > 0 {
			add("pricing.mode shop takes the products from the shop's database: leave catalog empty")
		}
	default:
		add("pricing.mode %q: want catalog, shop or quote", p.Pricing.Mode)
	}
	items := map[string]bool{}
	for _, it := range p.Catalog {
		if !idRe.MatchString(it.ID) {
			add("catalog item id %q: lowercase letters, digits and _ only", it.ID)
		}
		if items[it.ID] {
			add("catalog item %q is listed twice", it.ID)
		}
		items[it.ID] = true
		if it.Price <= 0 {
			add("catalog item %q needs a price above zero", it.ID)
		}
	}
	fields := map[string]Field{}
	hasEmail := false
	for _, f := range p.Checkout.Fields {
		if !idRe.MatchString(f.ID) {
			add("checkout field id %q: lowercase letters, digits and _ only", f.ID)
		}
		if _, dup := fields[f.ID]; dup {
			add("checkout field %q is listed twice", f.ID)
		}
		fields[f.ID] = f
		switch f.Type {
		case FieldText, FieldTextarea, FieldPhone, FieldEmail, FieldNumber, FieldDatetime, FieldURL:
		case FieldSelect:
			if len(f.Options) == 0 {
				add("checkout field %q: select needs options", f.ID)
			}
		default:
			add("checkout field %q: unknown type %q", f.ID, f.Type)
		}
		if f.Type == FieldEmail && f.ID == "email" {
			hasEmail = f.Required
		}
	}
	for i, sv := range p.Services {
		if strings.TrimSpace(sv.Title) == "" {
			add("services[%d]: title is required", i)
		}
		if sv.Pick == "" {
			continue
		}
		name, val, ok := strings.Cut(sv.Pick, "=")
		f, known := fields[name]
		if !ok || !known || f.Type == FieldSelect && !slices.Contains(f.Options, val) {
			add("service %q: pick %q must be field=value of a checkout field (one of the options for a select)", sv.Title, sv.Pick)
		}
	}
	if !hasEmail {
		add("checkout needs a required field with id email and type email: documents, codes and status go there")
	}
	for _, fee := range p.Pricing.Fees {
		if fee.PerField != "" {
			if f, ok := fields[fee.PerField]; !ok || f.Type != FieldNumber {
				add("fee %q: per_field %q must be a number field", fee.ID, fee.PerField)
			}
		}
		if fee.When != "" {
			name, _, ok := strings.Cut(fee.When, "=")
			if _, known := fields[name]; !ok || !known {
				add("fee %q: when %q must look like field=value with a known field", fee.ID, fee.When)
			}
		}
	}
	for _, m := range p.Payment.Methods {
		if m != MethodTransfer && m != MethodCash {
			add("payment method %q: want transfer or cash", m)
		}
	}

	states := map[string]State{}
	for _, s := range p.Workflow.States {
		if !idRe.MatchString(s.ID) {
			add("state id %q: lowercase letters, digits and _ only", s.ID)
		}
		if _, dup := states[s.ID]; dup {
			add("state %q is listed twice", s.ID)
		}
		states[s.ID] = s
	}
	if _, ok := states[p.Workflow.Initial]; !ok {
		add("workflow.initial %q is not a state", p.Workflow.Initial)
	}
	ids := map[string]bool{}
	reach := map[string]bool{p.Workflow.Initial: true}
	for _, t := range p.Workflow.Transitions {
		if !idRe.MatchString(t.ID) {
			add("transition id %q: lowercase letters, digits and _ only", t.ID)
		}
		if ids[t.ID] {
			add("transition %q is listed twice", t.ID)
		}
		ids[t.ID] = true
		if len(t.From) == 0 {
			add("transition %q has no from", t.ID)
		}
		for _, f := range t.From {
			if s, ok := states[f]; !ok {
				add("transition %q: from %q is not a state", t.ID, f)
			} else if s.Final {
				add("transition %q starts in final state %q", t.ID, f)
			}
		}
		if _, ok := states[t.To]; !ok {
			add("transition %q: to %q is not a state", t.ID, t.To)
		}
		if len(t.By) == 0 {
			add("transition %q: by is required (customer, operator or system)", t.ID)
		}
		for _, b := range t.By {
			if b != ActorCustomer && b != ActorOperator && b != ActorSystem {
				add("transition %q: actor %q", t.ID, b)
			}
		}
		for _, r := range t.Requires {
			if r != FlagPaid && r != FlagPriced && r != FlagVerified && r != FlagApproved {
				add("transition %q: unknown requirement %q", t.ID, r)
			}
		}
	}
	// reachability: a state nobody can get to is a typo in the graph
	for changed := true; changed; {
		changed = false
		for _, t := range p.Workflow.Transitions {
			for _, f := range t.From {
				if reach[f] && !reach[t.To] {
					reach[t.To] = true
					changed = true
				}
			}
		}
	}
	for id := range states {
		if !reach[id] {
			add("state %q cannot be reached from %q", id, p.Workflow.Initial)
		}
	}
	return errors.Join(errs...)
}

// State returns the state by id.
func (p *Profile) State(id string) (State, bool) {
	for _, s := range p.Workflow.States {
		if s.ID == id {
			return s, true
		}
	}
	return State{}, false
}

// Item returns a visible catalog item by id.
func (p *Profile) Item(id string) (Item, bool) {
	for _, it := range p.Catalog {
		if it.ID == id && !it.Hidden {
			return it, true
		}
	}
	return Item{}, false
}

// AcceptsTransfer reports whether customers may pay by bank transfer to the owner's requisites.
func (p *Profile) AcceptsTransfer() bool { return slices.Contains(p.Payment.Methods, MethodTransfer) }

// Display renders a stored checkout value for people: dates as 28.10.2026 10:00, the rest as is.
func (f Field) Display(v string) string {
	if f.Type == FieldDatetime {
		if t, err := time.Parse("2006-01-02T15:04", v); err == nil {
			return t.Format("02.01.2006 15:04")
		}
	}
	return v
}
