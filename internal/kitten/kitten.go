// Package kitten is Dymok, the smoke kitten that walks behind the pages of the storefront and the market.
// Two scripts, no dependencies and nothing loaded from other sites: dymok.js draws him, kitten.js teaches
// him to stay out of the way. Visitors who asked for reduced motion never see him.
package kitten

import (
	_ "embed"
	"net/http"
)

//go:embed dymok.js
var dymok []byte

//go:embed kitten.js
var behaviour []byte

// Handler serves /dymok.js and /kitten.js under whatever prefix it is mounted at.
func Handler() http.Handler {
	mux := http.NewServeMux()
	serve := func(body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(body)
		}
	}
	mux.HandleFunc("GET /dymok.js", serve(dymok))
	mux.HandleFunc("GET /kitten.js", serve(behaviour))
	return mux
}
