package order

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
)

// Creating the customer's shop by one tap («🚀 Создать магазин» in the bot). The site cannot start containers,
// so it leaves a request in a directory the server's provisioner watches (deploy/provision): <order>.req, a
// JSON of the name and what the customer typed. The provisioner makes the shop and answers with <order>.done:
// its address and the owner's link, or why it failed. The site picks the answer up, hands the links over and
// closes the order.

// ProvisionRequest is <order>.req.
type ProvisionRequest struct {
	Order int64  `json:"order"`
	Name  string `json:"name"`  // the address: name.free-sellers.com
	Title string `json:"title"` // the shop's name, may be empty
	About string `json:"about"` // what it sells, may be empty
	At    string `json:"at"`
}

// ProvisionAnswer is <order>.done.
type ProvisionAnswer struct {
	OK    bool   `json:"ok"`
	URL   string `json:"url"`
	Admin string `json:"admin"`
	Error string `json:"error"`
}

// CanProvision says the order is ready for «Создать магазин»: the site has a provisioner, the order is paid,
// the address is there, and no shop was made or asked for yet.
func (s *Service) CanProvision(o *Order) bool {
	if s.ProvisionDir == "" || len(s.P.Checkout.Setup) == 0 || s.IsFinal(o.State) {
		return false
	}
	st := o.Fields["provision"]
	return (o.Paid || o.AdvanceAt != nil) && o.Fields[s.P.Checkout.Setup[0]] != "" && (st == "" || st == "failed")
}

// RequestShop leaves the request for the provisioner.
func (s *Service) RequestShop(ctx context.Context, o *Order, operatorID string) error {
	if !s.CanProvision(o) {
		return ErrForbidden
	}
	setup := s.P.Checkout.Setup
	req := ProvisionRequest{Order: o.ID, Name: o.Fields[setup[0]], At: s.Now().UTC().Format(time.RFC3339)}
	if len(setup) > 1 {
		req.Title = o.Fields[setup[1]]
	}
	if len(setup) > 2 {
		req.About = o.Fields[setup[2]]
	}
	if err := os.MkdirAll(s.ProvisionDir, 0o750); err != nil {
		return err
	}
	id := strconv.FormatInt(o.ID, 10)
	_ = os.Remove(filepath.Join(s.ProvisionDir, id+".done")) // a retry after a failure
	if err := writeJSON(filepath.Join(s.ProvisionDir, id+".req"), req); err != nil {
		return err
	}
	o.Fields["provision"] = "queued"
	now := s.Now()
	o.UpdatedAt = now
	return s.Store.Save(ctx, o, Event{At: now, Actor: profile.ActorOperator, ActorID: operatorID, Kind: "provision", Note: "создаём магазин " + req.Name})
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WatchProvision picks the provisioner's answers up until ctx ends.
func (s *Service) WatchProvision(ctx context.Context) {
	if s.ProvisionDir == "" {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ProvisionTick(ctx)
		}
	}
}

// ProvisionTick handles every answer waiting in the directory; exported for tests.
func (s *Service) ProvisionTick(ctx context.Context) {
	files, _ := filepath.Glob(filepath.Join(s.ProvisionDir, "*.done"))
	for _, f := range files {
		id, err := strconv.ParseInt(strings.TrimSuffix(filepath.Base(f), ".done"), 10, 64)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(f) // #nosec G304 -- a file of the site's own provisioning directory
		if err != nil {
			continue
		}
		var a ProvisionAnswer
		if err := json.Unmarshal(raw, &a); err != nil {
			continue
		}
		if err := s.provisioned(ctx, id, a); err != nil {
			s.Log.Warn("provision answer", "order", id, "err", err)
			continue
		}
		_ = os.Rename(f, f+".seen")
	}
}

func (s *Service) provisioned(ctx context.Context, id int64, a ProvisionAnswer) error {
	o, err := s.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if o.Fields["provision"] != "queued" {
		return nil // an answer to a request this order no longer waits for
	}
	now := s.Now()
	if !a.OK {
		o.Fields["provision"] = "failed"
		o.UpdatedAt = now
		return s.save(ctx, o, Event{At: now, Actor: profile.ActorSystem, Kind: "provision_failed", Note: clip(a.Error, 400)})
	}
	o.Fields["provision"], o.Fields["shop_url"] = "done", a.URL
	o.UpdatedAt = now
	if err := s.save(ctx, o, Event{At: now, Actor: profile.ActorSystem, Kind: "provisioned", Note: a.URL}); err != nil {
		return err
	}
	// the order walks its good way to the end: taken, done
	for range 4 {
		if o, err = s.Store.Get(ctx, id); err != nil || s.IsFinal(o.State) {
			break
		}
		next := ""
		for _, t := range s.Flow.Open(o.State, profile.ActorOperator, o.Flags()) {
			if !strings.HasPrefix(t.ID, "cancel") && !strings.HasPrefix(t.ID, "expire") && !strings.HasPrefix(t.ID, "reject") {
				next = t.ID
				break
			}
		}
		if next == "" {
			break
		}
		if err := s.Transition(ctx, o, next, profile.ActorOperator, "provision"); err != nil {
			return fmt.Errorf("transition %s: %w", next, err)
		}
	}
	if a.Admin == "" {
		return nil
	}
	if o, err = s.Store.Get(ctx, id); err != nil {
		return err
	}
	return s.SetAdminLink(ctx, o, "provision", a.Admin)
}

func clip(s string, n int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > n {
		return string(r[:n]) + "…"
	}
	return strings.TrimSpace(s)
}
