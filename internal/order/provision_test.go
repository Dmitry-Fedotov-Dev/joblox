package order_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// «Создать магазин»: the request goes to the provisioner's directory; its answer hands the links over and
// closes the order; a failure lets the operator try again.
func TestProvision(t *testing.T) {
	svc, ctx := tariffService(t)
	svc.ProvisionDir = t.TempDir()
	o, _, err := svc.Create(ctx, order.CreateInput{Items: map[string]int{"hosting": 1}, Method: profile.MethodTransfer,
		ClientIP: "203.0.113.7", FillSeconds: 10, Quick: true})
	if err != nil {
		t.Fatal(err)
	}
	if svc.CanProvision(o) {
		t.Fatal("a shop for an unpaid order")
	}
	if err := svc.ConfirmPayment(ctx, o, "op", ""); err != nil {
		t.Fatal(err)
	}
	o, _ = svc.Store.Get(ctx, o.ID)
	if err := svc.SetSetup(ctx, o, map[string]string{"site": "valya", "shop": "Магазин Вали"}); err != nil {
		t.Fatal(err)
	}
	o, _ = svc.Store.Get(ctx, o.ID)
	if err := svc.RequestShop(ctx, o, "op"); err != nil {
		t.Fatal(err)
	}
	var req order.ProvisionRequest
	raw, err := os.ReadFile(filepath.Join(svc.ProvisionDir, "1.req"))
	if err != nil || json.Unmarshal(raw, &req) != nil || req.Name != "valya" || req.Title != "Магазин Вали" {
		t.Fatalf("request: %s %v", raw, err)
	}
	o, _ = svc.Store.Get(ctx, o.ID)
	if svc.CanProvision(o) {
		t.Fatal("asked twice")
	}
	answer := func(a order.ProvisionAnswer) {
		b, _ := json.Marshal(a)
		if err := os.WriteFile(filepath.Join(svc.ProvisionDir, "1.done"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		svc.ProvisionTick(ctx)
	}
	answer(order.ProvisionAnswer{OK: false, Error: "the name is taken"})
	if o, _ = svc.Store.Get(ctx, o.ID); !svc.CanProvision(o) || o.Fields["provision"] != "failed" {
		t.Fatalf("after a failure: %v", o.Fields)
	}
	if err := svc.RequestShop(ctx, o, "op"); err != nil {
		t.Fatal(err)
	}
	answer(order.ProvisionAnswer{OK: true, URL: "https://valya.free-sellers.com/", Admin: "https://valya.free-sellers.com/owner/enter?k=key"})
	o, _ = svc.Store.Get(ctx, o.ID)
	if !svc.IsFinal(o.State) || o.Fields["admin_link"] == "" || o.Fields["provision"] != "done" {
		t.Fatalf("after the shop is made: %s %v", o.State, o.Fields)
	}
}

func tariffService(t *testing.T) (*order.Service, context.Context) {
	t.Helper()
	return newEnv(t, "openline").svc, context.Background()
}
