package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeGoogle is the token endpoint: it hands out an id token for whoever «signed in».
type fakeGoogle struct {
	sub, email string
	nonce      string // the nonce the browser was sent with, read from the auth URL
}

func (f *fakeGoogle) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostFormValue("code") != "good-code" || r.PostFormValue("client_secret") != "secret" ||
			r.PostFormValue("code_verifier") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(map[string]any{"iss": "https://accounts.google.com", "aud": "client-1", "sub": f.sub,
			"email": f.email, "email_verified": true, "exp": time.Now().Add(time.Hour).Unix(), "nonce": f.nonce})
		tok := "e30." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": tok})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// status asks for a page without following redirects: its status code and where it sends.
func status(t *testing.T, s *site, path string) (int, string) {
	t.Helper()
	resp, err := s.client.Get(s.http.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// signIn goes the browser's way: start, «Google», back to the callback with the state from the auth URL.
func signIn(t *testing.T, s *site, f *fakeGoogle, start string) (int, string) {
	t.Helper()
	resp, err := s.client.Get(s.http.URL + start)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	auth, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(auth.Path, "/auth") {
		t.Fatalf("start: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	q := auth.Query()
	if q.Get("code_challenge_method") != "S256" || !strings.HasSuffix(q.Get("redirect_uri"), "/owner/sso/google/callback") {
		t.Fatalf("auth URL: %s", auth)
	}
	f.nonce = q.Get("nonce")
	return status(t, s, "/owner/sso/google/callback?code=good-code&state="+url.QueryEscape(q.Get("state")))
}

func ssoSite(t *testing.T) (*site, *fakeGoogle) {
	s, f := ownerSite(t)
	f.setups = []SetupInput{{Kind: "bakery"}}
	g := &fakeGoogle{sub: "owner-1", email: "owner@gmail.com"}
	tok := g.serve(t)
	s.srv.cfg.SSO = SSO{Google: &GoogleSSO{ClientID: "client-1", Secret: "secret", AuthURL: "https://google.test/auth", TokenURL: tok.URL}}
	s.http.Config.Handler = s.srv.Handler() // the routes depend on the keys
	return s, g
}

func TestGoogleLinkThenSignIn(t *testing.T) {
	s, g := ssoSite(t)
	// a stranger is sent to the sign-in page instead of a 404
	if code, to := status(t, s, "/admin"); code != http.StatusSeeOther || to != "/owner/login" {
		t.Fatalf("stranger on /admin: %d %s", code, to)
	}
	if _, page := s.get(t, "/owner/login"); !strings.Contains(page, "Войти через Google") {
		t.Fatal("no Google button on the sign-in page")
	}
	// not linked yet: Google says who it is, the back office does not open
	if code, _ := signIn(t, s, g, "/owner/sso/google/start"); code != http.StatusForbidden {
		t.Fatalf("an account nobody linked opened the back office: %d", code)
	}
	// the owner comes in with the master link and links the account
	s.get(t, "/owner/enter?k=masterkey0123456789")
	_, settings := s.get(t, "/owner")
	if !strings.Contains(settings, "Привязать Google") {
		t.Fatal("the settings offer no Google link")
	}
	csrf := match(t, csrfRe, settings)
	if code, _ := status(t, s, "/owner/sso/google/start?mode=link&csrf=wrong"); code != http.StatusForbidden {
		t.Fatalf("link without the page's token: %d", code)
	}
	if code, to := signIn(t, s, g, "/owner/sso/google/start?mode=link&csrf="+url.QueryEscape(csrf)); code != http.StatusSeeOther ||
		!strings.Contains(to, "sso=linked") {
		t.Fatalf("link: %d %s", code, to)
	}
	if _, settings := s.get(t, "/owner"); !strings.Contains(settings, "owner@gmail.com") {
		t.Fatal("the linked account is not shown")
	}
	// signed out, back in with Google
	s.post(t, "/owner/logout", url.Values{"csrf": {csrf}})
	if code, _ := status(t, s, "/admin"); code != http.StatusSeeOther {
		t.Fatalf("still the owner after signing out: %d", code)
	}
	if code, to := signIn(t, s, g, "/owner/sso/google/start"); code != http.StatusSeeOther || to != "/admin" {
		t.Fatalf("sign-in: %d %s", code, to)
	}
	if code, _ := s.get(t, "/admin"); code != http.StatusOK {
		t.Fatalf("the back office after the sign-in: %d", code)
	}
	// someone else's Google account does not get in
	other, _ := ssoSiteSameKeys(t, s, g)
	g.sub = "intruder"
	if code, _ := signIn(t, other, g, "/owner/sso/google/start"); code != http.StatusForbidden {
		t.Fatalf("another account got in: %d", code)
	}
}

// ssoSiteSameKeys is a second browser on the same site: no cookies.
func ssoSiteSameKeys(t *testing.T, s *site, _ *fakeGoogle) (*site, struct{}) {
	t.Helper()
	b := *s
	jar, _ := cookiejar.New(nil)
	b.client = &http.Client{Jar: jar, CheckRedirect: s.client.CheckRedirect}
	return &b, struct{}{}
}

func TestSSOStateMustMatch(t *testing.T) {
	s, _ := ssoSite(t)
	status(t, s, "/owner/sso/google/start")
	if code, _ := status(t, s, "/owner/sso/google/callback?code=good-code&state=forged"); code != http.StatusForbidden {
		t.Fatalf("a forged state: %d", code)
	}
}

func TestNoSSOWithoutKeys(t *testing.T) {
	s, f := ownerSite(t)
	f.setups = []SetupInput{{Kind: "bakery"}}
	if code, _ := s.get(t, "/owner/login"); code == http.StatusOK {
		t.Fatal("a sign-in page without any provider")
	}
	if code, _ := status(t, s, "/admin"); code != http.StatusNotFound {
		t.Fatalf("a free install keeps the master link only: %d", code)
	}
}

func TestAppleClientSecret(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	parsed, err := ParseAppleKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	a := &AppleSSO{ServiceID: "one.openline.shop", TeamID: "TEAM123456", KeyID: "KEY1234567", Key: parsed}
	jwt, err := a.clientSecret(time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %s", jwt)
	}
	var head, body map[string]any
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(h, &head)
	_ = json.Unmarshal(b, &body)
	if head["alg"] != "ES256" || head["kid"] != "KEY1234567" || body["iss"] != "TEAM123456" || body["sub"] != "one.openline.shop" ||
		body["aud"] != "https://appleid.apple.com" {
		t.Fatalf("claims: %v %v", head, body)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the signature does not verify with the team's key")
	}
}
