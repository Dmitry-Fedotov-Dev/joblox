// Package flow moves an order through the workflow of its profile. It knows nothing about storage or HTTP:
// given the order's state and flags and who is acting, it says which transitions are open and applies one.
package flow

import (
	"errors"
	"fmt"

	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/profile"
)

// Flags are the facts about an order that transitions may require.
type Flags struct {
	Paid     bool
	Priced   bool
	Verified bool
	Approved bool // not held for anti-fraud review, or cleared by the operator
}

func (f Flags) has(flag string) bool {
	switch flag {
	case profile.FlagPaid:
		return f.Paid
	case profile.FlagPriced:
		return f.Priced
	case profile.FlagVerified:
		return f.Verified
	case profile.FlagApproved:
		return f.Approved
	}
	return false
}

var (
	ErrUnknownTransition = errors.New("unknown transition")
	ErrNotAllowed        = errors.New("transition not allowed")
)

// Engine wraps one workflow.
type Engine struct {
	wf profile.Workflow
}

func New(wf profile.Workflow) *Engine { return &Engine{wf: wf} }

// Initial is the state of a new order.
func (e *Engine) Initial() string { return e.wf.Initial }

// Open lists the transitions the actor may take from the state now, in profile order.
func (e *Engine) Open(state, actor string, f Flags) []profile.Transition {
	var out []profile.Transition
	for _, t := range e.wf.Transitions {
		if e.allowed(t, state, actor, f) == nil {
			out = append(out, t)
		}
	}
	return out
}

// Blocked lists the transitions the actor could take from the state if their requirements were met,
// with the first missing requirement: the operator sees "Выдать (ждёт оплаты)" instead of a missing button.
func (e *Engine) Blocked(state, actor string, f Flags) map[string]string {
	out := map[string]string{}
	for _, t := range e.wf.Transitions {
		if !t.From.Has(state) || !t.By.Has(actor) {
			continue
		}
		for _, r := range t.Requires {
			if !f.has(r) {
				out[t.ID] = r
				break
			}
		}
	}
	return out
}

// Apply checks the transition and returns it; the caller stores the new state and the flags it sets.
func (e *Engine) Apply(id, state, actor string, f Flags) (profile.Transition, error) {
	for _, t := range e.wf.Transitions {
		if t.ID == id {
			return t, e.allowed(t, state, actor, f)
		}
	}
	return profile.Transition{}, fmt.Errorf("%w: %s", ErrUnknownTransition, id)
}

func (e *Engine) allowed(t profile.Transition, state, actor string, f Flags) error {
	if !t.From.Has(state) {
		return fmt.Errorf("%w: %s is not possible from %s", ErrNotAllowed, t.ID, state)
	}
	if !t.By.Has(actor) {
		return fmt.Errorf("%w: %s cannot do %s", ErrNotAllowed, actor, t.ID)
	}
	for _, r := range t.Requires {
		if !f.has(r) {
			return fmt.Errorf("%w: %s needs %s", ErrNotAllowed, t.ID, r)
		}
	}
	return nil
}

// Expiring finds a system transition out of the state into a final one: the sweeper uses it to close
// unpaid orders after the payment window.
func (e *Engine) Expiring(state string, finals map[string]bool) (profile.Transition, bool) {
	for _, t := range e.wf.Transitions {
		if t.From.Has(state) && t.By.Has(profile.ActorSystem) && finals[t.To] && len(t.Requires) == 0 {
			return t, true
		}
	}
	return profile.Transition{}, false
}
