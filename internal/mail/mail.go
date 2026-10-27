// Package mail sends the customer's codes and status changes: SMTP in production, an in-memory outbox
// for local runs and tests.
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SMTP sends through a relay. Port 465 uses implicit TLS, any other port STARTTLS (required unless Insecure).
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string // "Пекарня «Колос» <shop@example.com>"
	Insecure bool   // allow plain SMTP: only for a relay on localhost
	Timeout  time.Duration
}

func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("header injection refused")
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if s.Port == 465 {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if s.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		} else if !s.Insecure {
			return fmt.Errorf("smtp %s offers no STARTTLS", addr)
		}
	}
	if s.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return err
		}
	}
	from := s.From
	if a, err := parseAddr(from); err == nil {
		from = a
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(Compose(s.From, to, subject, body)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func parseAddr(s string) (string, error) {
	if i, j := strings.LastIndex(s, "<"), strings.LastIndex(s, ">"); i >= 0 && j > i {
		return s[i+1 : j], nil
	}
	if strings.Contains(s, "@") {
		return s, nil
	}
	return "", fmt.Errorf("bad address %q", s)
}

// Compose builds a UTF-8 plain-text message with encoded headers.
func Compose(from, to, subject, body string) []byte {
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "localhost"
	if a, err := parseAddr(from); err == nil {
		if _, d, ok := strings.Cut(a, "@"); ok {
			domain = d
		}
	}
	if i := strings.LastIndex(from, "<"); i > 0 {
		from = mime.QEncoding.Encode("utf-8", strings.TrimSpace(from[:i])) + " " + from[i:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\n", from, to,
		mime.QEncoding.Encode("utf-8", subject), time.Now().Format(time.RFC1123Z), hex.EncodeToString(id), domain)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}

// Message is a letter kept by the Outbox.
type Message struct {
	At      time.Time `json:"at"`
	To      string    `json:"to"`
	Subject string    `json:"subject"`
	Body    string    `json:"body"`
}

// Outbox keeps the last letters in memory instead of sending them (JOBLOX_ENV=dev and tests).
type Outbox struct {
	mu   sync.Mutex
	msgs []Message
}

func (o *Outbox) Send(_ context.Context, to, subject, body string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.msgs = append(o.msgs, Message{At: time.Now().UTC(), To: to, Subject: subject, Body: body})
	if len(o.msgs) > 100 {
		o.msgs = o.msgs[len(o.msgs)-100:]
	}
	return nil
}

// Messages returns the letters, newest first.
func (o *Outbox) Messages() []Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Message, len(o.msgs))
	for i, m := range o.msgs {
		out[len(o.msgs)-1-i] = m
	}
	return out
}
