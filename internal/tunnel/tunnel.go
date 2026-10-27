// Package tunnel publishes a local shop to the internet through Cloudflare Tunnel, so the owner needs no
// server, no white IP and no domain to start. By default it is a quick tunnel: a random *.trycloudflare.com
// address that changes on every connection. With a tunnel token from the Cloudflare dashboard it runs the
// owner's named tunnel on their own domain instead.
package tunnel

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Version of cloudflared downloaded when none is installed. The sums are from its release notes: a file
// that does not match is not run.
const Version = "2026.10.0"

var sums = map[string]string{
	"cloudflared-windows-amd64.exe": "86aee4017b26625cee8484c113558f48effa4cd47f7aa05fcf425604e5d2b23c",
	"cloudflared-windows-386.exe":   "0630a8779e9823a1a3b091698b8e71874e0f7b205559219f52fdd301466b5546",
	"cloudflared-linux-amd64":       "d33ff2d14475178d2012c2c56beba87389ac5ded27649519f198a7d3134a99db",
	"cloudflared-linux-arm64":       "e6422b9d4f72d3194bc5a38676f13667c06666523217b842a877d72a80b5ac08",
	"cloudflared-darwin-amd64.tgz":  "0560c9ab7281ac3f746055323623ed23bc0405b6dab9400474020cba33a978da",
	"cloudflared-darwin-arm64.tgz":  "72edfd3eea463aef4d5cb89e2e209cecb048cc756c2b01915de2e0ad7cb39830",
}

func asset() string {
	switch runtime.GOOS {
	case "windows":
		return "cloudflared-windows-" + runtime.GOARCH + ".exe"
	case "darwin":
		return "cloudflared-darwin-" + runtime.GOARCH + ".tgz"
	}
	return "cloudflared-" + runtime.GOOS + "-" + runtime.GOARCH
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// Ensure returns a cloudflared to run: the one in dir, the one on PATH, or a fresh download into dir.
func Ensure(ctx context.Context, dir string, log *slog.Logger) (string, error) {
	local := filepath.Join(dir, exeName())
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}
	name := asset()
	want, ok := sums[name]
	if !ok {
		return "", fmt.Errorf("no cloudflared build for %s/%s: install it yourself (https://github.com/cloudflare/cloudflared) and start again",
			runtime.GOOS, runtime.GOARCH)
	}
	url := "https://github.com/cloudflare/cloudflared/releases/download/" + Version + "/" + name
	log.Info("downloading cloudflared " + Version + " (about 40 MB, once)")
	body, err := download(ctx, url)
	if err != nil {
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", fmt.Errorf("cloudflared %s: checksum %s, expected %s; the file is not used", name, got, want)
	}
	if strings.HasSuffix(name, ".tgz") {
		if body, err = untar(body, "cloudflared"); err != nil {
			return "", fmt.Errorf("unpack cloudflared: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	tmp := local + ".part"
	if err := os.WriteFile(tmp, body, 0o750); err != nil { // #nosec G306 -- an executable
		return "", err
	}
	return local, os.Rename(tmp, local)
}

func download(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func untar(tgz []byte, name string) ([]byte, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(tgz)))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s not in the archive: %w", name, err)
		}
		if filepath.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// Tunnel keeps cloudflared running and restarts it when it falls.
type Tunnel struct {
	Bin   string // cloudflared
	Local string // http://127.0.0.1:port of the shop
	Token string // a named tunnel's token; empty runs a quick tunnel
	Log   *slog.Logger
	// Ready is called each time the tunnel is up, with its public address (empty for a named tunnel:
	// its address is the owner's domain).
	Ready func(url string)
}

var (
	quickURL   = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	registered = regexp.MustCompile(`(?i)registered tunnel connection`)
)

// Run blocks until ctx ends.
func (t *Tunnel) Run(ctx context.Context) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := t.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			wait = 2 * time.Second
		}
		t.Log.Warn("tunnel dropped, reconnecting", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func (t *Tunnel) once(ctx context.Context) error {
	args := []string{"tunnel", "--no-autoupdate"}
	if t.Token != "" {
		args = append(args, "run", "--token", t.Token)
	} else {
		args = append(args, "--url", t.Local)
	}
	cmd := exec.CommandContext(ctx, t.Bin, args...) // #nosec G204 -- our own cloudflared with our own arguments
	cmd.Env = append(os.Environ(), "TUNNEL_ORIGIN_CERT=")
	out, err := cmd.StderrPipe() // cloudflared logs to stderr
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	url, announced := "", false
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		line := sc.Text()
		if url == "" {
			url = quickURL.FindString(line)
		}
		if !announced && registered.MatchString(line) && (url != "" || t.Token != "") {
			announced = true
			if t.Ready != nil {
				t.Ready(url)
			}
		}
		if strings.Contains(line, " ERR ") {
			t.Log.Debug("cloudflared", "line", line)
		}
	}
	err = cmd.Wait()
	if err == nil {
		err = errors.New("cloudflared exited")
	}
	return err
}
