package sysexec

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFixArgs(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	cases := []struct {
		goos   string
		in     []string
		exe    func(string) bool
		out    []string
		loader string
	}{
		// what the phone showed: the name it was called by first, then the full path, the loader nowhere
		{"android", []string{"./joblox", "/data/joblox/joblox", "up"}, yes, []string{"/data/joblox/joblox", "up"}, "/system/bin/linker64"},
		{"android", []string{"/data/joblox/joblox", "/data/joblox/joblox", "version"}, yes, []string{"/data/joblox/joblox", "version"}, "/system/bin/linker64"},
		{"android", []string{"/system/bin/linker64", "/data/joblox/joblox", "up"}, no, []string{"/data/joblox/joblox", "up"}, "/system/bin/linker64"},
		// started directly: nothing to fix
		{"android", []string{"/data/joblox/joblox", "up"}, no, []string{"/data/joblox/joblox", "up"}, ""},
		{"android", []string{"./joblox"}, yes, []string{"./joblox"}, ""},
		{"linux", []string{"./joblox", "/opt/joblox", "up"}, yes, []string{"./joblox", "/opt/joblox", "up"}, ""},
	}
	for _, c := range cases {
		out, loader := fixArgs(c.goos, "arm64", c.in, c.exe)
		if !slices.Equal(out, c.out) || loader != c.loader {
			t.Errorf("%v: %v %q", c.in, out, loader)
		}
	}
}

func TestCommandThroughTheLoader(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "open-url")
	if err := os.WriteFile(script, []byte("#!/usr/bin/sh -e\necho hi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "cloudflared")
	if err := os.WriteFile(bin, []byte("\x7fELF..."), 0o700); err != nil {
		t.Fatal(err)
	}
	Loader = "/system/bin/linker64"
	defer func() { Loader = "" }()
	if c := CommandContext(t.Context(), bin, "tunnel"); !slices.Equal(c.Args, []string{"/system/bin/linker64", bin, "tunnel"}) {
		t.Fatalf("binary: %v", c.Args)
	}
	want := []string{"/system/bin/linker64", "/usr/bin/sh", "-e", script, "https://x"}
	if c := CommandContext(t.Context(), script, "https://x"); !slices.Equal(c.Args, want) {
		t.Fatalf("script: %v", c.Args)
	}
}
