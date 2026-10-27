package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dmitry-Fedotov-Dev/openline-shop/internal/order"
)

func sample() *order.Order {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	return &order.Order{Token: "t1", State: "new", Lines: []order.Line{{ItemID: "a", Title: "A", Qty: 1, Price: 100, Sum: 100}},
		Fees: []order.FeeLine{}, Fields: map[string]string{"email": "a@b.ru"}, Email: "a@b.ru", Method: "transfer",
		Total: 100, Priced: true, Approved: true, RiskReasons: []string{}, ClientHash: "c", CreatedAt: now, UpdatedAt: now}
}

func TestRoundTripAndJournal(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "shop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	o := sample()
	if err := s.Create(ctx, o, order.Event{At: o.CreatedAt, Actor: "customer", Kind: "created"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ByToken(ctx, "t1")
	if err != nil || got.ID != o.ID || got.Lines[0].Title != "A" || got.Fields["email"] != "a@b.ru" || got.Version != 1 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := s.db.Exec(`UPDATE events SET note='edited'`); err == nil {
		t.Fatal("journal row was edited")
	}
	if _, err := s.db.Exec(`DELETE FROM events`); err == nil {
		t.Fatal("journal row was deleted")
	}
	if _, err := s.ByToken(ctx, "missing"); err != order.ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestReopenKeepsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(context.Background(), sample(), order.Event{Kind: "created"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("second open migrated again: %v", err)
	}
	defer s.Close()
	list, err := s.List(context.Background(), order.Filter{Open: true}, []string{"done"})
	if err != nil || len(list) != 1 {
		t.Fatalf("after reopen: %d %v", len(list), err)
	}
}

func TestBlocklistAndCounts(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	o := sample()
	if err := s.Create(ctx, o, order.Event{Kind: "created"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Counts(ctx, "a@b.ru", "c", 100, []string{"done"}, o.CreatedAt.Add(time.Minute))
	if err != nil || c.ByClientHour != 1 || c.ByEmailDay != 1 || c.OpenUnpaid != 1 || c.DuplicateHour != 1 {
		t.Fatalf("counts: %+v %v", c, err)
	}
	if err := s.Block(ctx, "a@b.ru", "test"); err != nil {
		t.Fatal(err)
	}
	if hit, _ := s.IsBlocked(ctx, "", "a@b.ru"); !hit {
		t.Fatal("blocked email not found")
	}
	_ = s.Unblock(ctx, "a@b.ru")
	if hit, _ := s.IsBlocked(ctx, "a@b.ru"); hit {
		t.Fatal("unblock did not work")
	}
}
