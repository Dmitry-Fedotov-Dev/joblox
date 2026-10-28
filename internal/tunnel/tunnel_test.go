package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDownloadStreamsWithProgress(t *testing.T) {
	body := bytes.Repeat([]byte("cloudflared"), 300_000) // ~3 MB, several reads
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body))) // GitHub sends it: the window shows "N из M МБ"
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	var calls int
	var last, total int64
	path := filepath.Join(t.TempDir(), "cloudflared.part")
	got, err := download(context.Background(), srv.URL, path, func(done, all int64) { calls, last, total = calls+1, done, all })
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if got != hex.EncodeToString(sum[:]) {
		t.Fatal("checksum of the streamed file")
	}
	if calls < 2 || last != int64(len(body)) || total != int64(len(body)) {
		t.Fatalf("progress: %d calls, last %d of %d", calls, last, total)
	}
	if raw, _ := os.ReadFile(path); !bytes.Equal(raw, body) {
		t.Fatal("file on disk differs from the download")
	}
}

func TestQuickURL(t *testing.T) {
	line := "2026-10-28T06:07:55Z INF |  https://amp-dow-slots-song.trycloudflare.com                          |"
	if u := quickURL.FindString(line); u != "https://amp-dow-slots-song.trycloudflare.com" {
		t.Fatalf("url: %q", u)
	}
	if !registered.MatchString("INF Registered tunnel connection connIndex=0") {
		t.Fatal("registered line")
	}
}
