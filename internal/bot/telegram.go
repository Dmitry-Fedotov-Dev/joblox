package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// API is the small part of the Telegram Bot API the operator bot uses. Base is
// https://api.telegram.org/bot<token>; tests point it at a fake server.
type API struct {
	Base   string
	HTTP   *http.Client
	Header map[string]string // extra headers: the shop's address and key when Telegram is the hub
}

func NewAPI(token string) *API {
	return &API{Base: "https://api.telegram.org/bot" + token, HTTP: &http.Client{Timeout: 70 * time.Second}}
}

type Update struct {
	UpdateID int64          `json:"update_id"`
	Message  *Message       `json:"message"`
	Callback *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type Keyboard struct {
	Rows [][]Button `json:"inline_keyboard"`
}

type reply struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

func (a *API) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.Header {
		req.Header.Set(k, v)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var r reply
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// Me asks Telegram who the bot is: it also checks the token.
func (a *API) Me(ctx context.Context) (User, error) {
	var u User
	err := a.call(ctx, "getMe", map[string]any{}, &u)
	return u, err
}

func (a *API) Updates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var out []Update
	err := a.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": timeout,
		"allowed_updates": []string{"message", "callback_query"}}, &out)
	return out, err
}

func (a *API) Send(ctx context.Context, chat int64, text string, kb *Keyboard) (int64, error) {
	payload := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	var m Message
	err := a.call(ctx, "sendMessage", payload, &m)
	return m.MessageID, err
}

// SendMenu sends text with the reply keyboard that stays under the input field: the operator's menu.
func (a *API) SendMenu(ctx context.Context, chat int64, text string, rows [][]string) error {
	kb := make([][]map[string]string, 0, len(rows))
	for _, r := range rows {
		row := make([]map[string]string, 0, len(r))
		for _, t := range r {
			row = append(row, map[string]string{"text": t})
		}
		kb = append(kb, row)
	}
	payload := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true,
		"reply_markup": map[string]any{"keyboard": kb, "resize_keyboard": true, "is_persistent": true}}
	return a.call(ctx, "sendMessage", payload, nil)
}

func (a *API) Edit(ctx context.Context, chat, message int64, text string, kb *Keyboard) error {
	payload := map[string]any{"chat_id": chat, "message_id": message, "text": text, "parse_mode": "HTML",
		"disable_web_page_preview": true}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	return a.call(ctx, "editMessageText", payload, nil)
}

func (a *API) Answer(ctx context.Context, callbackID, text string) error {
	return a.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text}, nil)
}
