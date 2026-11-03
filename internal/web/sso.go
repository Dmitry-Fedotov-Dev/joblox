package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Owner sign-in with Google (the hosting and the turnkey tariffs) and Apple ID (turnkey). The owner first comes
// in with the master link and links an account in the settings; from then on «Войти через Google» opens the
// back office too. The master link keeps working: it is the way back when an account is lost.
//
// The id token comes straight from the provider's token endpoint over TLS in exchange for the code, so its
// claims are trusted without checking the signature (OpenID Connect Core 3.1.3.7).

// SSO holds the providers the server has keys for; nil fields are off.
type SSO struct {
	Google *GoogleSSO
	Apple  *AppleSSO
}

// GoogleSSO is an OAuth client of Google Cloud («Web application»); its redirect URI is
// <site>/owner/sso/google/callback.
type GoogleSSO struct {
	ClientID, Secret  string
	AuthURL, TokenURL string // empty: Google's
}

// AppleSSO is a «Services ID» of Sign in with Apple with a key (.p8) of the team; its return URL is
// <site>/owner/sso/apple/callback.
type AppleSSO struct {
	ServiceID, TeamID, KeyID string
	Key                      *ecdsa.PrivateKey
	AuthURL, TokenURL        string // empty: Apple's
}

// ParseAppleKey reads the .p8 key Apple gives for Sign in with Apple.
func ParseAppleKey(raw []byte) (*ecdsa.PrivateKey, error) {
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, errors.New("apple key: not a PEM file")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apple key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apple key: not an EC key")
	}
	return ec, nil
}

type identity struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
}

type ssoProvider struct {
	ID, Title string
	auth      func(redirect, state, nonce, challenge string) string
	exchange  func(ctx context.Context, code, redirect, verifier, nonce string) (identity, error)
}

func (s *Server) providers() []ssoProvider {
	var out []ssoProvider
	if g := s.cfg.SSO.Google; g != nil {
		out = append(out, ssoProvider{ID: "google", Title: "Google", auth: g.authURL, exchange: g.exchange})
	}
	if a := s.cfg.SSO.Apple; a != nil {
		out = append(out, ssoProvider{ID: "apple", Title: "Apple ID", auth: a.authURL, exchange: a.exchange})
	}
	return out
}

func (s *Server) provider(id string) (ssoProvider, bool) {
	for _, p := range s.providers() {
		if p.ID == id {
			return p, true
		}
	}
	return ssoProvider{}, false
}

func (s *Server) ssoOn() bool { return s.cfg.SSO.Google != nil || s.cfg.SSO.Apple != nil }

// ---------------------------------------------------------------- Google

