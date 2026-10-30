package web

import (
	"strings"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// Step is one point of the order's progress line on its page.
type Step struct {
	Title string
	Done  bool // passed
	Now   bool // the order is here
}

// happyPath is the workflow's way from the start to a good end: from the initial state it follows the first
// transition that is not a cancellation or an expiry. Profiles name those cancel… and expire….
func happyPath(p *profile.Profile) []profile.State {
	var path []profile.State
	seen := map[string]bool{}
	id := p.Workflow.Initial
	for id != "" && !seen[id] {
		seen[id] = true
		st, ok := p.State(id)
		if !ok {
			break
		}
		path = append(path, st)
		next := ""
		for _, t := range p.Workflow.Transitions {
			if strings.HasPrefix(t.ID, "cancel") || strings.HasPrefix(t.ID, "expire") {
				continue
			}
			for _, from := range t.From {
				if from == id && !seen[t.To] && next == "" {
					next = t.To
				}
			}
		}
		id = next
	}
	return path
}

// track is the progress line of an order: the happy path, with a «paid» step after the first one when the
// customer pays by transfer. An order that left the path (cancelled, expired) gets no line.
func track(p *profile.Profile, o *order.Order) []Step {
	path := happyPath(p)
	at := -1
	for i, st := range path {
		if st.ID == o.State {
			at = i
		}
	}
	if at < 0 {
		return nil
	}
	var steps []Step
	for i, st := range path {
		title := st.Title
		if i == 0 {
			title = "Оформлен"
		}
		steps = append(steps, Step{Title: title, Done: i < at, Now: i == at && (i != 0 || !needsPayment(o))})
		if i == 0 && needsPayment(o) {
			steps = append(steps, Step{Title: "Оплачен", Done: o.Paid || at > 0, Now: !o.Paid && at == 0})
			steps[0].Done = true
		}
	}
	if steps[len(steps)-1].Now {
		steps[len(steps)-1].Done = true // the good end is reached
	}
	return steps
}

func needsPayment(o *order.Order) bool { return o.Method == profile.MethodTransfer && o.Priced }
