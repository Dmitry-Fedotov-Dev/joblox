package flow

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/profile"
)

func bakery(t *testing.T) *profile.Profile {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := profile.Load(filepath.Join(filepath.Dir(file), "..", "..", "profiles", "bakery.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ids(ts []profile.Transition) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func TestOpenDependsOnActorAndFlags(t *testing.T) {
	e := New(bakery(t).Workflow)
	if got := ids(e.Open("new", profile.ActorOperator, Flags{})); len(got) != 1 || got[0] != "cancel" {
		t.Fatalf("operator before approval: %v", got)
	}
	if got := ids(e.Open("new", profile.ActorOperator, Flags{Approved: true})); len(got) != 2 || got[0] != "accept" {
		t.Fatalf("operator after approval: %v", got)
	}
	if got := ids(e.Open("new", profile.ActorCustomer, Flags{Approved: true})); len(got) != 1 || got[0] != "cancel" {
		t.Fatalf("customer: %v", got)
	}
	if got := ids(e.Open("done", profile.ActorOperator, Flags{Paid: true, Approved: true})); len(got) != 0 {
		t.Fatalf("final state has transitions: %v", got)
	}
}

func TestApply(t *testing.T) {
	e := New(bakery(t).Workflow)
	if _, err := e.Apply("hand_over", "ready", profile.ActorOperator, Flags{}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("hand over without payment: %v", err)
	}
	tr, err := e.Apply("hand_over", "ready", profile.ActorOperator, Flags{Paid: true})
	if err != nil || tr.To != "done" {
		t.Fatalf("hand over: %v %v", tr, err)
	}
	if _, err := e.Apply("accept", "new", profile.ActorCustomer, Flags{Approved: true}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("customer accepted own order: %v", err)
	}
	if _, err := e.Apply("teleport", "new", profile.ActorOperator, Flags{}); !errors.Is(err, ErrUnknownTransition) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestBlockedExplainsWhy(t *testing.T) {
	e := New(bakery(t).Workflow)
	if got := e.Blocked("ready", profile.ActorOperator, Flags{}); got["hand_over"] != profile.FlagPaid {
		t.Fatalf("blocked = %v", got)
	}
}

func TestExpiring(t *testing.T) {
	p := bakery(t)
	finals := map[string]bool{}
	for _, s := range p.Workflow.States {
		finals[s.ID] = s.Final
	}
	e := New(p.Workflow)
	if tr, ok := e.Expiring("new", finals); !ok || tr.ID != "expire" {
		t.Fatalf("expiring from new: %v %v", tr, ok)
	}
	if _, ok := e.Expiring("accepted", finals); ok {
		t.Fatal("accepted orders must not expire")
	}
}
