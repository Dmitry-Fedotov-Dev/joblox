package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTelegram records what the shared bot sends.
type fakeTelegram struct {
	mu    sync.Mutex
	sent  []map[string]any
	calls []string
}

func (f *fakeTelegram) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		f.mu.Lock()
		f.calls = append(f.calls, method)
		if method == "sendMessage" {
			f.sent = append(f.sent, p)
		}
		f.mu.Unlock()
		switch method {
		case "getMe":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"username":"JobloxTestBot"}}`)
		case "getUpdates":
			time.Sleep(50 * time.Millisecond)
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":42}}}`)
		}
	})
}

// fakeShop is a shop's side: it gets the updates the hub forwards and checks their signature.
type fakeShop struct {
	mu      sync.Mutex
	secret  string
	got     []string
	badSign int
}

func (f *fakeShop) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bot/relay/verify" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		mac := hmac.New(sha256.New, []byte(f.secret))
		mac.Write(body)
		if hex.EncodeToString(mac.Sum(nil)) != r.Header.Get("X-Relay-Sign") {
			f.badSign++
			return
		}
		f.got = append(f.got, string(body))
	})
}

func call(t *testing.T, method, url, site, secret, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader([]byte(body)))
	if site != "" {
		req.Header.Set("X-Relay-Site", site)
		req.Header.Set("X-Relay-Secret", secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// A shop on the hosting connects the shared bot: it registers (the hub calls it back), gets a one-time link,
// the seller presses Start; the shop then writes only to its own chats, its buttons carry its number, and what
// the seller presses comes back to it signed.
func TestSharedBot(t *testing.T) {
	tg, shop := &fakeTelegram{}, &fakeShop{}
	tgSrv, shopSrv := httptest.NewServer(tg.handler()), httptest.NewServer(shop.handler())
	defer tgSrv.Close()
	defer shopSrv.Close()
	oldAPI, oldAddr := telegramAPI, siteAddr
	telegramAPI, siteAddr = tgSrv.URL+"/bot", func(string) string { return shopSrv.URL }
	defer func() { telegramAPI, siteAddr = oldAPI, oldAddr }()

	h, srv, _ := newHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.StartBot(ctx, "TOKEN"); err != nil {
		t.Fatal(err)
	}
	const site = "https://valya.openline.one"

	if code, _ := call(t, "POST", srv.URL+"/api/v1/relay/register", "", "", `{"url":"https://evil.example","name":"x","nonce":"0123456789abcdef"}`); code != http.StatusForbidden {
		t.Fatalf("a site off the hosting registered: %d", code)
	}
	code, out := call(t, "POST", srv.URL+"/api/v1/relay/register", "", "", `{"url":"`+site+`","name":"Магазин Вали","nonce":"0123456789abcdef"}`)
	secret, _ := out["secret"].(string)
	if code != http.StatusOK || secret == "" || out["bot"] != "JobloxTestBot" {
		t.Fatalf("register: %d %v", code, out)
	}
	shop.mu.Lock()
	shop.secret = secret
	shop.mu.Unlock()

	if code, _ := call(t, "POST", srv.URL+"/api/v1/relay/link", site, "wrong", `{}`); code != http.StatusForbidden {
		t.Fatalf("a wrong key got a link: %d", code)
	}
	code, out = call(t, "POST", srv.URL+"/api/v1/relay/link", site, secret, `{}`)
	link, _ := out["link"].(string)
	if code != http.StatusOK || !strings.HasPrefix(link, "https://t.me/JobloxTestBot?start=") {
		t.Fatalf("link: %d %v", code, out)
	}
	startCode := strings.TrimPrefix(link, "https://t.me/JobloxTestBot?start=")

	// the seller presses Start with the code: the chat is linked and the shop gets «/start»
	r := h.bot()
	r.handle(ctx, tgUpdate{UpdateID: 1, Message: json.RawMessage(`{"message_id":5,"chat":{"id":42},"from":{"id":42},"text":"/start ` + startCode + `"}`)})
	if cs, _ := h.chats(ctx, 1); len(cs) != 1 || cs[0] != 42 {
		t.Fatalf("chats: %v", cs)
	}
	shop.mu.Lock()
	if len(shop.got) != 1 || !strings.Contains(shop.got[0], `"/start"`) || shop.badSign != 0 {
		t.Fatalf("forwarded: %v (bad signatures %d)", shop.got, shop.badSign)
	}
	shop.mu.Unlock()

	// the shop writes to everyone (chat 0): it reaches chat 42 with buttons tagged by the shop's number
	code, _ = call(t, "POST", srv.URL+"/api/v1/relay/tg/sendMessage", site, secret,
		`{"chat_id":0,"text":"Заказ №1","reply_markup":{"inline_keyboard":[[{"text":"✅","callback_data":"p:1"}]]}}`)
	tg.mu.Lock()
	last := tg.sent[len(tg.sent)-1]
	tg.mu.Unlock()
	kb, _ := json.Marshal(last["reply_markup"])
	if code != http.StatusOK || last["chat_id"] != float64(42) || !strings.Contains(string(kb), `"1|p:1"`) {
		t.Fatalf("broadcast: %d %v %s", code, last, kb)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v1/relay/tg/sendMessage", site, secret, `{"chat_id":99,"text":"spam"}`); code != http.StatusForbidden {
		t.Fatalf("wrote to a chat not linked to it: %d", code)
	}
	if code, _ := call(t, "POST", srv.URL+"/api/v1/relay/tg/getUpdates", site, secret, `{}`); code != http.StatusForbidden {
		t.Fatalf("a shop read the shared bot's updates: %d", code)
	}

	// the seller presses the button: the shop gets its own data back
	r.handle(ctx, tgUpdate{UpdateID: 2, Callback: json.RawMessage(`{"id":"cb1","from":{"id":42},"data":"1|p:1","message":{"message_id":7,"chat":{"id":42}}}`)})
	shop.mu.Lock()
	defer shop.mu.Unlock()
	if len(shop.got) != 2 || !strings.Contains(shop.got[1], `"data":"p:1"`) {
		t.Fatalf("the button press: %v", shop.got)
	}
}
