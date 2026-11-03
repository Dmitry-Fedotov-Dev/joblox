// Package sysexec starts programs the way the system allows. On Android, a recent Termux starts every program
// through the system loader (/system/bin/linker64) instead of executing the file itself: an app may not execute
// files from its own data folder. A Go program then sees the loader as its own argv[0] and the program's path as
// its first argument, and it has to start its children (cloudflared, termux-open-url) through the loader too.
package sysexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Loader is the system loader this program was started through; empty when it was started directly.
var Loader string

// Self is the path of this program: os.Executable names the loader when Loader is set.
func Self() (string, error) {
	if Loader != "" {
		return filepath.Abs(os.Args[0])
	}
	return os.Executable()
}

// FixArgs drops the loader from os.Args and remembers it. Call it first thing in main.
func FixArgs() { os.Args, Loader = fixArgs(runtime.GOOS, runtime.GOARCH, os.Args, isExecutable) }

func fixArgs(goos, goarch string, args []string, executable func(string) bool) ([]string, string) {
	if goos != "android" || len(args) < 2 {
		return args, ""
	}
	// Termux keeps the name the program was called by in argv[0] (./joblox, joblox) and puts the full path
	// second; the loader itself is not in the list. It is the system's one, at a fixed place.
	if strings.HasPrefix(filepath.Base(args[0]), "linker") || (strings.HasPrefix(args[1], "/") && executable(args[1])) {
		return args[1:], systemLoader(goarch)
	}
	return args, ""
}

// systemLoader is Android's own loader for programs of this processor.
func systemLoader(goarch string) string {
	if strings.HasSuffix(goarch, "64") {
		return "/system/bin/linker64"
	}
	return "/system/bin/linker"
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// CommandContext is exec.CommandContext that goes through the loader when this program did. A script (a file
// that starts with «#!») is handed to its interpreter, which the loader then starts.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if Loader == "" {
		return exec.CommandContext(ctx, name, args...) // #nosec G204 -- the caller's own program
	}
	path := name
	if !filepath.IsAbs(path) {
		if p, err := exec.LookPath(name); err == nil {
			path = p
		}
	}
	argv := append([]string{path}, args...)
	if interp := interpreter(path); interp != nil {
		argv = append(interp, argv...)
	}
	return exec.CommandContext(ctx, Loader, argv...) // #nosec G204 -- the system loader with our own program
}

// interpreter reads the «#!» line of a script: ["/…/usr/bin/sh"] or ["/…/env", "sh"]; nil for a binary.
func interpreter(path string) []string {
	f, err := os.Open(path) // #nosec G304 -- a program we are about to start
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	line := string(buf[:n])
	if !strings.HasPrefix(line, "#!") {
		return nil
	}
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.Fields(strings.TrimPrefix(line, "#!"))
}
