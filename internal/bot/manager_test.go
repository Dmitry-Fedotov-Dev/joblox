package bot

import (
	"context"
	"testing"
)

type memSettings map[string]string

func (m memSettings) Meta(_ context.Context, k string) (string, error) { return m[k], nil }
func (m memSettings) SetMeta(_ context.Context, k, v string) error     { m[k] = v; return nil }

// The cabinet's link lets one person join once; the code changes after it is used; a wrong code does nothing.
func TestJoinByLink(t *testing.T) {
	ctx := context.Background()
	st := memSettings{}
	m := NewManager(nil, st, "test", nil)
	code, err := m.newLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.join(ctx, 42, "wrong") {
		t.Fatal("a wrong code joined")
	}
	if !m.join(ctx, 42, code) {
		t.Fatal("the right code did not join")
	}
	if m.join(ctx, 43, code) {
		t.Fatal("the code worked twice")
	}
	if ops, _ := m.operators(ctx); len(ops) != 1 || ops[0] != 42 {
		t.Fatalf("operators: %v", ops)
	}
}

func TestValidToken(t *testing.T) {
	for tok, ok := range map[string]bool{
		"123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0": true,
		"123456789:short": false, "nocolon": false, "abc:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0": false,
		"123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsa w0": false,
	} {
		if validToken(tok) != ok {
			t.Errorf("%q: want %v", tok, ok)
		}
	}
}
