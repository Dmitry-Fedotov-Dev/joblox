// Package order is the business core: it creates orders from the checkout form, prices them, runs the
// anti-fraud check, verifies the customer's email, moves orders along the profile's workflow and decides when
// the owner's payment requisites may be shown.
package order

import (
	"context"
	"errors"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/flow"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
)

// Order is one customer request. Fields holds the checkout answers by field id.
type Order struct {
	ID     int64
	Token  string // the secret part of the customer's order link
	State  string
	Lines  []Line
	Fees   []FeeLine
	Fields map[string]string
	Email  string
	Method string // payment method chosen by the customer
	Total  money.Amount
	Priced bool

	Paid     bool
	PaidAt   *time.Time
	PaidNote string
	// ReportedAt: the customer pressed «Я оплатил»; the operator still checks and confirms the payment.
	ReportedAt *time.Time

	VerifiedAt   *time.Time
	CodeHash     string
	CodeExpires  time.Time
	CodeSentAt   time.Time
	CodeSends    int
	CodeAttempts int

	Review      bool // held by the anti-fraud until the operator approves
	Approved    bool
	Risk        int
	RiskReasons []string
	ClientHash  string // keyed hash of the client IP: enough for velocity checks, not a log of addresses

	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int // optimistic lock: two operators pressing buttons at once cannot both win
}

type Line struct {
	ItemID string       `json:"item"`
	Title  string       `json:"title"`
	Qty    int          `json:"qty"`
	Price  money.Amount `json:"price"`
	Sum    money.Amount `json:"sum"`
}

type FeeLine struct {
	ID    string       `json:"id"`
	Title string       `json:"title"`
	Sum   money.Amount `json:"sum"`
}

// Flags turns the order's facts into what workflow guards check.
func (o *Order) Flags() flow.Flags {
	return flow.Flags{Paid: o.Paid, Priced: o.Priced, Verified: o.VerifiedAt != nil, Approved: o.Approved}
}

// Event is one line of the order's append-only journal.
type Event struct {
	ID      int64
	OrderID int64
	At      time.Time
	Actor   string // customer, operator, system
	ActorID string // operator's Telegram id; empty for customer and system
	Kind    string // created, transition, paid, approved, priced, code_sent, verified, blocked …
	From    string
	To      string
	Note    string
}

// Filter selects orders for lists.
type Filter struct {
	Open   bool // only orders in non-final states
	States []string
	Review bool // only orders waiting for anti-fraud approval
	Limit  int
}

// Counts are what the anti-fraud asks about the past.
type Counts struct {
	ByClientHour  int
	ByEmailDay    int
	OpenUnpaid    int
	PaidByEmail   int
	DuplicateHour int // same email and same total within an hour
}

// Stats feed the admin dashboard.
type Stats struct {
	Today      int
	Open       int
	Review     int
	PaidToday  int
	RevenueDay money.Amount
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("order changed meanwhile")
)

// Store keeps orders. The SQLite implementation lives in internal/store; another database only needs this.
type Store interface {
	Create(ctx context.Context, o *Order, ev Event) error
	Get(ctx context.Context, id int64) (*Order, error)
	ByToken(ctx context.Context, token string) (*Order, error)
	// Save writes the order if its Version still matches and appends the event, in one transaction.
	Save(ctx context.Context, o *Order, ev Event) error
	Events(ctx context.Context, orderID int64) ([]Event, error)
	List(ctx context.Context, f Filter, finals []string) ([]*Order, error)
	Counts(ctx context.Context, email, clientHash string, total money.Amount, finals []string, now time.Time) (Counts, error)
	Stale(ctx context.Context, states []string, before time.Time) ([]*Order, error)
	IsBlocked(ctx context.Context, values ...string) (bool, error)
	Block(ctx context.Context, value, note string) error
	Unblock(ctx context.Context, value string) error
	Stats(ctx context.Context, since time.Time, finals []string) (Stats, error)
	Close() error
}
