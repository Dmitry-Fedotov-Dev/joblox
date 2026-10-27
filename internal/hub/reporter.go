package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Reporter sends this installation's heartbeat to a Joblox Network hub. It runs only when the owner sets
// JOBLOX_NETWORK_URL; the id is random and stays the same across restarts, so a restart is not a new shop.
type Reporter struct {
	URL         string // hub base address, e.g. https://joblox.example.com
	ID          string
	Kind        string
	Version     string
	Online      func() int
	OrdersToday func(ctx context.Context) int
	Listing     func() *Listing // the owner's market card, nil while it is not shown
	Wake        <-chan struct{} // report soon (as soon as the hub allows): the card or the address changed
	Every       time.Duration
	HTTP        *http.Client
	Log         *slog.Logger
	lastWarn    time.Time
}

// NewID makes a random installation id.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Send posts one heartbeat.
func (r *Reporter) Send(ctx context.Context) error {
	b := Beat{ID: r.ID, Kind: strings.ToLower(r.Kind), Version: r.Version, Online: r.Online(), OrdersToday: r.OrdersToday(ctx)}
	if r.Listing != nil {
		b.Listing = r.Listing()
	}
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.URL, "/")+"/api/v1/beat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "joblox/"+r.Version)
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("hub answered %s", resp.Status)
	}
	return nil
}

// Run reports right away and then every r.Every (5 minutes by default) until ctx ends.
// Failures are logged at most once an hour: the shop must not notice the hub being down.
func (r *Reporter) Run(ctx context.Context) {
	every := r.Every
	if every == 0 {
		every = 5 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		last := time.Now()
		if err := r.Send(ctx); err != nil && ctx.Err() == nil && time.Since(r.lastWarn) > time.Hour {
			r.lastWarn = time.Now()
			if r.Log != nil {
				r.Log.Warn("joblox network heartbeat", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.Wake:
			// the hub takes one beat a minute from an installation
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(last.Add(BeatEvery + 5*time.Second))):
			}
		}
	}
}