func (g *GoogleSSO) authURL(redirect, state, nonce, challenge string) string {
	q := url.Values{"client_id": {g.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {"openid email"}, "state": {state}, "nonce": {nonce}, "code_challenge": {challenge},
		"code_challenge_method": {"S256"}, "prompt": {"select_account"}}
	return or(g.AuthURL, "https://accounts.google.com/o/oauth2/v2/auth") + "?" + q.Encode()
}

func (g *GoogleSSO) exchange(ctx context.Context, code, redirect, verifier, nonce string) (identity, error) {
	c, err := tokenClaims(ctx, or(g.TokenURL, "https://oauth2.googleapis.com/token"), url.Values{"code": {code},
		"client_id": {g.ClientID}, "client_secret": {g.Secret}, "redirect_uri": {redirect},
		"grant_type": {"authorization_code"}, "code_verifier": {verifier}})
	if err != nil {
		return identity{}, err
	}
	if c.Iss != "https://accounts.google.com" && c.Iss != "accounts.google.com" {
		return identity{}, errors.New("google: wrong issuer")
	}
	if err := c.check(g.ClientID, nonce); err != nil {
		return identity{}, err
	}
	if !c.verified() {
		return identity{}, errors.New("google: the email is not verified")
	}
	return identity{Sub: c.Sub, Email: c.Email}, nil
}

// ---------------------------------------------------------------- Apple

func (a *AppleSSO) authURL(redirect, state, nonce, _ string) string {
	q := url.Values{"client_id": {a.ServiceID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {"email"}, "response_mode": {"form_post"}, "state": {state}, "nonce": {nonce}}
	return or(a.AuthURL, "https://appleid.apple.com/auth/authorize") + "?" + q.Encode()
}

func (a *AppleSSO) exchange(ctx context.Context, code, redirect, _, nonce string) (identity, error) {
	secret, err := a.clientSecret(time.Now())
	if err != nil {
		return identity{}, err
	}
	c, err := tokenClaims(ctx, or(a.TokenURL, "https://appleid.apple.com/auth/token"), url.Values{"code": {code},
		"client_id": {a.ServiceID}, "client_secret": {secret}, "redirect_uri": {redirect}, "grant_type": {"authorization_code"}})
	if err != nil {
		return identity{}, err
	}
	if c.Iss != "https://appleid.apple.com" {
		return identity{}, errors.New("apple: wrong issuer")
	}
	if err := c.check(a.ServiceID, nonce); err != nil {
		return identity{}, err
	}
	return identity{Sub: c.Sub, Email: c.Email}, nil
}

// clientSecret is the short-lived JWT Apple takes instead of a password: ES256, signed with the team's key.
func (a *AppleSSO) clientSecret(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.KeyID})
	body, _ := json.Marshal(map[string]any{"iss": a.TeamID, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		"aud": "https://appleid.apple.com", "sub": a.ServiceID})
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(body)
	sum := sha256.Sum256([]byte(signing))
	r, sg, err := ecdsa.Sign(rand.Reader, a.Key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sg.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// ---------------------------------------------------------------- the id token

type claims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"` // a string or a list
	Sub           string          `json:"sub"`
	Email         string          `json:"email"`
	EmailVerified any             `json:"email_verified"` // Google: a bool, Apple: «true» as a string
	Exp           int64           `json:"exp"`
	Nonce         string          `json:"nonce"`
}

func (c claims) check(aud, nonce string) error {
	var one string
	var many []string
	ok := json.Unmarshal(c.Aud, &one) == nil && one == aud
	if !ok && json.Unmarshal(c.Aud, &many) == nil {
		for _, a := range many {
			ok = ok || a == aud
		}
	}
	switch {
	case !ok:
		return errors.New("sso: the token is for another client")
	case c.Sub == "":
		return errors.New("sso: no subject")
	case time.Now().Unix() > c.Exp:
		return errors.New("sso: the token expired")
	case c.Nonce != nonce:
		return errors.New("sso: wrong nonce")
	}
	return nil
}

func (c claims) verified() bool {
	switch v := c.EmailVerified.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

var ssoHTTP = &http.Client{Timeout: 15 * time.Second}

func tokenClaims(ctx context.Context, endpoint string, form url.Values) (claims, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return claims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := ssoHTTP.Do(req)
	if err != nil {
		return claims{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return claims{}, fmt.Errorf("sso token: %s %.200s", resp.Status, raw)
	}
	var t struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &t); err != nil || t.IDToken == "" {
		return claims{}, errors.New("sso token: no id_token")
	}
	parts := strings.Split(t.IDToken, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("sso token: not a JWT")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, err
	}
	var c claims
	return c, json.Unmarshal(body, &c)
}

func or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------- the routes

const ssoCookie = "jx_sso"

func (s *Server) ssoRoutes(mux *http.ServeMux) {
	if !s.ssoOn() {
		return
	}
	mux.HandleFunc("GET /owner/login", s.ownerLogin)
	mux.HandleFunc("GET /owner/sso/{p}/start", s.ssoStart)
	mux.HandleFunc("GET /owner/sso/{p}/callback", s.ssoCallback)
	mux.HandleFunc("POST /owner/sso/{p}/callback", s.ssoCallback) // Apple answers with a form post
	mux.HandleFunc("POST /owner/sso/{p}/unlink", s.ownerOnly(s.ssoUnlink))
}

type loginView struct {
	base
	Providers []ssoProvider
}

func (s *Server) ownerLogin(w http.ResponseWriter, r *http.Request) {
	if s.isOwner(r) {
		s.redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.render(w, "login", http.StatusOK, loginView{base: s.base(w, r, "Вход"), Providers: s.providers()})
}

func (s *Server) linked(ctx context.Context, p string) *identity {
	raw := s.svc.Setting(ctx, "sso_"+p)
	if raw == "" {
		return nil
	}
	var id identity
	if json.Unmarshal([]byte(raw), &id) != nil || id.Sub == "" {
		return nil
	}
	return &id
}

func (s *Server) callbackURL(p string) string {
	return s.svc.BaseURL() + "/owner/sso/" + p + "/callback"
}

// ssoStart sends the browser to the provider. mode=link (the owner, from the settings) links an account;
// otherwise it is a sign-in.
func (s *Server) ssoStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(r.PathValue("p"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	mode := "login"
	if r.URL.Query().Get("mode") == "link" {
		v := s.visitor(w, r)
		if !s.isOwner(r) || r.URL.Query().Get("csrf") != s.csrfToken(v) {
			s.message(w, r, http.StatusForbidden, "Страница устарела", "Вернитесь в настройки и нажмите кнопку ещё раз.")
			return
		}
		mode = "link"
	}
	state, nonce, verifier := randomID(), randomID(), randomID()+randomID()
	sum := sha256.Sum256([]byte(verifier))
	exp := strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	val := strings.Join([]string{p.ID, mode, state, nonce, verifier, exp}, "|")
	http.SetCookie(w, s.ssoFlowCookie(r, base64.RawURLEncoding.EncodeToString([]byte(val))+"."+s.sign("sso", val), 600))
	http.Redirect(w, r, p.auth(s.callbackURL(p.ID), state, nonce, base64.RawURLEncoding.EncodeToString(sum[:])), http.StatusSeeOther)
}

// ssoFlowCookie keeps the state of one sign-in for ten minutes. Over https it is SameSite=None: Apple comes
// back with a cross-site form post, which a Lax cookie would not ride on.
func (s *Server) ssoFlowCookie(r *http.Request, value string, age int) *http.Cookie {
	c := &http.Cookie{Name: ssoCookie, Value: value, Path: s.cookiePath(), MaxAge: age, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if s.secure(r) {
		c.Secure, c.SameSite = true, http.SameSiteNoneMode
	}
	return c
}

func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	p, ok := s.provider(r.PathValue("p"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.codes.Allow(ClientIP(r, s.cfg.Trusted)) {
		s.message(w, r, http.StatusTooManyRequests, "Слишком много попыток", "Подождите минуту.")
		return
	}
	fail := func(text string) {
		s.message(w, r, http.StatusForbidden, "Не получилось войти", text)
	}
	c, err := r.Cookie(ssoCookie)
	http.SetCookie(w, s.ssoFlowCookie(r, "", -1))
	if err != nil {
		fail("Вход занял слишком много времени. Нажмите кнопку входа ещё раз.")
		return
	}
	enc, mac, _ := strings.Cut(c.Value, ".")
	rawVal, err := base64.RawURLEncoding.DecodeString(enc)
	val := string(rawVal)
	f := strings.Split(val, "|")
	if err != nil || len(f) != 6 || mac != s.sign("sso", val) || f[0] != p.ID {
		fail("Нажмите кнопку входа ещё раз.")
		return
	}
	mode, state, nonce, verifier := f[1], f[2], f[3], f[4]
	if exp, _ := strconv.ParseInt(f[5], 10, 64); time.Now().Unix() > exp || r.FormValue("state") != state {
		fail("Вход занял слишком много времени. Нажмите кнопку входа ещё раз.")
		return
	}
	if r.FormValue("error") != "" || r.FormValue("code") == "" {
		fail("Вход отменён.")
		return
	}
	id, err := p.exchange(r.Context(), r.FormValue("code"), s.callbackURL(p.ID), verifier, nonce)
	if err != nil {
		s.log.Warn("sso", "provider", p.ID, "err", err)
		fail(p.Title + " не подтвердил вход. Попробуйте ещё раз.")
		return
	}
	if mode == "link" {
		if !s.isOwner(r) {
			fail("Сначала войдите в админку по мастер-ссылке.")
			return
		}
		raw, _ := json.Marshal(id)
		if err := s.svc.SetSetting(r.Context(), "sso_"+p.ID, string(raw)); err != nil {
			s.log.Error("sso link", "err", err)
			fail("Не получилось сохранить, попробуйте ещё раз.")
			return
		}
		s.log.Info("sso linked", "provider", p.ID)
		s.redirect(w, r, "/owner?sso=linked#sso", http.StatusSeeOther)
		return
	}
	if l := s.linked(r.Context(), p.ID); l == nil || l.Sub != id.Sub {
		fail("Этот аккаунт " + p.Title + " не привязан к админке. Войдите по мастер-ссылке и привяжите его в «Настройках».")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: ownerCookie, Value: s.ownerToken(), Path: s.cookiePath(), MaxAge: 30 * 24 * 3600,
		HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	s.log.Info("owner signed in", "provider", p.ID)
	s.redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) ssoUnlink(w http.ResponseWriter, r *http.Request) {
	if !s.ownerForm(w, r) {
		return
	}
	if _, ok := s.provider(r.PathValue("p")); ok {
		if err := s.svc.SetSetting(r.Context(), "sso_"+r.PathValue("p"), ""); err != nil {
			s.log.Error("sso unlink", "err", err)
		}
	}
	s.redirect(w, r, "/owner#sso", http.StatusSeeOther)
}

// ssoRow is a provider in the settings: linked to an account or not.
type ssoRow struct {
	ID, Title, Email string
	Linked           bool
}

func (s *Server) ssoRows(ctx context.Context) []ssoRow {
	var out []ssoRow
	for _, p := range s.providers() {
		row := ssoRow{ID: p.ID, Title: p.Title}
		if l := s.linked(ctx, p.ID); l != nil {
			row.Linked, row.Email = true, l.Email
		}
		out = append(out, row)
	}
	return out
}
