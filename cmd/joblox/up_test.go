package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/profile"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/web"
)

func newLauncher(t *testing.T) *launcher {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("JOBLOX_PROFILE", filepath.Join(dir, "profile.yaml"))
	t.Setenv("JOBLOX_REQUISITES", "")
	l := &launcher{dir: dir, restart: make(chan struct{}, 1), wake: make(chan struct{}, 1)}
	base := "https://abc.trycloudflare.com"
	l.base.Store(&base)
	return l
}

func TestSetupWritesAWorkingProfile(t *testing.T) {
	for _, k := range web.Kinds {
		t.Run(k.ID, func(t *testing.T) {
			l := newLauncher(t)
			in := web.SetupInput{Kind: k.ID, Name: `Такси "Азамат": 24/7`, Contact: "+996 700 000 000", Place: "Чолпон-Ата",
				About: "Camry", Country: "kg", Show: true}
			if err := l.setup(in); err != nil {
				t.Fatal(err)
			}
			p, err := profile.Load(os.Getenv("JOBLOX_PROFILE"))
			if err != nil {
				t.Fatal(err)
			}
			if p.Business.Name != in.Name || p.Business.Currency != "KGS" || p.Business.Timezone != "Asia/Bishkek" || p.Business.Description != "Camry" {
				t.Fatalf("business: %+v", p.Business)
			}
			if !slices.Contains(p.Payment.Methods, profile.MethodCash) {
				t.Fatal("no requisites and no cash: the shop could take no order")
			}
			if _, err := os.Stat(l.path("requisites.yaml")); !os.IsNotExist(err) {
				t.Fatal("requisites written without payment details")
			}
			if !l.configured() || !l.shown() {
				t.Fatal("settings not saved")
			}
			if c := l.listing(); c == nil || c.Title != in.Name || c.URL != "https://abc.trycloudflare.com" {
				t.Fatalf("listing: %+v", c)
			}
		})
	}
}

func TestSetupWithRequisites(t *testing.T) {
	l := newLauncher(t)
	if err := l.setup(web.SetupInput{Kind: "shop", Name: "Лавка", Contact: "+996", Country: "kz", Pay: "Kaspi +7 700 000 00 00"}); err != nil {
		t.Fatal(err)
	}
	req, err := order.LoadRequisites(os.Getenv("JOBLOX_REQUISITES"))
	if err != nil || req.Lines[0].Value != "Kaspi +7 700 000 00 00" {
		t.Fatalf("requisites: %+v %v", req, err)
	}
	if l.listing() != nil {
		t.Fatal("a card for an owner who did not ask for the market")
	}
	if err := l.setShown(true); err != nil || l.listing() == nil {
		t.Fatal("show on the market")
	}
	select {
	case <-l.restart:
	case <-t.Context().Done():
	}
}

func TestEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "joblox.env")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("no template written on the first start")
	}
	t.Setenv("JOBLOX_TEST_A", "")
	t.Setenv("JOBLOX_TEST_B", "from the environment")
	if err := os.WriteFile(path, []byte("# comment\nJOBLOX_TEST_A = \"quoted\"\nJOBLOX_TEST_B=from the file\n#JOBLOX_TEST_C=off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("JOBLOX_TEST_A") != "quoted" || os.Getenv("JOBLOX_TEST_B") != "from the environment" || os.Getenv("JOBLOX_TEST_C") != "" {
		t.Fatal("env file parsing")
	}
}

func TestKeyFileIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.key")
	a, err := keyFile(path)
	if err != nil || len(a) != 64 {
		t.Fatalf("key: %q %v", a, err)
	}
	if b, _ := keyFile(path); b != a {
		t.Fatal("the master key changed between starts")
	}
}
