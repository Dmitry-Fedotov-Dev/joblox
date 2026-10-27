package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- client address

// ClientIP returns the visitor's address. X-Forwarded-For is believed only when the request comes from a
// configured proxy (Caddy on the same host, Cloudflare ranges): anyone else could write any address there.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if !inPrefixes(addr, trusted) {
		return addr.String()
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- { // the rightmost address not added by our own proxies
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			continue
		}
		if !inPrefixes(a, trusted) {
			return a.String()
		}
	}
	return addr.String()
}

func inPrefixes(a netip.Addr, list []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ParsePrefixes reads "127.0.0.1/32, ::1/128" or plain addresses.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if !strings.Contains(part, "/") {
			a, err := netip.ParseAddr(part)
			if err != nil {
				return nil, err
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// ---------------------------------------------------------------- rate limits

// Limiter is a token bucket per client: rate tokens a second, up to burst.
type Limiter struct {
	rate, burst float64
	mu          sync.Mutex
	buckets     map[string]*bucket
	lastSweep   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func NewLimiter(rate, burst float64) *Limiter {
	return &Limiter{rate: rate, burst: burst, buckets: map[string]*bucket{}, lastSweep: time.Now()}
}

func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.at) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ---------------------------------------------------------------- visitor cookie and CSRF

const visitorCookie = "jx_v"

func randomID() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) sign(parts ...string) string {
	m := hmac.New(sha256.New, s.secret)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// visitor returns the random visitor id, setting the cookie on the first visit.
func (s *Server) visitor(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(visitorCookie); err == nil && len(c.Value) >= 20 && len(c.Value) <= 40 {
		return c.Value
	}
	id := randomID()
	http.SetCookie(w, &http.Cookie{Name: visitorCookie, Value: id, Path: "/", MaxAge: 365 * 24 * 3600,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	return id
}

// csrfToken ties forms to the visitor cookie: another site cannot read the cookie, so it cannot forge the token.
func (s *Server) csrfToken(visitor string) string { return s.sign("csrf", visitor) }

func (s *Server) csrfOK(r *http.Request) bool {
	c, err := r.Cookie(visitorCookie)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(r.PostFormValue("csrf")), []byte(s.csrfToken(c.Value)))
}

// formStamp signs the moment the form was rendered; the checkout uses it to measure how fast it was filled.
func (s *Server) formStamp(now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return ts + "." + s.sign("form", ts)
}

func (s *Server) fillSeconds(stamp string, now time.Time) float64 {
	ts, sig, ok := strings.Cut(stamp, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign("form", ts))) {
		return 0 // a missing or forged stamp counts as "instant", which the anti-fraud scores
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0
	}
	return now.Sub(time.Unix(sec, 0)).Seconds()
}
