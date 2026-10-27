package mail

import (
	"context"
	"strings"
	"testing"
)

func TestCompose(t *testing.T) {
	raw := string(Compose("Пекарня <shop@example.com>", "anna@example.ru", "Код: 123456", "строка 1\nстрока 2"))
	for _, want := range []string{"From: =?utf-8?q?", "<shop@example.com>", "To: anna@example.ru", "Subject: =?utf-8?q?",
		"Message-ID: <", "@example.com>", "charset=utf-8", "строка 1\r\nстрока 2"} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %q in\n%s", want, raw)
		}
	}
}

func TestHeaderInjection(t *testing.T) {
	s := &SMTP{Host: "127.0.0.1", Port: 1}
	if err := s.Send(context.Background(), "a@b.ru\r\nBcc: x@y.ru", "s", "b"); err == nil || !strings.Contains(err.Error(), "injection") {
		t.Fatalf("injection: %v", err)
	}
}

func TestOutbox(t *testing.T) {
	var o Outbox
	for i := 0; i < 105; i++ {
		_ = o.Send(context.Background(), "a@b.ru", "s", "b")
	}
	_ = o.Send(context.Background(), "last@b.ru", "s", "b")
	m := o.Messages()
	if len(m) != 100 || m[0].To != "last@b.ru" {
		t.Fatalf("outbox: %d %s", len(m), m[0].To)
	}
}
